// Package probe runs the layered checks (local, VPN, DNS, TCP, TLS, HTTP) for one
// endpoint, stops at the first failure and records per-step timings. Clocks,
// resolvers, dialers and network state are injected so every cause code can be
// reproduced in tests without touching real third-party services.
package probe

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/diagnose"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/platform"
)

// Failure is a classified probe failure. It implements error so it can travel
// through net/http's error wrapping.
type Failure struct {
	Code  diagnose.Code
	Facts diagnose.Facts
}

func (f *Failure) Error() string { return string(f.Code) }

// Dialer opens TCP connections. *net.Dialer satisfies it.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Credentials are resolved auth values. Secret is never logged.
type Credentials struct {
	Type     string // bearer, header, basic
	Header   string
	Username string
	Secret   string
}

// Spec is everything needed to check one endpoint.
type Spec struct {
	Kind                 string   // "http" or "tcp"
	URL                  *url.URL // http only
	Host                 string
	Port                 int
	Timeout              time.Duration
	ExpectStatus         []int
	SlowThreshold        time.Duration
	TLSWarnDays          int
	FollowRedirects      bool
	AllowedRedirectHosts []string
	Method               string
	Headers              map[string]string
	Auth                 *Credentials
	RequiresVPN          string
	InsecureTLS          bool
	RootCAs              *x509.CertPool // nil means the system pool
	Assertions           []config.Assertion
}

// Result is the outcome of one check.
type Result struct {
	Start       time.Time
	Passed      []event.Layer
	Timings     event.Timings
	Outcome     *diagnose.Outcome // nil when healthy
	HTTPStatus  int
	Address     string
	Family      string
	TLSNotAfter time.Time
	Canceled    bool // the caller's context ended; discard this result
}

// Status returns the raw status implied by the outcome (before canary logic).
func (r Result) Status() event.Status {
	if r.Outcome == nil {
		return event.StatusUp
	}
	return diagnose.Taxonomy[r.Outcome.Code].Status
}

// Failed reports whether the check failed (as opposed to up or degraded).
func (r Result) Failed() bool {
	s := r.Status()
	return s != event.StatusUp && s != event.StatusDegraded
}

// Prober runs checks. The zero value is not usable; use New.
type Prober struct {
	Net       platform.Network // nil skips the local step
	Resolver  *Resolver
	Dialer    Dialer
	Now       func() time.Time
	UserAgent string
	// Proxy picks an HTTP proxy for a request. Nil means http.ProxyFromEnvironment.
	Proxy func(*http.Request) (*url.URL, error)
	// NoProxy disables proxies entirely (tests).
	NoProxy bool
}

// New returns a Prober wired to the real system.
func New(network platform.Network, userAgent string) *Prober {
	return &Prober{Net: network, Resolver: &Resolver{}, Dialer: &net.Dialer{}, Now: time.Now, UserAgent: userAgent}
}

// Run executes the layered check described by spec.
func (p *Prober) Run(parent context.Context, spec Spec) Result {
	if spec.Timeout <= 0 {
		spec.Timeout = 10 * time.Second
	}
	t := &run{p: p, spec: spec, res: Result{Start: p.Now()}, parent: parent}
	ctx, cancel := context.WithTimeout(parent, spec.Timeout)
	defer cancel()
	t.execute(ctx)
	if t.parentDone(ctx) {
		t.res.Canceled = true
	}
	return t.res
}

type run struct {
	p        *Prober
	spec     Spec
	res      Result
	deadline time.Time
	parent   context.Context
}

func (t *run) parentDone(ctx context.Context) bool {
	return t.parent != nil && t.parent.Err() != nil
}

func (t *run) execute(ctx context.Context) {
	t.deadline, _ = ctx.Deadline()
	if f := t.local(); f != nil {
		t.fail(f)
		return
	}
	if f := t.vpn(); f != nil {
		t.fail(f)
		return
	}
	if t.spec.Kind == "http" {
		t.httpCheck(ctx)
	} else {
		t.tcpCheck(ctx)
	}
	t.res.Timings.Total = event.MS(t.p.Now().Sub(t.res.Start))
}

func (t *run) fail(f *Failure) {
	if f.Facts.Now.IsZero() {
		f.Facts.Now = t.p.Now()
	}
	t.res.Outcome = &diagnose.Outcome{Code: f.Code, Facts: f.Facts}
}

func (t *run) pass(l event.Layer) { t.res.Passed = append(t.res.Passed, l) }

// local checks link and default route. Loopback targets and VPN-bound monitors
// skip the route check: they do not need a default route to work.
func (t *run) local() *Failure {
	if t.p.Net == nil {
		return nil
	}
	links, err := t.p.Net.Links()
	if err != nil {
		return nil // cannot tell; let the network steps decide
	}
	if isLoopbackHost(t.spec.Host) {
		t.pass(event.LayerLocal)
		return nil
	}
	if !platform.HasActiveLink(links) {
		return &Failure{Code: diagnose.LocalNoLink}
	}
	if t.spec.RequiresVPN == "" {
		if ok, err := t.p.Net.HasDefaultRoute(); err == nil && !ok {
			return &Failure{Code: diagnose.LocalNoRoute}
		}
	}
	t.pass(event.LayerLocal)
	return nil
}

func (t *run) vpn() *Failure {
	if t.spec.RequiresVPN == "" || t.p.Net == nil {
		return nil
	}
	links, err := t.p.Net.Links()
	if err != nil {
		return nil
	}
	l, ok := platform.FindLink(links, t.spec.RequiresVPN)
	if !ok || !l.Up || !l.Running {
		return &Failure{Code: diagnose.VPNDown, Facts: diagnose.Facts{Interface: t.spec.RequiresVPN, Missing: !ok}}
	}
	t.pass(event.LayerVPN)
	return nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (t *run) tcpCheck(ctx context.Context) {
	conn, f := t.connect(ctx, t.spec.Host, t.spec.Port, true)
	if f != nil {
		t.fail(f)
		return
	}
	conn.Close()
}

// connect resolves host and dials it, recording DNS/TCP timings and layers when
// record is true (the first connection of a check).
func (t *run) connect(ctx context.Context, host string, port int, record bool) (net.Conn, *Failure) {
	addrs, f := t.resolve(ctx, host, record)
	if f != nil {
		return nil, f
	}
	start := t.p.Now()
	conn, family, f := t.dial(ctx, host, addrs, port)
	if f != nil {
		f.Facts.Elapsed = t.p.Now().Sub(start)
		return nil, f
	}
	if record {
		t.res.Timings.TCP = event.MS(t.p.Now().Sub(start))
		t.res.Address = conn.RemoteAddr().String()
		t.res.Family = family
		t.pass(event.LayerTCP)
	}
	return conn, nil
}

func (t *run) resolve(ctx context.Context, host string, record bool) ([]net.IPAddr, *Failure) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IPAddr{{IP: ip}}, nil // literal address: no DNS step
	}
	start := t.p.Now()
	dctx, cancel := context.WithTimeout(ctx, stepBudget(t.spec.Timeout))
	defer cancel()
	addrs, err := t.p.Resolver.Lookup(dctx, host)
	if err != nil {
		var f *Failure
		if errors.As(err, &f) {
			return nil, f
		}
		return nil, &Failure{Code: diagnose.DNSServFail, Facts: diagnose.Facts{Host: host, Detail: err.Error()}}
	}
	if record {
		t.res.Timings.DNS = event.MS(t.p.Now().Sub(start))
		t.pass(event.LayerDNS)
	}
	return addrs, nil
}

// stepBudget is the share of the check timeout a DNS lookup or TCP connect may use.
func stepBudget(total time.Duration) time.Duration {
	b := total / 2
	if b > 5*time.Second {
		b = 5 * time.Second
	}
	if b < 500*time.Millisecond {
		b = 500 * time.Millisecond
	}
	return b
}

// dial tries the first IPv4 address, then the first IPv6 address. A working
// path in either family counts as up; failures report every family tried.
func (t *run) dial(ctx context.Context, host string, addrs []net.IPAddr, port int) (net.Conn, string, *Failure) {
	var v4, v6 *net.IPAddr
	for i := range addrs {
		a := &addrs[i]
		if a.IP.To4() != nil && v4 == nil {
			v4 = a
		} else if a.IP.To4() == nil && v6 == nil {
			v6 = a
		}
	}
	type attempt struct {
		family string
		addr   *net.IPAddr
	}
	var tries []attempt
	if v4 != nil {
		tries = append(tries, attempt{"ipv4", v4})
	}
	if v6 != nil {
		tries = append(tries, attempt{"ipv6", v6})
	}
	families := map[string]string{}
	var first *Failure
	for i, a := range tries {
		budget := stepBudget(t.spec.Timeout) / time.Duration(len(tries)-i)
		if i > 0 {
			budget = time.Until(t.deadline) / 2
		}
		dctx, cancel := context.WithTimeout(ctx, budget)
		conn, err := t.p.Dialer.DialContext(dctx, "tcp", net.JoinHostPort(a.addr.String(), strconv.Itoa(port)))
		cancel()
		if err == nil {
			return conn, a.family, nil
		}
		code, word := classifyDial(err)
		families[a.family] = word
		if first == nil {
			first = &Failure{Code: code, Facts: diagnose.Facts{Host: host, Port: port, Detail: word}}
		}
		if ctx.Err() != nil {
			break
		}
	}
	if first == nil {
		first = &Failure{Code: diagnose.DNSNoAddress, Facts: diagnose.Facts{Host: host}}
	}
	if len(families) > 1 {
		first.Facts.Families = families
	}
	return nil, "", first
}

func classifyDial(err error) (diagnose.Code, string) {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return diagnose.TCPRefused, "refused"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH),
		errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, syscall.ENETDOWN):
		return diagnose.TCPUnreachable, "unreachable"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return diagnose.TCPTimeout, "timed out"
	default:
		return diagnose.TCPUnreachable, fmt.Sprintf("connect failed (%s)", shortErr(err))
	}
}

// shortErr strips address noise from low-level errors ("dial tcp 1.2.3.4:5: x" -> "x").
func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	return err.Error()
}
