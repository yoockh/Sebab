package diagnose

import (
	"strings"
	"testing"
	"time"

	"github.com/yoockh/sebab/internal/event"
)

// appendixB is PRD Appendix B transcribed: code -> layer, side.
// "theirs or unknown" and "theirs or yours" collapse to the default side used
// when nothing else is known.
var appendixB = map[Code][2]string{
	"LOCAL_NO_LINK": {"local", "yours"}, "LOCAL_NO_ROUTE": {"local", "yours"},
	"LOCAL_OFFLINE": {"local", "yours"}, "LOCAL_DNS_BROKEN": {"local", "yours"},
	"VPN_DOWN":     {"vpn", "yours"},
	"DNS_NXDOMAIN": {"dns", "theirs"}, "DNS_SERVFAIL": {"dns", "theirs"},
	"DNS_TIMEOUT": {"dns", "unknown"}, "DNS_NO_ADDRESS": {"dns", "theirs"},
	"TCP_REFUSED": {"tcp", "theirs"}, "TCP_TIMEOUT": {"tcp", "theirs"}, "TCP_UNREACHABLE": {"tcp", "unknown"},
	"TLS_EXPIRED": {"tls", "theirs"}, "TLS_NOT_YET_VALID": {"tls", "unknown"},
	"TLS_HOSTNAME_MISMATCH": {"tls", "theirs"}, "TLS_UNTRUSTED": {"tls", "theirs"},
	"TLS_HANDSHAKE_FAILED": {"tls", "theirs"},
	"HTTP_BAD_GATEWAY":     {"http", "theirs"}, "HTTP_UNAVAILABLE": {"http", "theirs"},
	"HTTP_SERVER_ERROR": {"http", "theirs"}, "HTTP_AUTH": {"http", "yours"},
	"HTTP_CLIENT_ERROR": {"http", "theirs"}, "HTTP_RATE_LIMITED": {"http", "theirs"},
	"HTTP_TIMEOUT": {"http", "theirs"}, "HTTP_SLOW": {"http", "theirs"},
	"HTTP_UNEXPECTED_STATUS": {"http", "theirs"}, "HTTP_UNEXPECTED_REDIRECT": {"http", "theirs"},
	"HTTP_REDIRECT_LOOP": {"http", "theirs"},
}

func TestTaxonomyMatchesAppendixB(t *testing.T) {
	for code, want := range appendixB {
		spec, ok := Taxonomy[code]
		if !ok {
			t.Errorf("%s missing from taxonomy", code)
			continue
		}
		if string(spec.Layer) != want[0] || string(spec.Side) != want[1] {
			t.Errorf("%s: got %s/%s want %s/%s", code, spec.Layer, spec.Side, want[0], want[1])
		}
	}
	for code := range Taxonomy {
		if _, ok := appendixB[code]; !ok && code != TLSExpiringSoon && code != HTTPAssertion {
			t.Errorf("%s is not in Appendix B", code)
		}
	}
}

func TestDegradedCodes(t *testing.T) {
	for _, c := range []Code{HTTPRateLimited, HTTPSlow, TLSExpiringSoon} {
		if Taxonomy[c].Status != event.StatusDegraded {
			t.Errorf("%s should be degraded", c)
		}
	}
	if Taxonomy[VPNDown].Status != event.StatusPaused || Taxonomy[LocalOffline].Status != event.StatusUnknown {
		t.Error("vpn should pause, local should be unknown")
	}
}

func TestEveryCodeHasSummaryAndHint(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	f := Facts{Host: "api.example.com", Port: 443, Interface: "wg0", HTTPStatus: 502, Now: now,
		NotAfter: now.Add(-49 * time.Hour), NotBefore: now.Add(time.Hour), Elapsed: 4200 * time.Millisecond,
		Threshold: 2 * time.Second, RedirectHost: "login.example.net"}
	for code := range Taxonomy {
		d := Diagnose(Outcome{Code: code, Facts: f}, nil, Canary{Checked: true, OK: true})
		if d.Summary == "" || d.Hint == "" || d.Summary == string(code) {
			t.Errorf("%s: empty text %+v", code, d)
		}
		if d.Code != code {
			t.Errorf("%s: code changed to %s with a healthy canary", code, d.Code)
		}
	}
}

func TestPRDExampleTexts(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		o      Outcome
		passed []event.Layer
		sum    string
		hint   string
	}{
		{Outcome{Code: TLSExpired, Facts: Facts{Now: now, NotAfter: now.Add(-50 * time.Hour)}},
			[]event.Layer{event.LayerDNS, event.LayerTCP}, "TLS certificate expired 2 days ago", "DNS and TCP are fine."},
		{Outcome{Code: TCPRefused, Facts: Facts{Port: 5432}}, []event.Layer{event.LayerDNS},
			"host reachable, nothing listening on 5432", "DNS is fine."},
		{Outcome{Code: HTTPBadGateway, Facts: Facts{HTTPStatus: 502}},
			[]event.Layer{event.LayerDNS, event.LayerTCP, event.LayerTLS},
			"gateway returned 502, the upstream app is likely down", "DNS, TCP and TLS are fine."},
		{Outcome{Code: HTTPAuth, Facts: Facts{HTTPStatus: 401}}, nil, "credentials rejected (401)", "token may be expired or invalid"},
		{Outcome{Code: HTTPSlow, Facts: Facts{Elapsed: 4200 * time.Millisecond, Threshold: 2 * time.Second}}, nil,
			"response took 4.2 s (threshold 2 s)", "slow"},
		{Outcome{Code: VPNDown, Facts: Facts{Interface: "wg0"}}, nil, "VPN interface wg0 is down", "never starts VPNs"},
		{Outcome{Code: DNSNXDomain, Facts: Facts{Host: "x.example"}}, nil, "domain does not resolve (NXDOMAIN)", "x.example"},
		{Outcome{Code: TCPTimeout, Facts: Facts{Port: 443, Elapsed: 5 * time.Second,
			Families: map[string]string{"ipv4": "timed out", "ipv6": "unreachable"}}}, nil,
			"no answer on port 443 (timed out after 5 s) (IPv4 timed out, IPv6 unreachable)", "firewall"},
	}
	for _, tc := range cases {
		d := Diagnose(tc.o, tc.passed, Canary{Checked: true, OK: true})
		if d.Summary != tc.sum {
			t.Errorf("%s summary %q want %q", tc.o.Code, d.Summary, tc.sum)
		}
		if !strings.Contains(d.Hint, tc.hint) {
			t.Errorf("%s hint %q lacks %q", tc.o.Code, d.Hint, tc.hint)
		}
	}
}

func TestSideDecisions(t *testing.T) {
	ok := Canary{Checked: true, OK: true, DNSChecked: true, DNSOK: true}
	cases := []struct {
		name   string
		code   Code
		canary Canary
		want   Code
		side   event.Side
		status event.Status
	}{
		{"refused theirs", TCPRefused, ok, TCPRefused, event.SideTheirs, event.StatusDown},
		{"canary down means offline", TCPTimeout, Canary{Checked: true, OK: false}, LocalOffline, event.SideYours, event.StatusUnknown},
		{"canary down beats dns", DNSTimeout, Canary{Checked: true, OK: false}, LocalOffline, event.SideYours, event.StatusUnknown},
		{"dns canary fails", DNSServFail, Canary{Checked: true, OK: true, DNSChecked: true, DNSOK: false}, LocalDNSBroken, event.SideYours, event.StatusUnknown},
		{"nxdomain is never local", DNSNXDomain, Canary{Checked: true, OK: true, DNSChecked: true, DNSOK: false}, DNSNXDomain, event.SideTheirs, event.StatusDown},
		{"servfail with dns canary ok", DNSServFail, ok, DNSServFail, event.SideTheirs, event.StatusDown},
		{"timeout with dns canary ok", DNSTimeout, ok, DNSTimeout, event.SideTheirs, event.StatusDown},
		{"timeout without dns canary", DNSTimeout, Canary{Checked: true, OK: true}, DNSTimeout, event.SideUnknown, event.StatusDown},
		{"canary disabled tcp", TCPRefused, Canary{Disabled: true}, TCPRefused, event.SideUnknown, event.StatusDown},
		{"canary disabled http keeps side", HTTPBadGateway, Canary{Disabled: true}, HTTPBadGateway, event.SideTheirs, event.StatusDown},
		{"auth is yours", HTTPAuth, ok, HTTPAuth, event.SideYours, event.StatusDown},
		{"vpn yours paused", VPNDown, Canary{}, VPNDown, event.SideYours, event.StatusPaused},
		{"degraded not overridden", HTTPSlow, Canary{Checked: true, OK: false}, HTTPSlow, event.SideTheirs, event.StatusDegraded},
		{"unreachable unknown", TCPUnreachable, ok, TCPUnreachable, event.SideUnknown, event.StatusDown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Diagnose(Outcome{Code: tc.code}, nil, tc.canary)
			if d.Code != tc.want || d.Side != tc.side || d.Status != tc.status {
				t.Errorf("got %s/%s/%s want %s/%s/%s", d.Code, d.Side, d.Status, tc.want, tc.side, tc.status)
			}
		})
	}
}

func TestClockNoteOnNotYetValid(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	d := Diagnose(Outcome{Code: TLSNotYetValid, Facts: Facts{Now: now, NotBefore: now.Add(72 * time.Hour)}}, nil, Canary{})
	if !strings.Contains(d.Hint, "local clock") || !strings.Contains(d.Hint, "2026-10-11 12:00 UTC") {
		t.Errorf("hint %q", d.Hint)
	}
}

func TestHumanAndAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{850 * time.Millisecond: "850 ms", 4200 * time.Millisecond: "4.2 s",
		2 * time.Second: "2 s", 3 * time.Minute: "3 min", 2 * time.Hour: "2 h"} {
		if got := human(d); got != want {
			t.Errorf("human(%v)=%q want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{23 * time.Minute: "23 minutes", time.Minute: "1 minute",
		5 * time.Hour: "5 hours", 72 * time.Hour: "3 days"} {
		if got := ago(d); got != want {
			t.Errorf("ago(%v)=%q want %q", d, got, want)
		}
	}
}

func TestHumanRoundsToTenths(t *testing.T) {
	if got := human(5000300 * time.Microsecond); got != "5 s" {
		t.Errorf("got %q", got)
	}
}
