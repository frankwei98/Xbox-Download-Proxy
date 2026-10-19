package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Keep the UDP and TCP fixture on the same port, as a real DNS server is.
func dnsFixture(t *testing.T, respond func([]byte, bool) []byte) string {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		tcp.Close()
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		buf := make([]byte, 65535)
		for {
			n, addr, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			if out := respond(append([]byte(nil), buf[:n]...), false); out != nil {
				_, _ = udp.WriteTo(out, addr)
			}
		}
	}()
	go func() {
		defer workers.Done()
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				var head [2]byte
				if _, err := io.ReadFull(conn, head[:]); err != nil {
					return
				}
				req := make([]byte, binary.BigEndian.Uint16(head[:]))
				if _, err := io.ReadFull(conn, req); err != nil {
					return
				}
				if out := respond(req, true); out != nil {
					frame := binary.BigEndian.AppendUint16(nil, uint16(len(out)))
					_, _ = conn.Write(append(frame, out...))
				}
			}()
		}
	}()
	t.Cleanup(func() {
		udp.Close()
		tcp.Close()
		workers.Wait()
	})
	return tcp.Addr().String()
}

// Construct answers independently of reply(), so a broken encoder cannot make
// both sides of the assertion agree on an invalid packet.
func dnsAnswer(req []byte, flags uint16, records ...[]byte) []byte {
	out := append([]byte(nil), req...)
	binary.BigEndian.PutUint16(out[2:4], flags)
	binary.BigEndian.PutUint16(out[6:8], uint16(len(records)))
	for _, rr := range records {
		out = append(out, rr...)
	}
	return out
}

func dnsA(a, b, c, d byte) []byte {
	return []byte{0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, a, b, c, d}
}

func TestResolveIPv4ReturnsOnlyPublicARecords(t *testing.T) {
	upstream := dnsFixture(t, func(req []byte, tcp bool) []byte {
		// A CNAME and an AAAA record must not be interpreted as IPv4 addresses.
		cname := []byte{0xc0, 0x0c, 0, 5, 0, 1, 0, 0, 0, 60, 0, 2, 0xc0, 0x0c}
		aaaa := append([]byte{0xc0, 0x0c, 0, 28, 0, 1, 0, 0, 0, 60, 0, 16}, make([]byte, 16)...)
		return dnsAnswer(req, 0x8180, cname, dnsA(1, 1, 1, 1), dnsA(192, 168, 1, 1), aaaa, dnsA(8, 8, 8, 8))
	})
	ips, err := ResolveIPv4(context.Background(), "cdn.example", upstream)
	if err != nil || !reflect.DeepEqual(ips, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Fatalf("public answers = %v, %v", ips, err)
	}
}

func TestResolveIPv4RetriesTruncatedUDPOverTCP(t *testing.T) {
	var udpCalls, tcpCalls atomic.Int32
	upstream := dnsFixture(t, func(req []byte, tcp bool) []byte {
		if tcp {
			tcpCalls.Add(1)
			return dnsAnswer(req, 0x8180, dnsA(8, 8, 4, 4))
		}
		udpCalls.Add(1)
		return dnsAnswer(req, 0x8380)
	})
	ips, err := ResolveIPv4(context.Background(), "cdn.example", upstream)
	if err != nil || !reflect.DeepEqual(ips, []string{"8.8.4.4"}) || udpCalls.Load() != 1 || tcpCalls.Load() != 1 {
		t.Fatalf("TCP fallback = %v, %v; UDP=%d TCP=%d", ips, err, udpCalls.Load(), tcpCalls.Load())
	}
}

func TestResolveIPv4RejectsInvalidUpstreamReplies(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		packet     func([]byte) []byte
	}{
		{"short header", "invalid upstream", func(req []byte) []byte { return req[:5] }},
		{"wrong ID", "invalid upstream", func(req []byte) []byte { b := dnsAnswer(req, 0x8180); b[0] ^= 1; return b }},
		{"query instead of reply", "invalid upstream", func(req []byte) []byte { return dnsAnswer(req, 0x0100) }},
		{"NXDOMAIN", "rcode 3", func(req []byte) []byte { return dnsAnswer(req, 0x8183) }},
		{"missing question", "invalid DNS answer", func(req []byte) []byte { return dnsAnswer(req, 0x8180)[:12] }},
		{"short question", "invalid DNS answer", func(req []byte) []byte { return dnsAnswer(req, 0x8180)[:len(req)-1] }},
		{"short record", "invalid DNS record", func(req []byte) []byte { b := dnsAnswer(req, 0x8180, dnsA(1, 1, 1, 1)); return b[:len(req)+5] }},
		{"short RDATA", "short DNS record", func(req []byte) []byte { b := dnsAnswer(req, 0x8180, dnsA(1, 1, 1, 1)); return b[:len(b)-1] }},
		{"no A record", "no public IPv4", func(req []byte) []byte { return dnsAnswer(req, 0x8180) }},
		{"private A only", "no public IPv4", func(req []byte) []byte { return dnsAnswer(req, 0x8180, dnsA(127, 0, 0, 1)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := dnsFixture(t, func(req []byte, _ bool) []byte { return tc.packet(req) })
			ips, err := ResolveIPv4(context.Background(), "cdn.example", upstream)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(ips) != 0 {
				t.Fatalf("answers = %v, error = %v; want %q", ips, err, tc.want)
			}
		})
	}
}

func TestDNSUpstreamTimeoutCancellationAndTCPForwarding(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		upstream := dnsFixture(t, func([]byte, bool) []byte { return nil })
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := ResolveIPv4(ctx, "cdn.example", upstream)
		if e, ok := err.(net.Error); !ok || !e.Timeout() {
			t.Fatalf("want timeout, got %v", err)
		}
	})
	t.Run("cancelled before dialing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := ResolveIPv4(ctx, "cdn.example", "127.0.0.1:53"); err == nil {
			t.Fatal("cancelled lookup succeeded")
		}
	})
	t.Run("forward TCP and suppress upstream failure", func(t *testing.T) {
		var tcpCalls atomic.Int32
		upstream := dnsFixture(t, func(req []byte, tcp bool) []byte {
			if tcp {
				tcpCalls.Add(1)
			}
			return dnsAnswer(req, 0x8180, dnsA(8, 8, 8, 8))
		})
		cfg := testConfig(t)
		cfg.UpstreamDNS = upstream
		r, err := Start(cfg, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		b, err := exchange(context.Background(), cfg.DNSListen, query("example.com"), true)
		if err != nil || len(b) < 4 || !bytes.Equal(b[len(b)-4:], []byte{8, 8, 8, 8}) || tcpCalls.Load() != 1 {
			t.Fatalf("TCP forward %x %v, calls=%d", b, err, tcpCalls.Load())
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		failure := &Runtime{cfg: cfg, ctx: ctx, log: func(string, string, string, string, string) {}}
		b = failure.answerDNS(query("example.com"), "127.0.0.1:1234", false)
		if len(b) < 12 || b[3]&15 != 2 || binary.BigEndian.Uint16(b[6:8]) != 0 {
			t.Fatalf("upstream failure must produce SERVFAIL: %x", b)
		}
	})
}
