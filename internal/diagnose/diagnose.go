package diagnose

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/yoockh/sebab/internal/event"
)

// Facts are the observations a probe attaches to an outcome.
type Facts struct {
	Host         string
	Port         int
	Interface    string // VPN interface
	Missing      bool   // VPN interface does not exist
	Detail       string // short low-level error text, already free of secrets
	HTTPStatus   int
	Expected     []int
	Elapsed      time.Duration
	Threshold    time.Duration
	Now          time.Time
	NotBefore    time.Time
	NotAfter     time.Time
	CertNames    []string
	SelfSigned   bool
	RedirectHost string
	Redirects    int
	RetryAfter   time.Duration
	Families     map[string]string // "ipv4" -> "refused"
	Proxy        string
}

// Outcome is what a probe concluded: one code plus facts.
type Outcome struct {
	Code  Code
	Facts Facts
}

// Canary is the result of checking the local side after a failure.
type Canary struct {
	Disabled   bool // no canary targets configured
	Checked    bool // canary was run for this decision
	OK         bool // at least one canary target answered
	DNSChecked bool
	DNSOK      bool
}

// Diagnosis is the final classification of a check.
type Diagnosis struct {
	Code    Code
	Layer   event.Layer
	Side    event.Side
	Status  event.Status
	Summary string
	Hint    string
}

// Cause converts a diagnosis to the event representation.
func (d Diagnosis) Cause() *event.Cause {
	return &event.Cause{Layer: d.Layer, Code: string(d.Code), Summary: d.Summary, Hint: d.Hint}
}

// Diagnose classifies an outcome, applying the canary verdict to decide the side.
// passed lists the layers that completed before the failure.
func Diagnose(o Outcome, passed []event.Layer, c Canary) Diagnosis {
	spec, ok := Taxonomy[o.Code]
	if !ok {
		spec = Spec{Layer: event.LayerHTTP, Side: event.SideUnknown, Status: event.StatusDown}
	}
	if spec.Layer != event.LayerLocal && spec.Layer != event.LayerVPN && spec.Status == event.StatusDown {
		if c.Checked && !c.OK {
			return explain(Outcome{Code: LocalOffline, Facts: o.Facts}, passed)
		}
		if spec.Layer == event.LayerDNS && o.Code != DNSNXDomain && c.DNSChecked && !c.DNSOK {
			return explain(Outcome{Code: LocalDNSBroken, Facts: o.Facts}, passed)
		}
	}
	d := explain(o, passed)
	d.Side = decideSide(o.Code, spec, c)
	return d
}

// decideSide applies the canary verdict to the taxonomy's default side.
func decideSide(code Code, spec Spec, c Canary) event.Side {
	switch spec.Layer {
	case event.LayerLocal, event.LayerVPN:
		return event.SideYours
	case event.LayerTLS, event.LayerHTTP:
		// A server answered, so the local network works: the taxonomy side holds.
		return spec.Side
	}
	// DNS and TCP: without a canary we cannot tell whose fault it is.
	if c.Disabled {
		return event.SideUnknown
	}
	if code == DNSServFail || code == DNSTimeout {
		if c.DNSChecked && c.DNSOK {
			return event.SideTheirs
		}
		return event.SideUnknown
	}
	return spec.Side
}

func explain(o Outcome, passed []event.Layer) Diagnosis {
	spec := Taxonomy[o.Code]
	summary, hint := describe(o)
	if fine := fineLayers(spec.Layer, passed); fine != "" && spec.Layer != event.LayerLocal && spec.Layer != event.LayerVPN {
		hint = strings.TrimSpace(hint + " " + fine)
	}
	return Diagnosis{Code: o.Code, Layer: spec.Layer, Side: spec.Side, Status: spec.Status, Summary: summary, Hint: hint}
}

var layerNames = map[event.Layer]string{event.LayerDNS: "DNS", event.LayerTCP: "TCP", event.LayerTLS: "TLS"}

// fineLayers renders "DNS and TCP are fine." for the passed layers below the failing one.
func fineLayers(failed event.Layer, passed []event.Layer) string {
	var names []string
	for _, l := range passed {
		if n, ok := layerNames[l]; ok && l != failed {
			names = append(names, n)
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0] + " is fine."
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " are fine."
	}
}

func describe(o Outcome) (string, string) {
	f := o.Facts
	host := f.Host
	switch o.Code {
	case LocalNoLink:
		return "no active network interface", "Connect to Wi-Fi or Ethernet. Checks resume when the link is back."
	case LocalNoRoute:
		return "no default route", "You are connected to a network that has no route to the internet. Reconnect or check your router."
	case LocalOffline:
		return "internet canary unreachable", "The canary targets do not answer either, so your connection is down (or a captive portal blocks it). Check Wi-Fi, cable or VPN."
	case LocalDNSBroken:
		return "your DNS resolver is failing", "Name resolution fails for well-known names too. Check your resolver (systemd-resolved, /etc/resolv.conf) or VPN DNS settings."
	case VPNDown:
		state := "is down"
		if f.Missing {
			state = "is missing"
		}
		return fmt.Sprintf("VPN interface %s %s", f.Interface, state),
			fmt.Sprintf("Connect your VPN. Sebab never starts VPNs itself; checks resume when %s is up.", f.Interface)
	case DNSNXDomain:
		return "domain does not resolve (NXDOMAIN)",
			fmt.Sprintf("The name %s does not exist. Check for typos, a deleted DNS record or an expired domain.", host)
	case DNSServFail:
		return fmt.Sprintf("DNS server failure (SERVFAIL) for %s", host),
			"The DNS servers for this name are failing or DNSSEC validation fails."
	case DNSTimeout:
		return fmt.Sprintf("no DNS answer for %s", host),
			"The resolver did not answer in time. If other names resolve, the domain's name servers may be down."
	case DNSNoAddress:
		return fmt.Sprintf("%s has no A/AAAA record", host), "The name exists but has no address records. Check the DNS zone."
	case TCPRefused:
		return fmt.Sprintf("host reachable, nothing listening on %d%s", f.Port, familyNote(f)),
			"The host refused the connection: the service is stopped or listens on another port."
	case TCPTimeout:
		return fmt.Sprintf("no answer on port %d (timed out after %s)%s", f.Port, human(f.Elapsed), familyNote(f)),
			"The host is down, or a firewall silently drops traffic to this port."
	case TCPUnreachable:
		return fmt.Sprintf("network or host unreachable%s", familyNote(f)),
			"There is no route to the host. Check your VPN or routing."
	case TLSExpired:
		return fmt.Sprintf("TLS certificate expired %s ago", ago(f.Now.Sub(f.NotAfter))), clockNote(
			"Renew the certificate on the server.", f, f.Now.Sub(f.NotAfter) > 365*24*time.Hour)
	case TLSNotYetValid:
		return fmt.Sprintf("TLS certificate is not valid until %s", f.NotBefore.UTC().Format("2006-01-02 15:04 MST")),
			clockNote("The certificate starts in the future.", f, true)
	case TLSHostMismatch:
		return fmt.Sprintf("TLS certificate does not cover %s%s", host, namesNote(f.CertNames)),
			"The server presents a certificate for another name. Check the virtual host or CDN configuration."
	case TLSUntrusted:
		what := "signed by an unknown CA"
		if f.SelfSigned {
			what = "self-signed"
		}
		return "TLS certificate is " + what,
			"For an internal service, set ca_file to your CA certificate (or insecure_tls = true as a last resort)."
	case TLSHandshake:
		return "TLS handshake failed" + detailNote(f.Detail),
			"Protocol or cipher mismatch, or the server closed the connection during the handshake."
	case TLSExpiringSoon:
		return fmt.Sprintf("TLS certificate expires in %s", ago(f.NotAfter.Sub(f.Now))),
			fmt.Sprintf("Renew the certificate before %s.", f.NotAfter.UTC().Format("2006-01-02"))
	case HTTPBadGateway:
		return fmt.Sprintf("gateway returned %d, the upstream app is likely down", f.HTTPStatus),
			"The proxy or load balancer answers but cannot reach the application behind it."
	case HTTPUnavailable:
		return "service unavailable (503)", "The service is overloaded or in maintenance."
	case HTTPServerError:
		return fmt.Sprintf("server error (%s)", statusText(f.HTTPStatus)), "The application failed while handling the request. Check its logs."
	case HTTPAuth:
		return fmt.Sprintf("credentials rejected (%d)", f.HTTPStatus),
			"Your token may be expired or invalid. Update it with 'sebab secret set <name>'."
	case HTTPClientError:
		return fmt.Sprintf("unexpected %s", statusText(f.HTTPStatus)), "Check the URL path, method and headers."
	case HTTPRateLimited:
		s := "rate limited (429)"
		if f.RetryAfter > 0 {
			s += fmt.Sprintf(", retry after %s", human(f.RetryAfter))
		}
		return s, "Sebab backs off and checks again after the suggested delay."
	case HTTPTimeout:
		return fmt.Sprintf("connected, but no response within %s", human(f.Elapsed)),
			"The server accepted the connection but the application hangs."
	case HTTPSlow:
		return fmt.Sprintf("response took %s (threshold %s)", human(f.Elapsed), human(f.Threshold)),
			"The service works but is slow."
	case HTTPUnexpStatus:
		return fmt.Sprintf("returned %s, expected %s", statusText(f.HTTPStatus), expectedText(f.Expected)),
			"Update expect_status if this response is intended."
	case HTTPUnexpRedir:
		return fmt.Sprintf("redirected to unexpected host %s", f.RedirectHost),
			"This can be a login page or a captive portal. Add the host to allowed_redirect_hosts if it is expected."
	case HTTPRedirLoop:
		return fmt.Sprintf("more than %d redirects", 10), "The server redirects in a loop. Check its redirect rules."
	}
	return string(o.Code), ""
}

func familyNote(f Facts) string {
	if len(f.Families) < 2 {
		return ""
	}
	keys := make([]string, 0, len(f.Families))
	for k := range f.Families {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %s", strings.ToUpper(k[:2])+k[2:], f.Families[k]))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func clockNote(base string, f Facts, large bool) string {
	if !large || f.Now.IsZero() {
		return base
	}
	return fmt.Sprintf("%s If that looks wrong, check your local clock (it reads %s).", base,
		f.Now.UTC().Format("2006-01-02 15:04 MST"))
}

func namesNote(names []string) string {
	if len(names) == 0 {
		return ""
	}
	if len(names) > 3 {
		names = append(names[:3:3], "...")
	}
	return " (it covers " + strings.Join(names, ", ") + ")"
}

func detailNote(d string) string {
	if d == "" {
		return ""
	}
	return ": " + d
}

func statusText(code int) string {
	if t := http.StatusText(code); t != "" {
		return fmt.Sprintf("%d %s", code, t)
	}
	return fmt.Sprintf("%d", code)
}

func expectedText(codes []int) string {
	if len(codes) == 0 {
		return "2xx"
	}
	parts := make([]string, len(codes))
	for i, c := range codes {
		parts[i] = fmt.Sprintf("%d", c)
	}
	return strings.Join(parts, " or ")
}

// human formats a duration for summaries: "850 ms", "4.2 s", "3 min".
func human(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		s := d.Seconds()
		if s == math.Trunc(s) {
			return fmt.Sprintf("%d s", int(s))
		}
		return fmt.Sprintf("%.1f s", s)
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
}

// Human is exported for notification texts.
func Human(d time.Duration) string { return human(d) }

// ago formats a coarse age: "2 days", "5 hours", "12 minutes".
func ago(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return fmt.Sprintf("%d %ss", n, unit)
	}
	switch {
	case d >= 48*time.Hour:
		return plural(int(d.Hours()/24), "day")
	case d >= time.Hour:
		return plural(int(d.Hours()), "hour")
	case d >= time.Minute:
		return plural(int(d.Minutes()), "minute")
	default:
		return plural(int(d.Seconds()), "second")
	}
}

// Ago is exported for notification texts ("recovered after 23 minutes").
func Ago(d time.Duration) string { return ago(d) }
