package lab_test

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/diagnose"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/platform"
	"github.com/yoockh/sebab/internal/probe"
	"github.com/yoockh/sebab/lab"
)

func startLab(t *testing.T) *lab.Lab {
	t.Helper()
	l, err := lab.Start(lab.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l
}

type env struct {
	lab    *lab.Lab
	prober *probe.Prober
	canary *probe.CanaryChecker
}

func newEnv(t *testing.T, l *lab.Lab, net platform.Network) *env {
	p := &probe.Prober{Net: net, Resolver: &probe.Resolver{Server: l.DNS.Addr}, Dialer: l.Dialer,
		Now: time.Now, UserAgent: "sebab-test", NoProxy: true}
	c := &probe.CanaryChecker{Prober: p, Targets: []string{"tcp://127.0.0.1:" + strconv.Itoa(l.HTTPPort)},
		DNSNames: []string{"canary.lab.test"}, Timeout: time.Second}
	return &env{lab: l, prober: p, canary: c}
}

func httpSpec(t *testing.T, raw string, l *lab.Lab) probe.Spec {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	return probe.Spec{Kind: "http", URL: u, Host: u.Hostname(), Port: port, Timeout: 2 * time.Second,
		SlowThreshold: 1500 * time.Millisecond, TLSWarnDays: 14, FollowRedirects: true, RootCAs: l.Certs.Pool}
}

func tcpSpec(host string, port int) probe.Spec {
	return probe.Spec{Kind: "tcp", Host: host, Port: port, Timeout: 2 * time.Second}
}

// check runs a probe plus the canary decision, like the engine does.
func (e *env) check(t *testing.T, spec probe.Spec) (probe.Result, diagnose.Diagnosis) {
	t.Helper()
	ctx := context.Background()
	res := e.prober.Run(ctx, spec)
	if res.Outcome == nil {
		return res, diagnose.Diagnosis{Status: event.StatusUp}
	}
	canary := diagnose.Canary{}
	if res.Failed() && !diagnose.IsLocal(res.Outcome.Code) && res.Outcome.Code != diagnose.VPNDown {
		canary = e.canary.Check(ctx, diagnose.LayerOf(res.Outcome.Code) == event.LayerDNS)
	}
	return res, diagnose.Diagnose(*res.Outcome, res.Passed, canary)
}

type want struct {
	code   diagnose.Code
	layer  event.Layer
	side   event.Side
	status event.Status
}

func expect(t *testing.T, d diagnose.Diagnosis, w want) {
	t.Helper()
	if d.Code != w.code || d.Layer != w.layer || d.Side != w.side || d.Status != w.status {
		t.Fatalf("got %s/%s/%s/%s want %s/%s/%s/%s (summary %q)", d.Code, d.Layer, d.Side, d.Status,
			w.code, w.layer, w.side, w.status, d.Summary)
	}
	if d.Summary == "" || d.Hint == "" {
		t.Fatalf("empty summary or hint: %+v", d)
	}
}

func TestLabLocalNoLink(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.StaticNetwork{LinkList: []platform.Link{{Name: "lo", Up: true, Running: true, Loopback: true},
		{Name: "wlan0", Up: true}}, DefaultRoute: false})
	_, d := e.check(t, httpSpec(t, l.HTTPURL("app.lab.test", "/ok"), l))
	expect(t, d, want{diagnose.LocalNoLink, event.LayerLocal, event.SideYours, event.StatusUnknown})
}

func TestLabLocalNoRoute(t *testing.T) {
	l := startLab(t)
	net := platform.Online()
	net.DefaultRoute = false
	e := newEnv(t, l, net)
	_, d := e.check(t, httpSpec(t, l.HTTPURL("app.lab.test", "/ok"), l))
	expect(t, d, want{diagnose.LocalNoRoute, event.LayerLocal, event.SideYours, event.StatusUnknown})
}

func TestLabLocalOffline(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	l.Dialer.SetOffline(true)
	_, d := e.check(t, tcpSpec("192.0.2.10", 443))
	expect(t, d, want{diagnose.LocalOffline, event.LayerLocal, event.SideYours, event.StatusUnknown})
}

func TestLabLocalDNSBroken(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	l.DNS.SetBroken(true)
	_, d := e.check(t, httpSpec(t, l.HTTPURL("app.lab.test", "/ok"), l))
	expect(t, d, want{diagnose.LocalDNSBroken, event.LayerLocal, event.SideYours, event.StatusUnknown})
}

func TestLabVPNDown(t *testing.T) {
	l := startLab(t)
	for _, tc := range []struct {
		name  string
		links []platform.Link
	}{
		{"missing", nil},
		{"down", []platform.Link{{Name: "wg0", Up: true, Running: false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, l, platform.Online(tc.links...))
			spec := httpSpec(t, l.HTTPURL("app.lab.test", "/ok"), l)
			spec.RequiresVPN = "wg0"
			_, d := e.check(t, spec)
			expect(t, d, want{diagnose.VPNDown, event.LayerVPN, event.SideYours, event.StatusPaused})
		})
	}
	e := newEnv(t, l, platform.Online(platform.Link{Name: "wg0", Up: true, Running: true}))
	spec := httpSpec(t, l.HTTPURL("app.lab.test", "/ok"), l)
	spec.RequiresVPN = "wg0"
	if _, d := e.check(t, spec); d.Status != event.StatusUp {
		t.Fatalf("vpn up should pass: %+v", d)
	}
}

func TestLabDNSNXDomain(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	_, d := e.check(t, httpSpec(t, l.HTTPURL(lab.NameNXDomain, "/ok"), l))
	expect(t, d, want{diagnose.DNSNXDomain, event.LayerDNS, event.SideTheirs, event.StatusDown})
}

func TestLabDNSServFail(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	_, d := e.check(t, httpSpec(t, l.HTTPURL(lab.NameServFail, "/ok"), l))
	expect(t, d, want{diagnose.DNSServFail, event.LayerDNS, event.SideTheirs, event.StatusDown})
}

func TestLabDNSTimeout(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	_, d := e.check(t, httpSpec(t, l.HTTPURL(lab.NameTimeout, "/ok"), l))
	expect(t, d, want{diagnose.DNSTimeout, event.LayerDNS, event.SideTheirs, event.StatusDown})
}

func TestLabDNSNoAddress(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	_, d := e.check(t, httpSpec(t, l.HTTPURL(lab.NameNoAddress, "/ok"), l))
	expect(t, d, want{diagnose.DNSNoAddress, event.LayerDNS, event.SideTheirs, event.StatusDown})
}

func TestLabTCPRefused(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	res, d := e.check(t, tcpSpec("db.lab.test", l.RefusedPort))
	expect(t, d, want{diagnose.TCPRefused, event.LayerTCP, event.SideTheirs, event.StatusDown})
	if res.Timings.DNS == nil || res.Timings.TCP != nil {
		t.Errorf("timings: dns should be set, tcp nil: %+v", res.Timings)
	}
	if !strings.Contains(d.Summary, strconv.Itoa(l.RefusedPort)) || !strings.Contains(d.Hint, "DNS is fine") {
		t.Errorf("text %q / %q", d.Summary, d.Hint)
	}
}

func TestLabTCPTimeoutBlackhole(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	_, d := e.check(t, tcpSpec(lab.NameBlackhole, 5432))
	expect(t, d, want{diagnose.TCPTimeout, event.LayerTCP, event.SideTheirs, event.StatusDown})
}

func TestLabTCPUnreachable(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	_, d := e.check(t, tcpSpec(lab.NameUnreach, 443))
	expect(t, d, want{diagnose.TCPUnreachable, event.LayerTCP, event.SideUnknown, event.StatusDown})
}

func TestLabDualStackReportsFamilies(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	res, _ := e.check(t, tcpSpec(lab.NameDualStack, l.HTTPPort))
	if res.Outcome != nil || res.Family != "ipv4" {
		t.Fatalf("working IPv4 path should count as up: %+v", res)
	}
	_, d := e.check(t, tcpSpec(lab.NameDualStack, l.RefusedPort))
	if d.Code != diagnose.TCPRefused || !strings.Contains(d.Summary, "IPv4 refused") || !strings.Contains(d.Summary, "IPv6") {
		t.Fatalf("summary should name both families: %q", d.Summary)
	}
}

func TestLabTLSCodes(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	cases := []struct {
		server string
		code   diagnose.Code
		side   event.Side
		status event.Status
	}{
		{lab.TLSExpired, diagnose.TLSExpired, event.SideTheirs, event.StatusDown},
		{lab.TLSNotYet, diagnose.TLSNotYetValid, event.SideUnknown, event.StatusDown},
		{lab.TLSWrongHost, diagnose.TLSHostMismatch, event.SideTheirs, event.StatusDown},
		{lab.TLSSelfSign, diagnose.TLSUntrusted, event.SideTheirs, event.StatusDown},
		{lab.TLSSoon, diagnose.TLSExpiringSoon, event.SideTheirs, event.StatusDegraded},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			res, d := e.check(t, httpSpec(t, l.TLSURL(tc.server, "app.lab.test", "/ok"), l))
			expect(t, d, want{tc.code, event.LayerTLS, tc.side, tc.status})
			if tc.status == event.StatusDown && (res.Timings.DNS == nil || res.Timings.TCP == nil || res.Timings.TLS != nil) {
				t.Errorf("timings %+v", res.Timings)
			}
			if tc.status == event.StatusDown && !strings.Contains(d.Hint, "DNS and TCP are fine") {
				t.Errorf("hint %q", d.Hint)
			}
		})
	}
	t.Run("self-signed wording", func(t *testing.T) {
		_, d := e.check(t, httpSpec(t, l.TLSURL(lab.TLSSelfSign, "app.lab.test", "/ok"), l))
		if !strings.Contains(d.Summary, "self-signed") {
			t.Errorf("summary %q", d.Summary)
		}
	})
	t.Run("valid", func(t *testing.T) {
		res, d := e.check(t, httpSpec(t, l.TLSURL(lab.TLSValid, "app.lab.test", "/ok"), l))
		if d.Status != event.StatusUp || res.Timings.TLS == nil || res.Timings.TTFB == nil || res.TLSNotAfter.IsZero() {
			t.Fatalf("valid TLS should be up with timings: %+v %+v", d, res)
		}
	})
	t.Run("insecure flag accepts self-signed", func(t *testing.T) {
		spec := httpSpec(t, l.TLSURL(lab.TLSSelfSign, "app.lab.test", "/ok"), l)
		spec.InsecureTLS = true
		if _, d := e.check(t, spec); d.Status != event.StatusUp {
			t.Fatalf("insecure_tls should pass: %+v", d)
		}
	})
	t.Run("untrusted without lab CA", func(t *testing.T) {
		spec := httpSpec(t, l.TLSURL(lab.TLSValid, "app.lab.test", "/ok"), l)
		spec.RootCAs = nil
		_, d := e.check(t, spec)
		if d.Code != diagnose.TLSUntrusted {
			t.Fatalf("system roots should not trust the lab CA: %s", d.Code)
		}
	})
}

func TestLabTLSHandshakeFailed(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	_, d := e.check(t, httpSpec(t, fmt.Sprintf("https://app.lab.test:%d/", l.HandshakePort), l))
	expect(t, d, want{diagnose.TLSHandshake, event.LayerTLS, event.SideTheirs, event.StatusDown})
}

func TestLabHTTPCodes(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	cases := []struct {
		path   string
		code   diagnose.Code
		side   event.Side
		status event.Status
		mutate func(*probe.Spec)
	}{
		{"/status/502", diagnose.HTTPBadGateway, event.SideTheirs, event.StatusDown, nil},
		{"/status/504", diagnose.HTTPBadGateway, event.SideTheirs, event.StatusDown, nil},
		{"/status/503", diagnose.HTTPUnavailable, event.SideTheirs, event.StatusDown, nil},
		{"/status/500", diagnose.HTTPServerError, event.SideTheirs, event.StatusDown, nil},
		{"/status/401", diagnose.HTTPAuth, event.SideYours, event.StatusDown, nil},
		{"/status/403", diagnose.HTTPAuth, event.SideYours, event.StatusDown, nil},
		{"/auth/bearer", diagnose.HTTPAuth, event.SideYours, event.StatusDown, nil},
		{"/status/404", diagnose.HTTPClientError, event.SideTheirs, event.StatusDown, nil},
		{"/status/429", diagnose.HTTPRateLimited, event.SideTheirs, event.StatusDegraded, nil},
		{"/hang", diagnose.HTTPTimeout, event.SideTheirs, event.StatusDown, nil},
		{"/slow?ms=400", diagnose.HTTPSlow, event.SideTheirs, event.StatusDegraded,
			func(s *probe.Spec) { s.SlowThreshold = 100 * time.Millisecond }},
		{"/ok", diagnose.HTTPUnexpStatus, event.SideTheirs, event.StatusDown,
			func(s *probe.Spec) { s.ExpectStatus = []int{204} }},
		{"/away", diagnose.HTTPUnexpRedir, event.SideTheirs, event.StatusDown, nil},
		{"/loop", diagnose.HTTPRedirLoop, event.SideTheirs, event.StatusDown, nil},
		{"/closed", diagnose.HTTPServerError, event.SideTheirs, event.StatusDown, nil},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			spec := httpSpec(t, l.HTTPURL("app.lab.test", tc.path), l)
			if tc.mutate != nil {
				tc.mutate(&spec)
			}
			res, d := e.check(t, spec)
			expect(t, d, want{tc.code, event.LayerHTTP, tc.side, tc.status})
			if tc.code != diagnose.HTTPTimeout && tc.code != diagnose.HTTPServerError &&
				tc.code != diagnose.HTTPRedirLoop && tc.code != diagnose.HTTPUnexpRedir && res.Timings.TTFB == nil {
				t.Errorf("ttfb missing: %+v", res.Timings)
			}
		})
	}
	t.Run("retry-after", func(t *testing.T) {
		res, _ := e.check(t, httpSpec(t, l.HTTPURL("app.lab.test", "/status/429"), l))
		if res.Outcome.Facts.RetryAfter != 120*time.Second {
			t.Errorf("retry after %v", res.Outcome.Facts.RetryAfter)
		}
	})
	t.Run("redirect same host is fine", func(t *testing.T) {
		if _, d := e.check(t, httpSpec(t, l.HTTPURL("app.lab.test", "/redirect"), l)); d.Status != event.StatusUp {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("allowed redirect host", func(t *testing.T) {
		spec := httpSpec(t, l.HTTPURL("app.lab.test", "/away"), l)
		spec.AllowedRedirectHosts = []string{"elsewhere.lab.test"}
		if _, d := e.check(t, spec); d.Status != event.StatusUp {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("no follow keeps 3xx", func(t *testing.T) {
		spec := httpSpec(t, l.HTTPURL("app.lab.test", "/redirect"), l)
		spec.FollowRedirects = false
		if res, d := e.check(t, spec); d.Status != event.StatusUp || res.HTTPStatus != 301 {
			t.Fatalf("got %+v %d", d, res.HTTPStatus)
		}
	})
	t.Run("https 502 says DNS TCP TLS fine", func(t *testing.T) {
		_, d := e.check(t, httpSpec(t, l.TLSURL(lab.TLSValid, "app.lab.test", "/status/502"), l))
		if d.Code != diagnose.HTTPBadGateway || !strings.Contains(d.Hint, "DNS, TCP and TLS are fine") {
			t.Fatalf("got %+v", d)
		}
	})
}

func TestLabAuthSuccess(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	for _, tc := range []struct {
		path string
		cred probe.Credentials
	}{
		{"/auth/bearer", probe.Credentials{Type: "bearer", Secret: lab.Token}},
		{"/auth/header", probe.Credentials{Type: "header", Header: "X-API-Key", Secret: lab.Token}},
		{"/auth/basic", probe.Credentials{Type: "basic", Username: lab.BasicUser, Secret: lab.Token}},
	} {
		t.Run(tc.cred.Type, func(t *testing.T) {
			spec := httpSpec(t, l.TLSURL(lab.TLSValid, "app.lab.test", tc.path), l)
			cred := tc.cred
			spec.Auth = &cred
			if _, d := e.check(t, spec); d.Status != event.StatusUp {
				t.Fatalf("auth should pass: %+v", d)
			}
			cred.Secret = "wrong"
			_, d := e.check(t, spec)
			expect(t, d, want{diagnose.HTTPAuth, event.LayerHTTP, event.SideYours, event.StatusDown})
		})
	}
}

func TestLabDialerRefusesRealHosts(t *testing.T) {
	d := &lab.Dialer{}
	if _, err := d.DialContext(context.Background(), "tcp", "203.0.113.5:443"); err == nil {
		t.Fatal("lab dialer must refuse non-loopback addresses")
	}
}

func TestLabInjectedClockExpiresCert(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	e.prober.Now = func() time.Time { return time.Now().Add(100 * 24 * time.Hour) }
	_, d := e.check(t, httpSpec(t, l.TLSURL(lab.TLSValid, "app.lab.test", "/ok"), l))
	expect(t, d, want{diagnose.TLSExpired, event.LayerTLS, event.SideTheirs, event.StatusDown})
}

func TestLabHandshakeDetailIsShort(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	_, d := e.check(t, httpSpec(t, fmt.Sprintf("https://app.lab.test:%d/", l.HandshakePort), l))
	if strings.Contains(d.Summary, "Get \"") || strings.Contains(d.Summary, "https://") {
		t.Errorf("summary should not carry Go's request wrapper: %q", d.Summary)
	}
}

func TestLabAssertions(t *testing.T) {
	l := startLab(t)
	e := newEnv(t, l, platform.Online())
	spec := httpSpec(t, l.HTTPURL("app.lab.test", "/json"), l)
	spec.Assertions = []config.Assertion{{Type: "json", Path: "items[0].state", Value: "ready"},
		{Type: "header", Name: "Content-Type", Value: "json"}}
	if _, d := e.check(t, spec); d.Status != event.StatusUp {
		t.Fatalf("assertions should pass: %+v", d)
	}
	// A captive portal answers 200 with the wrong content.
	spec = httpSpec(t, l.HTTPURL("app.lab.test", "/portal"), l)
	spec.Assertions = []config.Assertion{{Type: "body", Value: `"status":"ok"`}}
	_, d := e.check(t, spec)
	expect(t, d, want{diagnose.HTTPAssertion, event.LayerHTTP, event.SideTheirs, event.StatusDown})
	if !strings.Contains(d.Summary, `body does not contain "\"status\":\"ok\""`) || !strings.Contains(d.Hint, "captive portal") {
		t.Fatalf("text %q / %q", d.Summary, d.Hint)
	}
}
