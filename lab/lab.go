// Package lab is the Sebab failure lab: pure-Go servers on loopback that induce
// every Phase 1 cause code (NXDOMAIN, refused port, blackholed TEST-NET address,
// expired / wrong-host / self-signed certificates, 401, 502, 503, slow and
// hanging responses, unexpected status, redirect loops).
//
// Nothing here contacts the internet. The lab Dialer refuses every address that
// is not loopback, and simulates TEST-NET addresses instead of sending packets.
package lab

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Token is the canary secret the lab accepts for authenticated routes. Secret
// safety tests grep every output for it.
const Token = "lab-canary-secret-7f3a9c1e5b"

// Basic auth user accepted by /auth/basic.
const BasicUser = "lab"

// TLS server names.
const (
	TLSValid     = "valid"
	TLSExpired   = "expired"
	TLSNotYet    = "notyet"
	TLSWrongHost = "wronghost"
	TLSSelfSign  = "selfsigned"
	TLSSoon      = "soon"
)

// Lab is a running failure lab.
type Lab struct {
	Certs         *Certs
	DNS           *DNSServer
	Dialer        *Dialer
	HTTPPort      int
	TLSPorts      map[string]int
	RefusedPort   int
	HandshakePort int

	closers []func()
	mu      sync.Mutex
	closed  bool
}

// Options configure a lab.
type Options struct {
	Now     time.Time // certificate reference time; zero means time.Now()
	DNSAddr string    // "" picks a free loopback port
	Ports   []int     // optional fixed ports for: http, then each TLS server, refused, handshake
}

// Start launches every lab server on 127.0.0.1.
func Start(opts Options) (*Lab, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	certs, err := NewCerts(opts.Now)
	if err != nil {
		return nil, err
	}
	l := &Lab{Certs: certs, Dialer: &Dialer{}, TLSPorts: map[string]int{}}
	ports := append([]int(nil), opts.Ports...)
	next := func() string {
		if len(ports) == 0 {
			return "127.0.0.1:0"
		}
		p := ports[0]
		ports = ports[1:]
		return "127.0.0.1:" + strconv.Itoa(p)
	}
	if err := l.start(opts, next); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func (l *Lab) start(opts Options, next func() string) error {
	dns, err := StartDNS(opts.DNSAddr)
	if err != nil {
		return fmt.Errorf("lab dns: %w", err)
	}
	l.DNS = dns
	l.closers = append(l.closers, dns.Close)

	if l.HTTPPort, err = l.serve(next(), nil); err != nil {
		return err
	}
	for _, s := range []struct {
		name string
		cert tls.Certificate
	}{
		{TLSValid, l.Certs.Valid}, {TLSExpired, l.Certs.Expired}, {TLSNotYet, l.Certs.NotYet},
		{TLSWrongHost, l.Certs.Wrong}, {TLSSelfSign, l.Certs.SelfSig}, {TLSSoon, l.Certs.Soon},
	} {
		port, err := l.serve(next(), &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12})
		if err != nil {
			return err
		}
		l.TLSPorts[s.name] = port
	}
	if l.RefusedPort, err = refusedPort(next()); err != nil {
		return err
	}
	if l.HandshakePort, err = l.garbageServer(next()); err != nil {
		return err
	}
	return nil
}

func (l *Lab) serve(addr string, tlsCfg *tls.Config) (int, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("lab listen %s: %w", addr, err)
	}
	srv := &http.Server{Handler: Handler(), ReadHeaderTimeout: 5 * time.Second, ErrorLog: quietLog()}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	go func() { _ = srv.Serve(ln) }()
	l.closers = append(l.closers, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = srv.Close()
	})
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// garbageServer accepts TCP and answers with non-TLS bytes, so a TLS client
// fails the handshake (TLS_HANDSHAKE_FAILED).
func (l *Lab) garbageServer(addr string) (int, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("lab listen %s: %w", addr, err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("SSH-2.0-SebabLab\r\n"))
			_ = c.Close()
		}
	}()
	l.closers = append(l.closers, func() { ln.Close(); wg.Wait() })
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// refusedPort reserves a port and closes it, so connecting gets ECONNREFUSED.
func refusedPort(addr string) (int, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("lab reserve port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// Close stops every server.
func (l *Lab) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	for i := len(l.closers) - 1; i >= 0; i-- {
		l.closers[i]()
	}
}

// HTTPURL returns a plain-HTTP lab URL for host (a .lab.test name) and path.
func (l *Lab) HTTPURL(host, path string) string {
	return fmt.Sprintf("http://%s:%d%s", host, l.HTTPPort, path)
}

// TLSURL returns an HTTPS URL on the named TLS server.
func (l *Lab) TLSURL(server, host, path string) string {
	return fmt.Sprintf("https://%s:%d%s", host, l.TLSPorts[server], path)
}

// Dialer only dials loopback. TEST-NET-1 (192.0.2.0/24) blackholes until the
// context ends; TEST-NET-2 (198.51.100.0/24) is reported unreachable. Anything
// else is refused so tests can never reach a real host.
type Dialer struct {
	inner net.Dialer
	mu    sync.Mutex
	down  bool
}

var (
	testNet1 = &net.IPNet{IP: net.IPv4(192, 0, 2, 0), Mask: net.CIDRMask(24, 32)}
	testNet2 = &net.IPNet{IP: net.IPv4(198, 51, 100, 0), Mask: net.CIDRMask(24, 32)}
)

// ErrNotLoopback is returned for addresses the lab refuses to dial.
var ErrNotLoopback = errors.New("lab dialer: refusing non-loopback address")

// SetOffline makes every dial fail as if the network were down.
func (d *Dialer) SetOffline(down bool) {
	d.mu.Lock()
	d.down = down
	d.mu.Unlock()
}

// DialContext implements the probe Dialer interface.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	d.mu.Lock()
	down := d.down
	d.mu.Unlock()
	opErr := func(e error) error {
		return &net.OpError{Op: "dial", Net: network, Err: os.NewSyscallError("connect", e)}
	}
	switch {
	case ip == nil:
		return nil, fmt.Errorf("lab dialer: %q is not an IP address", host)
	case down:
		return nil, opErr(syscall.ENETUNREACH)
	case testNet1.Contains(ip):
		<-ctx.Done()
		return nil, &net.OpError{Op: "dial", Net: network, Err: ctx.Err()}
	case testNet2.Contains(ip):
		return nil, opErr(syscall.EHOSTUNREACH)
	case ip.IsLoopback():
		return d.inner.DialContext(ctx, network, address)
	default:
		return nil, ErrNotLoopback
	}
}
