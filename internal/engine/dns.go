package engine

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
	"xboxdownloadproxy/internal/config"
	"xboxdownloadproxy/internal/domain"
)

type question struct {
	name  string
	typ   uint16
	class uint16
	end   int
}

func readName(b []byte, start int) (string, int, error) {
	var labels []string
	pos := start
	end := -1
	visited := map[int]bool{}
	for steps := 0; steps < 128; steps++ {
		if pos >= len(b) || visited[pos] {
			return "", 0, errors.New("invalid DNS name")
		}
		visited[pos] = true
		n := int(b[pos])
		pos++
		if n == 0 {
			if end < 0 {
				end = pos
			}
			s := strings.Join(labels, ".")
			if len(s) > 253 {
				return "", 0, errors.New("DNS name too long")
			}
			return domain.Normalize(s), end, nil
		}
		if n&0xc0 == 0xc0 {
			if pos >= len(b) {
				break
			}
			if end < 0 {
				end = pos + 1
			}
			pos = ((n & 63) << 8) | int(b[pos])
			continue
		}
		if n > 63 || pos+n > len(b) {
			break
		}
		labels = append(labels, string(b[pos:pos+n]))
		pos += n
	}
	return "", 0, errors.New("malformed DNS name")
}
func parseQuestion(b []byte) (question, error) {
	if len(b) < 12 || binary.BigEndian.Uint16(b[4:6]) != 1 || b[2]&0x80 != 0 || b[2]&0x78 != 0 {
		return question{}, errors.New("expected one standard DNS question")
	}
	name, end, e := readName(b, 12)
	if e != nil || end+4 > len(b) {
		return question{}, errors.New("malformed DNS question")
	}
	return question{name, binary.BigEndian.Uint16(b[end:]), binary.BigEndian.Uint16(b[end+2:]), end + 4}, nil
}
func reply(b []byte, q question, ip net.IP, rcode uint16) []byte {
	// Re-encode the question: never preserve compression pointers into removed sections.
	out := make([]byte, 12)
	copy(out[:2], b[:2])
	flags := uint16(0x8080) | (binary.BigEndian.Uint16(b[2:4]) & 0x0110) | rcode
	binary.BigEndian.PutUint16(out[2:4], flags)
	binary.BigEndian.PutUint16(out[4:6], 1)
	for _, label := range strings.Split(q.name, ".") {
		if label != "" {
			out = append(out, byte(len(label)))
			out = append(out, label...)
		}
	}
	out = append(out, 0, byte(q.typ>>8), byte(q.typ), byte(q.class>>8), byte(q.class))
	if ip != nil && ip.To4() != nil && q.typ == 1 && rcode == 0 {
		binary.BigEndian.PutUint16(out[6:8], 1)
		out = append(out, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
		out = append(out, ip.To4()...)
	}
	return out
}
func query(host string) []byte {
	b := make([]byte, 12)
	_, _ = rand.Read(b[:2])
	b[2] = 1
	b[5] = 1
	for _, s := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		b = append(b, byte(len(s)))
		b = append(b, s...)
	}
	return append(b, 0, 0, 1, 0, 1)
}

func exchange(ctx context.Context, upstream string, b []byte, tcp bool) ([]byte, error) {
	network := "udp"
	if tcp {
		network = "tcp"
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, e := d.DialContext(ctx, network, upstream)
	if e != nil {
		return nil, e
	}
	defer conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if tcp {
		framed := make([]byte, 2+len(b))
		binary.BigEndian.PutUint16(framed, uint16(len(b)))
		copy(framed[2:], b)
		if _, e = conn.Write(framed); e != nil {
			return nil, e
		}
		head := make([]byte, 2)
		if _, e = io.ReadFull(conn, head); e != nil {
			return nil, e
		}
		n := int(binary.BigEndian.Uint16(head))
		if n < 12 {
			return nil, errors.New("short upstream DNS reply")
		}
		out := make([]byte, n)
		if _, e = io.ReadFull(conn, out); e != nil {
			return nil, e
		}
		return validateDNSReply(b, out)
	}
	if _, e = conn.Write(b); e != nil {
		return nil, e
	}
	out := make([]byte, 65535)
	n, e := conn.Read(out)
	if e != nil {
		return nil, e
	}
	return validateDNSReply(b, out[:n])
}
func validateDNSReply(req, res []byte) ([]byte, error) {
	if len(res) < 12 || req[0] != res[0] || req[1] != res[1] || res[2]&0x80 == 0 {
		return nil, errors.New("invalid upstream DNS response")
	}
	return res, nil
}
func ResolveIPv4(ctx context.Context, host, upstream string) ([]string, error) {
	req := query(host)
	b, e := exchange(ctx, upstream, req, false)
	if e == nil && b[2]&2 != 0 {
		b, e = exchange(ctx, upstream, req, true)
	}
	if e != nil {
		return nil, e
	}
	if b[3]&15 != 0 {
		return nil, fmt.Errorf("DNS returned rcode %d", b[3]&15)
	}
	_, end, e := readName(b, 12)
	if e != nil || end+4 > len(b) {
		return nil, errors.New("invalid DNS answer")
	}
	pos := end + 4
	var out []string
	for n := 0; n < int(binary.BigEndian.Uint16(b[6:8])); n++ {
		_, end, e = readName(b, pos)
		if e != nil || end+10 > len(b) {
			return nil, errors.New("invalid DNS record")
		}
		typ := binary.BigEndian.Uint16(b[end:])
		size := int(binary.BigEndian.Uint16(b[end+8:]))
		pos = end + 10
		if pos+size > len(b) {
			return nil, errors.New("short DNS record")
		}
		if typ == 1 && size == 4 {
			ip := net.IP(b[pos : pos+4]).String()
			if config.PublicIPv4(ip) {
				out = append(out, ip)
			}
		}
		pos += size
	}
	if len(out) == 0 {
		return nil, errors.New("upstream returned no public IPv4 addresses")
	}
	return out, nil
}
func (r *Runtime) answerDNS(b []byte, client string, tcp bool) []byte {
	q, e := parseQuestion(b)
	if e != nil {
		return nil
	}
	r.dnsQueries.Add(1)
	if q.class == 1 {
		if q.typ == 1 {
			if ip := domain.Address(r.cfg, q.name); ip != "" {
				r.log("DNS", client, q.name, "A → "+ip, "")
				return reply(b, q, net.ParseIP(ip), 0)
			}
		}
		if q.typ == 28 && r.cfg.SuppressAAAA && domain.GroupFor(q.name) != "" {
			r.log("DNS", client, q.name, "AAAA 空答复（下载域名 IPv4 优选）", "")
			return reply(b, q, nil, 0)
		}
	}
	out, e := exchange(r.ctx, r.cfg.UpstreamDNS, b, tcp)
	if e != nil {
		r.log("DNS_ERROR", client, q.name, e.Error(), "")
		return reply(b, q, nil, 2)
	}
	if domain.GroupFor(q.name) != "" {
		r.log("DNS", client, q.name, "转发到上游 DNS", "")
	}
	return out
}
func (r *Runtime) serveUDP() {
	b := make([]byte, 65535)
	for {
		n, addr, e := r.udp.ReadFrom(b)
		if e != nil {
			return
		}
		if !localClient(addr.String()) {
			continue
		}
		req := append([]byte(nil), b[:n]...)
		select {
		case r.slots <- struct{}{}:
			go func() {
				defer func() { <-r.slots }()
				if out := r.answerDNS(req, addr.String(), false); out != nil {
					_, _ = r.udp.WriteTo(out, addr)
				}
			}()
		default:
		}
	}
}
func (r *Runtime) serveDNSTCP() {
	for {
		c, e := r.dnsTCP.Accept()
		if e != nil {
			return
		}
		if !r.admit(c) {
			continue
		}
		go func() {
			defer r.release(c)
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			for n := 0; n < 100; n++ {
				var head [2]byte
				if _, e := io.ReadFull(c, head[:]); e != nil {
					return
				}
				size := int(binary.BigEndian.Uint16(head[:]))
				if size < 12 {
					return
				}
				b := make([]byte, size)
				if _, e := io.ReadFull(c, b); e != nil {
					return
				}
				out := r.answerDNS(b, c.RemoteAddr().String(), true)
				if out == nil {
					return
				}
				frame := make([]byte, len(out)+2)
				binary.BigEndian.PutUint16(frame, uint16(len(out)))
				copy(frame[2:], out)
				if _, e := c.Write(frame); e != nil {
					return
				}
			}
		}()
	}
}
