package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

func sniHello(host string) []byte {
	name := append([]byte{0, byte(len(host) >> 8), byte(len(host))}, host...)
	list := binary.BigEndian.AppendUint16(nil, uint16(len(name)))
	list = append(list, name...)
	ext := binary.BigEndian.AppendUint16([]byte{0, 0}, uint16(len(list)))
	ext = append(ext, list...)
	body := append([]byte{3, 3}, make([]byte, 32)...)
	body = append(body, 0, 0, 2, 0x13, 1, 1, 0)
	body = binary.BigEndian.AppendUint16(body, uint16(len(ext)))
	body = append(body, ext...)
	return append([]byte{1, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
}

func tlsRecord(body []byte) []byte {
	head := binary.BigEndian.AppendUint16([]byte{22, 3, 3}, uint16(len(body)))
	return append(head, body...)
}

func TestClientHelloAcceptsFragmentationAndPreservesAllRecords(t *testing.T) {
	hello := sniHello("ASSETS1.XBOXLIVE.COM.")
	for _, wire := range [][]byte{
		tlsRecord(hello),
		append(tlsRecord(hello[:2]), tlsRecord(hello[2:])...),
		append(append(tlsRecord(hello[:2]), tlsRecord(hello[2:19])...), tlsRecord(hello[19:])...),
	} {
		host, got, err := clientHello(bytes.NewReader(wire))
		if err != nil || host != "assets1.xboxlive.com" || !bytes.Equal(got, wire) {
			t.Fatalf("fragmented hello = %q, %x, %v", host, got, err)
		}
	}
}

func TestClientHelloRejectsTruncationMalformedSNIAndOversizedRecords(t *testing.T) {
	wire := tlsRecord(sniHello("assets1.xboxlive.com"))
	for size := 0; size < len(wire); size++ {
		if _, _, err := clientHello(bytes.NewReader(wire[:size])); err == nil {
			t.Fatalf("accepted hello truncated at byte %d/%d", size, len(wire))
		}
	}
	for _, tc := range []struct {
		name string
		wire []byte
	}{
		{"application data", []byte{23, 3, 3, 0, 1, 0}},
		{"empty record", []byte{22, 3, 3, 0, 0}},
		{"oversized record", []byte{22, 3, 3, 0xff, 0xff}},
		{"wrong handshake type", tlsRecord([]byte{2, 0, 0, 0})},
		{"oversized handshake", tlsRecord([]byte{1, 1, 0, 0})},
		{"missing extensions", tlsRecord(append([]byte{1, 0, 0, 34}, make([]byte, 34)...))},
		{"empty SNI", tlsRecord(sniHello(""))},
		{"SNI normalizes to empty", tlsRecord(sniHello("."))},
		{"too long SNI", tlsRecord(sniHello(strings.Repeat("a", 254)))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := clientHello(bytes.NewReader(tc.wire)); err == nil {
				t.Fatal("accepted malformed ClientHello")
			}
		})
	}
}

func TestTLSUpstreamUsesIndependentDNSAndRejectsLoops(t *testing.T) {
	for _, tc := range []struct {
		name, lan, want, wantError string
		addresses                  [][]byte
	}{
		{"first public answer", "192.168.1.10", "1.1.1.1", "", [][]byte{dnsA(1, 1, 1, 1), dnsA(8, 8, 8, 8)}},
		{"skip own public address", "1.1.1.1", "8.8.8.8", "", [][]byte{dnsA(1, 1, 1, 1), dnsA(8, 8, 8, 8)}},
		{"only own address", "1.1.1.1", "", "points back", [][]byte{dnsA(1, 1, 1, 1)}},
		{"no usable answer", "192.168.1.10", "", "no public IPv4", [][]byte{dnsA(127, 0, 0, 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.LANIP = tc.lan
			cfg.UpstreamDNS = dnsFixture(t, func(req []byte, _ bool) []byte { return dnsAnswer(req, 0x8180, tc.addresses...) })
			r := &Runtime{cfg: cfg}
			ip, err := r.TLSUpstream(context.Background(), "assets2.xboxlive.com")
			if ip != tc.want || (tc.wantError == "" && err != nil) || (tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError))) {
				t.Fatalf("TLS destination = %q, %v", ip, err)
			}
		})
	}
}

func FuzzClientHello(f *testing.F) {
	f.Add(tlsRecord(sniHello("assets1.xboxlive.com")))
	f.Add(tlsRecord(sniHello(".")))
	f.Add([]byte{22, 3, 3, 0, 0})
	f.Add([]byte("GET / HTTP/1.1\r\n"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		host, consumed, err := clientHello(bytes.NewReader(wire))
		if err == nil {
			if len(host) == 0 || len(host) > 253 || !bytes.HasPrefix(wire, consumed) || len(consumed) > 65536 {
				t.Fatalf("invalid accepted hello: host=%q, wire bytes=%d", host, len(consumed))
			}
		}
	})
}
