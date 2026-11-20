package browser

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	socksVersion    = 5
	socksConnect    = 1
	socksIPv4       = 1
	socksDomain     = 3
	socksIPv6       = 4
	socksOK         = 0
	socksNotAllowed = 2
	socksRefused    = 5
	socksBadCommand = 7
	socksBadAddress = 8
	dialWait        = 10 * time.Second
)

type gate struct {
	ln      net.Listener
	allowed map[string]bool

	mu     sync.Mutex
	conns  map[net.Conn]bool
	closed bool
	wg     sync.WaitGroup
}

func hostPorts(origins map[string]bool) map[string]bool {
	out := map[string]bool{}
	for o := range origins {
		u, err := url.Parse(o)
		if err != nil {
			continue
		}
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		out[net.JoinHostPort(strings.ToLower(u.Hostname()), port)] = true
	}
	return out
}

func openGate(origins map[string]bool) (*gate, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g := &gate{ln: ln, allowed: hostPorts(origins), conns: map[net.Conn]bool{}}
	g.wg.Go(g.serve)
	return g, nil
}

func (g *gate) addr() string {
	return g.ln.Addr().String()
}

func (g *gate) track(c net.Conn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.conns[c] = true
	return true
}

func (g *gate) untrack(c net.Conn) {
	g.mu.Lock()
	delete(g.conns, c)
	g.mu.Unlock()
	_ = c.Close()
}

func (g *gate) serve() {
	for {
		c, err := g.ln.Accept()
		if err != nil {
			return
		}
		if !g.track(c) {
			_ = c.Close()
			return
		}
		g.wg.Go(func() {
			defer g.untrack(c)
			g.handle(c)
		})
	}
}

func reply(c net.Conn, code byte) {
	_, _ = c.Write([]byte{socksVersion, code, 0, socksIPv4, 0, 0, 0, 0, 0, 0})
}

func (g *gate) handle(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(dialWait))
	br := bufio.NewReader(c)
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != socksVersion {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	noAuth := false
	for _, m := range methods {
		noAuth = noAuth || m == 0
	}
	if !noAuth {
		_, _ = c.Write([]byte{socksVersion, 0xff})
		return
	}
	if _, err := c.Write([]byte{socksVersion, 0}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[0] != socksVersion {
		return
	}
	if req[1] != socksConnect {
		reply(c, socksBadCommand)
		return
	}
	var host string
	switch req[3] {
	case socksIPv4, socksIPv6:
		n := net.IPv4len
		if req[3] == socksIPv6 {
			n = net.IPv6len
		}
		ip := make([]byte, n)
		if _, err := io.ReadFull(br, ip); err != nil {
			return
		}
		host = net.IP(ip).String()
	case socksDomain:
		n, err := br.ReadByte()
		if err != nil {
			return
		}
		name := make([]byte, n)
		if _, err := io.ReadFull(br, name); err != nil {
			return
		}
		host = string(name)
	default:
		reply(c, socksBadAddress)
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return
	}
	target := net.JoinHostPort(strings.ToLower(strings.Trim(host, "[]")), strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	if !g.allowed[target] {
		reply(c, socksNotAllowed)
		return
	}
	dst, err := net.DialTimeout("tcp", target, dialWait)
	if err != nil {
		reply(c, socksRefused)
		return
	}
	if !g.track(dst) {
		_ = dst.Close()
		return
	}
	defer g.untrack(dst)
	reply(c, socksOK)
	_ = c.SetDeadline(time.Time{})
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(dst, br)
		_ = dst.Close()
		_ = c.Close()
		close(done)
	}()
	_, _ = io.Copy(c, dst)
	_ = dst.Close()
	_ = c.Close()
	<-done
}

func (g *gate) close() {
	g.mu.Lock()
	g.closed = true
	conns := make([]net.Conn, 0, len(g.conns))
	for c := range g.conns {
		conns = append(conns, c)
	}
	g.mu.Unlock()
	_ = g.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	g.wg.Wait()
}
