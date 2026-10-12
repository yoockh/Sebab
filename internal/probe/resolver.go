package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"

	"github.com/yoockh/sebab/internal/diagnose"
)

// DNS response codes we care about.
const (
	rcodeNoError  = 0
	rcodeServFail = 2
	rcodeNXDomain = 3
	rcodeRefused  = 5
)

// Resolver resolves names through Go's DNS client (which reads /etc/hosts and
// /etc/resolv.conf, so systemd-resolved and VPN split DNS apply) while sniffing
// the raw responses to recover the RCODE that net.DNSError hides.
type Resolver struct {
	// Server, when set, sends every query to this address (failure lab).
	Server string
	// Dial dials the DNS server. Nil uses a plain net.Dialer.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// dnsRecorder collects the RCODEs seen per question name during one lookup.
type dnsRecorder struct {
	mu     sync.Mutex
	rcodes map[string][]int
	any    bool
}

func (r *dnsRecorder) record(name string, rcode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rcodes == nil {
		r.rcodes = map[string][]int{}
	}
	r.rcodes[name] = append(r.rcodes[name], rcode)
	r.any = true
}

func (r *dnsRecorder) has(name string, rcode int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.rcodes[name] {
		if c == rcode {
			return true
		}
	}
	return false
}

func (r *dnsRecorder) sawAny() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.any
}

// Lookup resolves host to IP addresses. On failure it returns a *Failure with a
// DNS cause code.
func (r *Resolver) Lookup(ctx context.Context, host string) ([]net.IPAddr, error) {
	rec := &dnsRecorder{}
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		if r.Server != "" {
			address = r.Server
		}
		dial := r.Dial
		if dial == nil {
			var d net.Dialer
			dial = d.DialContext
		}
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		// Go's resolver picks UDP or TCP framing by checking for net.PacketConn,
		// so the wrapper must keep that interface when the inner conn has it.
		if pc, ok := conn.(net.PacketConn); ok {
			return &sniffPacketConn{sniffConn: &sniffConn{Conn: conn, rec: rec}, pc: pc}, nil
		}
		return &sniffConn{Conn: conn, stream: true, rec: rec}, nil
	}}
	addrs, err := res.LookupIPAddr(ctx, host)
	if err == nil && len(addrs) == 0 {
		err = &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	if err != nil {
		return nil, classifyDNS(ctx, err, rec, host)
	}
	return addrs, nil
}

func classifyDNS(ctx context.Context, err error, rec *dnsRecorder, host string) *Failure {
	name := normName(host)
	fail := func(code diagnose.Code, detail string) *Failure {
		return &Failure{Code: code, Facts: diagnose.Facts{Host: host, Detail: detail}}
	}
	var de *net.DNSError
	isDNS := errors.As(err, &de)
	switch {
	case rec.has(name, rcodeNXDomain):
		return fail(diagnose.DNSNXDomain, "NXDOMAIN")
	case rec.has(name, rcodeServFail) || rec.has(name, rcodeRefused):
		return fail(diagnose.DNSServFail, "SERVFAIL")
	case isDNS && de.IsNotFound && rec.has(name, rcodeNoError):
		return fail(diagnose.DNSNoAddress, "no A/AAAA records")
	case isDNS && de.IsTimeout, errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil:
		return fail(diagnose.DNSTimeout, "timed out")
	case !rec.sawAny():
		if isDNS && de.IsNotFound {
			// Answered locally (for example /etc/hosts or nsswitch) without a DNS query.
			return fail(diagnose.DNSNXDomain, "not found")
		}
		return fail(diagnose.DNSTimeout, "no answer from the resolver")
	default:
		return fail(diagnose.DNSServFail, "resolver error")
	}
}

func normName(s string) string { return strings.ToLower(strings.TrimSuffix(s, ".")) }

// sniffConn passes DNS traffic through unchanged and records each response.
type sniffConn struct {
	net.Conn
	stream bool
	buf    []byte
	rec    *dnsRecorder
}

func (c *sniffConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.observe(p[:n])
	}
	return n, err
}

func (c *sniffConn) observe(b []byte) {
	if !c.stream {
		c.parse(b)
		return
	}
	c.buf = append(c.buf, b...)
	for len(c.buf) >= 2 {
		size := int(binary.BigEndian.Uint16(c.buf))
		if len(c.buf) < 2+size {
			return
		}
		c.parse(c.buf[2 : 2+size])
		c.buf = c.buf[2+size:]
	}
}

// sniffPacketConn is a sniffConn that still satisfies net.PacketConn.
type sniffPacketConn struct {
	*sniffConn
	pc net.PacketConn
}

func (c *sniffPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.pc.ReadFrom(p)
	if n > 0 {
		c.observe(p[:n])
	}
	return n, addr, err
}

func (c *sniffPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) { return c.pc.WriteTo(p, addr) }

// parse reads the header and the first question name of a DNS response.
func (c *sniffConn) parse(msg []byte) {
	if len(msg) < 12 || msg[2]&0x80 == 0 { // too short or not a response
		return
	}
	rcode := int(msg[3] & 0x0f)
	if binary.BigEndian.Uint16(msg[4:6]) == 0 {
		return
	}
	name, ok := readName(msg, 12)
	if !ok {
		return
	}
	c.rec.record(normName(name), rcode)
}

// readName decodes an uncompressed question name.
func readName(msg []byte, off int) (string, bool) {
	var labels []string
	for steps := 0; off < len(msg) && steps < 128; steps++ {
		l := int(msg[off])
		if l == 0 {
			return strings.Join(labels, "."), true
		}
		if l&0xc0 != 0 || off+1+l > len(msg) {
			return "", false
		}
		labels = append(labels, string(msg[off+1:off+1+l]))
		off += 1 + l
	}
	return "", false
}
