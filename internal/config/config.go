// Package config loads, validates and writes ~/.config/sebab/config.toml, the single
// source of truth for monitors shared by every frontend.
package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Limits from the PRD non-functional requirements.
const (
	MinInterval     = 30 * time.Second
	DefaultInterval = 60 * time.Second
	MaxMonitors     = 100
	MaxThreshold    = 10
)

// Defaults applied when [defaults] or a monitor leaves a field unset.
var (
	DefaultCanary        = []string{"tcp://1.1.1.1:443", "tcp://9.9.9.9:443"}
	DefaultDNSCanary     = []string{"example.com"}
	defaultTimeout       = 10 * time.Second
	defaultSlowThreshold = 2 * time.Second
	defaultHeartbeat     = 15 * time.Second
	defaultTLSWarnDays   = 14
	defaultThreshold     = 2
)

// Duration is a time.Duration written as a Go duration string ("60s") in TOML.
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("invalid duration %q (use values like \"60s\" or \"2m\")", string(b))
	}
	*d = Duration(v)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
// Whole seconds are written as "60s" rather than Go's "1m0s".
func (d Duration) MarshalText() ([]byte, error) {
	v := time.Duration(d)
	if v%time.Second == 0 {
		return []byte(strconv.FormatInt(int64(v/time.Second), 10) + "s"), nil
	}
	return []byte(v.String()), nil
}

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config is the on-disk configuration.
type Config struct {
	Defaults Defaults  `toml:"defaults"`
	History  History   `toml:"history,omitempty"`
	Monitors []Monitor `toml:"monitor"`
}

// History configures the local check history (sparklines, incident reports).
type History struct {
	Disabled bool `toml:"disabled,omitempty"`
	KeepDays int  `toml:"keep_days,omitempty"` // default 7
}

// Defaults are engine-wide settings and per-monitor fallbacks.
type Defaults struct {
	Interval         *Duration `toml:"interval,omitempty"`
	Timeout          *Duration `toml:"timeout,omitempty"`
	FailThreshold    int       `toml:"fail_threshold,omitempty"`
	RecoverThreshold int       `toml:"recover_threshold,omitempty"`
	SlowThreshold    *Duration `toml:"slow_threshold,omitempty"`
	TLSWarnDays      *int      `toml:"tls_warn_days,omitempty"`
	Heartbeat        *Duration `toml:"heartbeat,omitempty"`
	// Canary nil means the default list; an empty list disables the canary.
	Canary    *[]string `toml:"canary,omitempty"`
	DNSCanary *[]string `toml:"dns_canary,omitempty"`
}

// Auth describes credentials. The secret itself never lives in config.toml:
// Secret is a reference resolved by the secret package.
type Auth struct {
	Type     string `toml:"type"`               // bearer, header, basic
	Secret   string `toml:"secret"`             // reference: name, or env:VAR
	Header   string `toml:"header,omitempty"`   // header type only
	Username string `toml:"username,omitempty"` // basic type only
}

// VPN is a monitor precondition.
type VPN struct {
	Interface string `toml:"interface"`
}

// Monitor is one configured endpoint.
type Monitor struct {
	ID                   string            `toml:"id"`
	Name                 string            `toml:"name"`
	URL                  string            `toml:"url,omitempty"`
	Target               string            `toml:"target,omitempty"`
	Interval             *Duration         `toml:"interval,omitempty"`
	Timeout              *Duration         `toml:"timeout,omitempty"`
	ExpectStatus         []int             `toml:"expect_status,omitempty"`
	SlowThreshold        *Duration         `toml:"slow_threshold,omitempty"`
	FailThreshold        int               `toml:"fail_threshold,omitempty"`
	RecoverThreshold     int               `toml:"recover_threshold,omitempty"`
	TLSWarnDays          *int              `toml:"tls_warn_days,omitempty"`
	Paused               bool              `toml:"paused,omitempty"`
	Method               string            `toml:"method,omitempty"`
	Headers              map[string]string `toml:"headers,omitempty"`
	Auth                 *Auth             `toml:"auth,omitempty"`
	RequiresVPN          *VPN              `toml:"requires_vpn,omitempty"`
	InsecureTLS          bool              `toml:"insecure_tls,omitempty"`
	CAFile               string            `toml:"ca_file,omitempty"`
	FollowRedirects      *bool             `toml:"follow_redirects,omitempty"`
	AllowedRedirectHosts []string          `toml:"allowed_redirect_hosts,omitempty"`
}

// Kind is "http" for URL monitors and "tcp" for raw targets.
func (m Monitor) Kind() string {
	if m.URL != "" {
		return "http"
	}
	return "tcp"
}

// Endpoint returns the URL or target string.
func (m Monitor) Endpoint() string {
	if m.URL != "" {
		return m.URL
	}
	return m.Target
}

var (
	idPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	ifacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)
	headerName   = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	secretRef    = regexp.MustCompile(`^(env:[A-Za-z_][A-Za-z0-9_]*|[A-Za-z0-9][A-Za-z0-9._-]{0,63})$`)
)

// ValidationError lists every problem found in a config.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string { return strings.Join(e.Problems, "; ") }

// Validate checks the whole config and returns a *ValidationError or nil.
func (c *Config) Validate() error {
	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	probs = append(probs, c.Defaults.validate()...)
	if c.History.KeepDays < 0 || c.History.KeepDays > 365 {
		add("history: keep_days must be between 1 and 365")
	}
	if len(c.Monitors) > MaxMonitors {
		add("too many monitors: %d (limit %d)", len(c.Monitors), MaxMonitors)
	}
	seen := map[string]bool{}
	for i, m := range c.Monitors {
		label := m.ID
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		for _, p := range m.Validate() {
			add("monitor '%s': %s", label, p)
		}
		if m.ID != "" && seen[m.ID] {
			add("monitor '%s': duplicate id", m.ID)
		}
		seen[m.ID] = true
	}
	if len(probs) > 0 {
		return &ValidationError{Problems: probs}
	}
	return nil
}

func (d Defaults) validate() []string {
	var probs []string
	if d.Interval != nil && d.Interval.D() < MinInterval {
		probs = append(probs, fmt.Sprintf("defaults: interval below %s", MinInterval))
	}
	if d.Timeout != nil && d.Timeout.D() <= 0 {
		probs = append(probs, "defaults: timeout must be positive")
	}
	if d.Heartbeat != nil && (d.Heartbeat.D() < time.Second || d.Heartbeat.D() > 5*time.Minute) {
		probs = append(probs, "defaults: heartbeat must be between 1s and 5m")
	}
	probs = append(probs, thresholdProblems("defaults: ", d.FailThreshold, d.RecoverThreshold)...)
	if d.Canary != nil {
		for _, c := range *d.Canary {
			if _, _, err := ParseTCPTarget(c); err != nil {
				probs = append(probs, fmt.Sprintf("defaults: canary %q: %v", c, err))
			}
		}
	}
	if d.DNSCanary != nil {
		for _, n := range *d.DNSCanary {
			if !validHostname(n) {
				probs = append(probs, fmt.Sprintf("defaults: dns_canary %q is not a hostname", n))
			}
		}
	}
	return probs
}

func thresholdProblems(prefix string, fail, recover int) []string {
	var probs []string
	if fail < 0 || fail > MaxThreshold {
		probs = append(probs, fmt.Sprintf("%sfail_threshold must be between 1 and %d", prefix, MaxThreshold))
	}
	if recover < 0 || recover > MaxThreshold {
		probs = append(probs, fmt.Sprintf("%srecover_threshold must be between 1 and %d", prefix, MaxThreshold))
	}
	return probs
}

// Validate checks one monitor and returns human-readable problems.
func (m Monitor) Validate() []string {
	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	if !idPattern.MatchString(m.ID) {
		add("id must be lowercase letters, digits, '.', '_' or '-' (max 64 chars)")
	}
	if strings.TrimSpace(m.Name) == "" {
		add("name is required")
	}
	switch {
	case m.URL != "" && m.Target != "":
		add("set either url or target, not both")
	case m.URL != "":
		if err := ValidateURL(m.URL); err != nil {
			add("%v", err)
		}
	case m.Target != "":
		if _, _, err := ParseTCPTarget(m.Target); err != nil {
			add("%v", err)
		}
	default:
		add("url or target is required")
	}
	if m.Interval != nil && m.Interval.D() < MinInterval {
		add("interval below %s", MinInterval)
	}
	if m.Timeout != nil && m.Timeout.D() <= 0 {
		add("timeout must be positive")
	}
	if m.Interval != nil && m.Timeout != nil && m.Timeout.D() >= m.Interval.D() {
		add("timeout must be shorter than the interval")
	}
	if m.SlowThreshold != nil && m.SlowThreshold.D() < 0 {
		add("slow_threshold must not be negative")
	}
	if m.TLSWarnDays != nil && (*m.TLSWarnDays < 0 || *m.TLSWarnDays > 365) {
		add("tls_warn_days must be between 0 and 365")
	}
	probs = append(probs, thresholdProblems("", m.FailThreshold, m.RecoverThreshold)...)
	for _, code := range m.ExpectStatus {
		if code < 100 || code > 599 {
			add("expect_status %d is not an HTTP status code", code)
		}
	}
	if m.Kind() == "tcp" && (len(m.ExpectStatus) > 0 || m.Auth != nil || len(m.Headers) > 0) {
		add("expect_status, auth and headers only apply to url monitors")
	}
	if m.Method != "" && !map[string]bool{"GET": true, "HEAD": true}[strings.ToUpper(m.Method)] {
		add("method must be GET or HEAD")
	}
	for k := range m.Headers {
		if !headerName.MatchString(k) {
			add("header name %q is invalid", k)
		}
		if strings.EqualFold(k, "Authorization") {
			add("put credentials in auth, not in headers")
		}
	}
	probs = append(probs, m.Auth.problems()...)
	if m.RequiresVPN != nil && !ifacePattern.MatchString(m.RequiresVPN.Interface) {
		add("requires_vpn.interface %q is not a valid interface name", m.RequiresVPN.Interface)
	}
	if m.InsecureTLS && m.CAFile != "" {
		add("set either insecure_tls or ca_file, not both")
	}
	for _, h := range m.AllowedRedirectHosts {
		if !validHostname(h) {
			add("allowed_redirect_hosts entry %q is not a hostname", h)
		}
	}
	return probs
}

func (a *Auth) problems() []string {
	if a == nil {
		return nil
	}
	var probs []string
	switch a.Type {
	case "bearer":
	case "header":
		if !headerName.MatchString(a.Header) {
			probs = append(probs, "auth.header must be a valid header name for type \"header\"")
		}
	case "basic":
		if a.Username == "" {
			probs = append(probs, "auth.username is required for type \"basic\"")
		}
	default:
		probs = append(probs, fmt.Sprintf("auth.type %q must be bearer, header or basic", a.Type))
	}
	if !secretRef.MatchString(a.Secret) {
		probs = append(probs, "auth.secret must be a secret name (letters, digits, '.', '_', '-') or env:VARIABLE")
	}
	return probs
}

// ValidateURL checks a monitor URL.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("bad URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("bad URL %q: scheme must be http or https", raw)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("bad URL %q: missing host", raw)
	}
	if strings.Contains(u.Hostname(), "/") || strings.ContainsAny(u.Hostname(), "*") {
		return fmt.Errorf("bad URL %q: ranges and wildcards are not supported", raw)
	}
	if p := u.Port(); p != "" {
		if _, err := parsePort(p); err != nil {
			return fmt.Errorf("bad URL %q: %v", raw, err)
		}
	}
	if u.User != nil {
		return fmt.Errorf("bad URL %q: put credentials in auth, not in the URL", raw)
	}
	return nil
}

// ParseTCPTarget accepts "tcp://host:port" or "host:port" and returns host and port.
func ParseTCPTarget(raw string) (string, int, error) {
	s := strings.TrimPrefix(raw, "tcp://")
	if strings.Contains(s, "://") {
		return "", 0, fmt.Errorf("bad target %q: only tcp:// targets are supported (use url for HTTP)", raw)
	}
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, fmt.Errorf("bad target %q: missing port (use host:port)", raw)
	}
	if host == "" {
		return "", 0, fmt.Errorf("bad target %q: missing host", raw)
	}
	if strings.ContainsAny(host, "/*") || strings.Contains(portStr, "-") {
		return "", 0, fmt.Errorf("bad target %q: ranges and wildcards are not supported", raw)
	}
	port, err := parsePort(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("bad target %q: %v", raw, err)
	}
	if net.ParseIP(host) == nil && !validHostname(host) {
		return "", 0, fmt.Errorf("bad target %q: invalid host", raw)
	}
	return host, port, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("port %q must be between 1 and 65535", s)
	}
	return p, nil
}

func validHostname(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				return false
			}
		}
	}
	return true
}

// Slugify turns a display name into a valid monitor id.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-")
	}
	if s == "" {
		s = "monitor"
	}
	return s
}
