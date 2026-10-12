package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yoockh/sebab/internal/diagnose"
	"github.com/yoockh/sebab/internal/platform"
)

// dnsResponse builds a minimal response header + question for name.
func dnsResponse(name string, rcode byte) []byte {
	msg := []byte{0x12, 0x34, 0x81, 0x80 | rcode, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range splitLabels(name) {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	return append(msg, 0, 0, 1, 0, 1)
}

func splitLabels(name string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			out = append(out, name[start:i])
			start = i + 1
		}
	}
	return out
}

func TestSniffUDPAndTCPFraming(t *testing.T) {
	rec := &dnsRecorder{}
	udp := &sniffConn{rec: rec}
	udp.observe(dnsResponse("a.example", rcodeNXDomain))
	if !rec.has("a.example", rcodeNXDomain) {
		t.Fatal("udp response not recorded")
	}
	tcp := &sniffConn{rec: rec, stream: true}
	msg := dnsResponse("B.Example.", rcodeServFail)
	framed := binary.BigEndian.AppendUint16(nil, uint16(len(msg)))
	framed = append(framed, msg...)
	// Deliver in awkward chunks like io.ReadFull would.
	tcp.observe(framed[:1])
	tcp.observe(framed[1:7])
	tcp.observe(framed[7:])
	if !rec.has("b.example", rcodeServFail) {
		t.Fatal("tcp framed response not recorded")
	}
	query := dnsResponse("c.example", 0)
	query[2] &^= 0x80 // a query, not a response
	udp.observe(query)
	if rec.has("c.example", 0) {
		t.Fatal("queries must be ignored")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"120": 2 * time.Minute, "": 0, "soon": 0,
		"Sun, 11 Oct 2026 12:05:00 GMT": 5 * time.Minute,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestClassifyDial(t *testing.T) {
	op := func(e error) error { return &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", e)} }
	cases := []struct {
		err  error
		want diagnose.Code
	}{
		{op(syscall.ECONNREFUSED), diagnose.TCPRefused},
		{op(syscall.EHOSTUNREACH), diagnose.TCPUnreachable},
		{op(syscall.ENETUNREACH), diagnose.TCPUnreachable},
		{context.DeadlineExceeded, diagnose.TCPTimeout},
		{errors.New("weird"), diagnose.TCPUnreachable},
	}
	for _, tc := range cases {
		if got, _ := classifyDial(tc.err); got != tc.want {
			t.Errorf("%v: got %s want %s", tc.err, got, tc.want)
		}
	}
}

type countingDialer struct {
	calls atomic.Int32
	err   error
}

func (d *countingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	d.calls.Add(1)
	if d.err != nil {
		return nil, d.err
	}
	c1, c2 := net.Pipe()
	c2.Close()
	return c1, nil
}

func TestCanaryCachesAndDisables(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := &countingDialer{}
	p := &Prober{Dialer: d, Resolver: &Resolver{}, Now: func() time.Time { return now }}
	c := &CanaryChecker{Prober: p, Targets: []string{"tcp://192.0.2.1:443", "tcp://192.0.2.2:443"}, CacheFor: 10 * time.Second}
	v := c.Check(context.Background(), false)
	if !v.Checked || !v.OK {
		t.Fatalf("verdict %+v", v)
	}
	calls := d.calls.Load()
	c.Check(context.Background(), false)
	if d.calls.Load() != calls {
		t.Fatal("second check within CacheFor should reuse the verdict")
	}
	now = now.Add(11 * time.Second)
	c.Check(context.Background(), false)
	if d.calls.Load() == calls {
		t.Fatal("expired cache should re-run")
	}
	d.err = &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}
	c.Invalidate()
	if v := c.Check(context.Background(), false); v.OK {
		t.Fatal("all canaries failing should report not OK")
	}
	off := &CanaryChecker{Prober: p}
	if v := off.Check(context.Background(), false); !v.Disabled || v.Checked {
		t.Fatalf("no targets should mean disabled: %+v", v)
	}
}

func TestLocalSkipsRouteForLoopbackAndVPN(t *testing.T) {
	noRoute := platform.Online()
	noRoute.DefaultRoute = false
	p := &Prober{Net: noRoute, Now: time.Now}
	r := &run{p: p, spec: Spec{Host: "127.0.0.1"}}
	if f := r.local(); f != nil {
		t.Errorf("loopback target should not need a default route: %v", f)
	}
	r = &run{p: p, spec: Spec{Host: "db.example", RequiresVPN: "wg0"}}
	if f := r.local(); f != nil {
		t.Errorf("VPN-bound monitor should not need a default route: %v", f)
	}
	r = &run{p: p, spec: Spec{Host: "db.example"}}
	if f := r.local(); f == nil || f.Code != diagnose.LocalNoRoute {
		t.Errorf("want LOCAL_NO_ROUTE, got %v", f)
	}
}

func TestStepBudget(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{10 * time.Second: 5 * time.Second,
		30 * time.Second: 5 * time.Second, 2 * time.Second: time.Second, 100 * time.Millisecond: 500 * time.Millisecond} {
		if got := stepBudget(in); got != want {
			t.Errorf("stepBudget(%v)=%v want %v", in, got, want)
		}
	}
}
