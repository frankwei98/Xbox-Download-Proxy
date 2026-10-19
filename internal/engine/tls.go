package engine

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

// clientHello reads TLS records without terminating TLS. Every byte is replayed
// unchanged to the selected CDN; no certificate or private key is generated.
func clientHello(reader io.Reader) (string, []byte, error) {
	var wire, handshake []byte
	for len(wire) < 65536 {
		head := make([]byte, 5)
		if _, e := io.ReadFull(reader, head); e != nil {
			return "", nil, e
		}
		size := int(binary.BigEndian.Uint16(head[3:]))
		if head[0] != 22 || size == 0 || size > 18432 || len(wire)+size+5 > 65536 {
			return "", nil, errors.New("invalid TLS handshake record")
		}
		body := make([]byte, size)
		if _, e := io.ReadFull(reader, body); e != nil {
			return "", nil, e
		}
		wire = append(wire, head...)
		wire = append(wire, body...)
		handshake = append(handshake, body...)
		if len(handshake) < 4 {
			continue
		}
		want := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
		if handshake[0] != 1 || want > 65532 {
			return "", nil, errors.New("expected TLS ClientHello")
		}
		if len(handshake) < want+4 {
			continue
		}
		host, e := helloSNI(handshake[4 : want+4])
		return host, wire, e
	}
	return "", nil, errors.New("TLS ClientHello too large")
}
func helloSNI(b []byte) (string, error) {
	fail := errors.New("missing or malformed TLS server name")
	if len(b) < 35 {
		return "", fail
	}
	p := 34
	session := int(b[p])
	p++
	p += session
	if p+2 > len(b) {
		return "", fail
	}
	n := int(binary.BigEndian.Uint16(b[p:]))
	p += 2 + n
	if p >= len(b) {
		return "", fail
	}
	n = int(b[p])
	p += 1 + n
	if p+2 > len(b) {
		return "", fail
	}
	n = int(binary.BigEndian.Uint16(b[p:]))
	p += 2
	end := p + n
	if end > len(b) {
		return "", fail
	}
	for p+4 <= end {
		typ := binary.BigEndian.Uint16(b[p:])
		size := int(binary.BigEndian.Uint16(b[p+2:]))
		p += 4
		if p+size > end {
			return "", fail
		}
		if typ == 0 {
			data := b[p : p+size]
			if len(data) < 2 || int(binary.BigEndian.Uint16(data)) != len(data)-2 {
				return "", fail
			}
			for q := 2; q+3 <= len(data); {
				kind := data[q]
				length := int(binary.BigEndian.Uint16(data[q+1:]))
				q += 3
				if length == 0 || q+length > len(data) {
					return "", fail
				}
				if kind == 0 {
					name := CleanHost(string(data[q : q+length]))
					if len(name) > 253 {
						return "", fail
					}
					return name, nil
				}
				q += length
			}
		}
		p += size
	}
	return "", fail
}
func (r *Runtime) serveTLS() {
	for {
		c, e := r.tlsListener.Accept()
		if e != nil {
			return
		}
		if !r.admit(c) {
			continue
		}
		go r.tunnel(c)
	}
}
func (r *Runtime) tunnel(c net.Conn) {
	defer r.release(c)
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	host, wire, e := clientHello(c)
	if e != nil {
		r.log("TLS_ERROR", c.RemoteAddr().String(), "", e.Error(), "")
		return
	}
	ip, e := r.TLSUpstream(r.ctx, host)
	if e != nil {
		r.log("TLS_ERROR", c.RemoteAddr().String(), host, e.Error(), "")
		return
	}
	up, e := r.dialTLS(r.ctx, "tcp", net.JoinHostPort(ip, "443"))
	if e != nil {
		r.log("TLS_ERROR", c.RemoteAddr().String(), host, e.Error(), "")
		return
	}
	defer up.Close()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.connections[up] = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.connections, up); r.mu.Unlock() }()
	_ = up.SetDeadline(time.Now().Add(30 * time.Minute))
	_ = c.SetDeadline(time.Now().Add(30 * time.Minute))
	if _, e = up.Write(wire); e != nil {
		return
	}
	r.tlsConnections.Add(1)
	r.log("TLS", c.RemoteAddr().String(), host, "加密原样转发 → "+ip, "")
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(up, c)
		if tcp, ok := up.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(c, up)
	_ = c.Close()
	_ = up.Close()
	<-done
}
