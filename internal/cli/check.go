package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/diagnose"
	"github.com/yoockh/sebab/internal/engine"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/probe"
	"github.com/yoockh/sebab/internal/report"
	"github.com/yoockh/sebab/internal/secret"
)

// Exit codes of `sebab check` (documented in docs/cli.md).
const (
	ExitDegraded = 1
	ExitLocal    = 10
	ExitVPN      = 11
	ExitDNS      = 12
	ExitTCP      = 13
	ExitTLS      = 14
	ExitHTTP     = 15
)

var layerExit = map[event.Layer]int{
	event.LayerLocal: ExitLocal, event.LayerVPN: ExitVPN, event.LayerDNS: ExitDNS,
	event.LayerTCP: ExitTCP, event.LayerTLS: ExitTLS, event.LayerHTTP: ExitHTTP,
}

// monitorFlags are shared by check, monitor add and monitor edit.
type monitorFlags struct {
	name, id, target        string
	interval, timeout, slow time.Duration
	expect                  string
	authType, secretRef     string
	headerName, username    string
	secretFile, secretBknd  string
	secretStdin, keepSecret bool
	vpn, caFile, method     string
	insecure, noFollow      bool
	paused                  bool
	allowRedirect           string
	jsonOut                 bool
}

func (f *monitorFlags) register(fs flagSet, forCheck bool) {
	fs.StringVar(&f.name, "name", "", "display name")
	if !forCheck {
		fs.StringVar(&f.id, "id", "", "monitor id (default: derived from the name)")
		fs.StringVar(&f.target, "target", "", "URL (http/https) or host:port")
		fs.DurationVar(&f.interval, "interval", 0, "check interval (minimum 30s, default 60s)")
		fs.BoolVar(&f.paused, "paused", false, "add the monitor paused")
	}
	fs.DurationVar(&f.timeout, "timeout", 0, "check timeout (default 10s)")
	fs.DurationVar(&f.slow, "slow", 0, "latency threshold for HTTP_SLOW (default 2s)")
	fs.StringVar(&f.expect, "expect", "", "expected HTTP status codes, comma separated (default any 2xx)")
	fs.StringVar(&f.authType, "auth", "", "auth type: bearer, header or basic")
	fs.StringVar(&f.secretRef, "secret", "", "secret reference: a stored secret name or env:VARIABLE")
	fs.StringVar(&f.headerName, "header-name", "", "header for --auth header (for example X-API-Key)")
	fs.StringVar(&f.username, "username", "", "user name for --auth basic")
	fs.StringVar(&f.secretFile, "secret-file", "", "read the secret value from a 'sebab secret handoff' file")
	fs.BoolVar(&f.secretStdin, "secret-stdin", false, "read the secret value from stdin")
	fs.BoolVar(&f.keepSecret, "keep-secret-file", false, "do not delete --secret-file after reading (live test before save)")
	fs.StringVar(&f.secretBknd, "secret-backend", "", "where to store a new secret: keyring or file (default: keyring if available)")
	fs.StringVar(&f.vpn, "vpn", "", "only check while this VPN interface is up (for example wg0)")
	fs.StringVar(&f.caFile, "ca-file", "", "PEM file with the CA that signs the server certificate")
	fs.BoolVar(&f.insecure, "insecure", false, "skip TLS verification (shown as insecure everywhere)")
	fs.StringVar(&f.method, "method", "", "HTTP method: GET or HEAD")
	fs.BoolVar(&f.noFollow, "no-follow", false, "do not follow redirects")
	fs.StringVar(&f.allowRedirect, "allow-redirect", "", "extra hosts redirects may go to, comma separated")
	fs.BoolVar(&f.jsonOut, "json", false, "print JSON")
}

// normalizeTarget turns user input into a URL or a tcp:// target.
func normalizeTarget(raw string) (url string, tcp string, err error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", "", fmt.Errorf("target is required (a URL like https://example.com or host:port)")
	case strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://"):
		return raw, "", config.ValidateURL(raw)
	case strings.HasPrefix(raw, "tcp://"):
		_, _, err := config.ParseTCPTarget(raw)
		return "", raw, err
	case strings.Contains(raw, "://"):
		return "", "", fmt.Errorf("bad target %q: use http://, https:// or host:port", raw)
	}
	if _, _, err := config.ParseTCPTarget(raw); err == nil {
		return "", "tcp://" + raw, nil
	}
	if !strings.ContainsAny(raw, ":/") {
		u := "https://" + raw + "/"
		return u, "", config.ValidateURL(u)
	}
	_, _, err = config.ParseTCPTarget(raw)
	return "", "", err
}

func parseExpect(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 100 || n > 599 {
			return nil, fmt.Errorf("--expect: %q is not an HTTP status code", part)
		}
		out = append(out, n)
	}
	return out, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// applyFlags copies the set flags onto m. set lists the flags given explicitly.
func (f *monitorFlags) applyFlags(m *config.Monitor, set map[string]bool) error {
	if set["name"] {
		m.Name = f.name
	}
	if set["id"] {
		m.ID = f.id
	}
	if set["target"] {
		u, tcp, err := normalizeTarget(f.target)
		if err != nil {
			return err
		}
		m.URL, m.Target = u, tcp
	}
	dur := func(d time.Duration) *config.Duration { v := config.Duration(d); return &v }
	if set["interval"] {
		m.Interval = dur(f.interval)
	}
	if set["timeout"] {
		m.Timeout = dur(f.timeout)
	}
	if set["slow"] {
		m.SlowThreshold = dur(f.slow)
	}
	if set["expect"] {
		codes, err := parseExpect(f.expect)
		if err != nil {
			return err
		}
		m.ExpectStatus = codes
	}
	if set["paused"] {
		m.Paused = f.paused
	}
	if set["vpn"] {
		m.RequiresVPN = nil
		if f.vpn != "" {
			m.RequiresVPN = &config.VPN{Interface: f.vpn}
		}
	}
	if set["ca-file"] {
		m.CAFile = f.caFile
	}
	if set["insecure"] {
		m.InsecureTLS = f.insecure
	}
	if set["method"] {
		m.Method = strings.ToUpper(f.method)
	}
	if set["no-follow"] {
		follow := !f.noFollow
		m.FollowRedirects = &follow
	}
	if set["allow-redirect"] {
		m.AllowedRedirectHosts = splitList(f.allowRedirect)
	}
	if set["auth"] {
		if f.authType == "" || f.authType == "none" {
			m.Auth = nil
		} else {
			m.Auth = &config.Auth{Type: f.authType}
		}
	}
	if m.Auth != nil {
		if set["secret"] {
			m.Auth.Secret = f.secretRef
		}
		if set["header-name"] {
			m.Auth.Header = f.headerName
		}
		if set["username"] {
			m.Auth.Username = f.username
		}
	}
	return nil
}

// secretValue reads a secret from --secret-file or --secret-stdin, if given.
func (f *monitorFlags) secretValue(rt *runtime) (string, bool, error) {
	switch {
	case f.secretFile != "" && f.secretStdin:
		return "", false, fmt.Errorf("use either --secret-file or --secret-stdin")
	case f.secretFile != "":
		v, err := secret.ReadHandoff(f.secretFile, handoffDirs(rt, f.secretFile), f.keepSecret)
		return v, err == nil, err
	case f.secretStdin:
		v, err := secret.ReadSecret(rt.env.Stdin)
		return v, err == nil, err
	}
	return "", false, nil
}

func runCheck(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("check", env)
	var f monitorFlags
	f.register(fs, true)
	monitorID := fs.String("monitor", "", "check a configured monitor by id instead of a target")
	noCanary := fs.Bool("no-canary", false, "skip the internet canary (side detection)")
	markdown := fs.Bool("markdown", false, "print a Markdown diagnosis (for bug reports)")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: sebab check [flags] <url | host:port>")
		fs.PrintDefaults()
	}
	if code, ok := parseFlags(fs, reorder(args)); !ok {
		return code
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	cfg, err := rt.loadConfig()
	if err != nil {
		return errorf(env, "%v", err)
	}
	m, value, code := checkMonitor(rt, cfg, &f, fs, *monitorID)
	if code != ExitOK {
		return code
	}
	settings := cfg.Resolve(m)
	spec, problems := engine.BuildSpec(settings, rt.store, rt.redactor)
	if value != "" && m.Auth != nil {
		addSecret(rt.redactor, m.Auth, value)
		spec.Auth = &probe.Credentials{Type: m.Auth.Type, Header: m.Auth.Header, Username: m.Auth.Username, Secret: value}
		problems = dropSecretProblems(problems)
	}
	for _, p := range problems {
		fmt.Fprintf(env.Stderr, "sebab: warning: %s\n", rt.redactor.Scrub(p.Message))
	}
	res := rt.prober.Run(ctx, spec)
	if res.Canceled {
		return ExitInternal
	}
	ev, d := buildCheckEvent(ctx, rt, cfg, settings, res, *noCanary)
	switch {
	case f.jsonOut:
		enc := event.NewEncoder(env.Stdout, rt.redactor)
		if err := enc.Encode(ev); err != nil {
			return ExitInternal
		}
	case *markdown:
		fmt.Fprint(env.Stdout, report.Markdown(ev, versionString(), rt.redactor))
		if *monitorID != "" {
			if h := openHistory(rt, cfg); h != nil {
				fmt.Fprint(env.Stdout, report.Timeline(h, *monitorID, 24*time.Hour, rt.redactor))
				h.Close()
			}
		}
	default:
		printHuman(env.Stdout, rt.redactor, settings, res, d, ev)
	}
	return exitFor(d)
}

// reorder lets flags follow the positional target ("sebab check URL --json").
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !isBoolFlag(a) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

var boolFlags = map[string]bool{"json": true, "insecure": true, "no-follow": true, "secret-stdin": true,
	"keep-secret-file": true, "no-canary": true, "markdown": true, "paused": true, "no-test": true, "raw": true,
	"h": true, "help": true, "clear-auth": true, "fresh": true}

func isBoolFlag(a string) bool { return boolFlags[strings.TrimLeft(a, "-")] }

// checkMonitor builds the monitor to check from flags or the config.
func checkMonitor(rt *runtime, cfg *config.Config, f *monitorFlags, fs flagSet, id string) (config.Monitor, string, int) {
	env := rt.env
	var m config.Monitor
	if id != "" {
		i := cfg.Find(id)
		if i < 0 {
			return m, "", usageError(env, "check: no monitor %q in %s", id, rt.paths.ConfigFile)
		}
		m = cfg.Monitors[i]
	} else {
		if fs.NArg() != 1 {
			return m, "", usageError(env, "check: exactly one target is required (or --monitor ID)")
		}
		f.target = fs.Arg(0)
		m = config.Monitor{ID: "check", Name: fs.Arg(0)}
	}
	set := visited(fs)
	if id == "" {
		set["target"] = true
	}
	if err := f.applyFlags(&m, set); err != nil {
		return m, "", usageError(env, "check: %v", err)
	}
	if f.name != "" {
		m.Name = f.name
	}
	value, has, err := f.secretValue(rt)
	if err != nil {
		return m, "", usageError(env, "check: %v", err)
	}
	if m.Auth != nil && m.Auth.Secret == "" {
		m.Auth.Secret = "cli-check" // placeholder reference; the value comes from the flag
		if !has {
			return m, "", usageError(env, "check: --auth needs --secret NAME, --secret-file or --secret-stdin")
		}
	}
	if probs := m.Validate(); len(probs) > 0 && id == "" {
		for _, p := range probs {
			if !strings.HasPrefix(p, "interval") {
				return m, "", usageError(env, "check: %s", p)
			}
		}
	}
	return m, value, ExitOK
}

func addSecret(red *secret.Redactor, a *config.Auth, value string) {
	if a.Type == "basic" {
		red.AddBasic(a.Username, value)
		return
	}
	red.Add(value)
}

func dropSecretProblems(ps []engine.SpecProblem) []engine.SpecProblem {
	var out []engine.SpecProblem
	for _, p := range ps {
		if p.Code != event.ErrSecretMissing {
			out = append(out, p)
		}
	}
	return out
}

// buildCheckEvent runs the canary when needed and renders the event.
func buildCheckEvent(ctx context.Context, rt *runtime, cfg *config.Config, s config.Settings, res probe.Result,
	noCanary bool) (event.Check, diagnose.Diagnosis) {
	d := diagnose.Diagnosis{Status: event.StatusUp}
	if res.Outcome != nil {
		canary := diagnose.Canary{Disabled: true}
		code := res.Outcome.Code
		if !noCanary && res.Failed() && !diagnose.IsLocal(code) && code != diagnose.VPNDown {
			canary = rt.canary(cfg.EngineSettings()).Check(ctx, diagnose.LayerOf(code) == event.LayerDNS)
		}
		d = diagnose.Diagnose(*res.Outcome, res.Passed, canary)
	}
	ev := event.NewCheck(time.Now())
	m := s.Monitor
	ev.Monitor = event.MonitorRef{ID: m.ID, Name: m.Name, Kind: m.Kind(), Target: secret.RedactURL(m.Endpoint()),
		InsecureTLS: m.InsecureTLS}
	ev.Status, ev.Observed = d.Status, d.Status
	ev.Timings = res.Timings
	if d.Code != "" {
		ev.Cause, ev.Side = d.Cause(), d.Side
	}
	det := &event.Details{HTTPStatus: res.HTTPStatus, Address: res.Address, Family: res.Family}
	if !res.TLSNotAfter.IsZero() {
		ts := event.TS(res.TLSNotAfter)
		det.TLSNotAfter = &ts
	}
	if *det != (event.Details{}) {
		ev.Details = det
	}
	return ev, d
}

func exitFor(d diagnose.Diagnosis) int {
	switch d.Status {
	case event.StatusUp:
		return ExitOK
	case event.StatusDegraded:
		return ExitDegraded
	}
	if code, ok := layerExit[d.Layer]; ok {
		return code
	}
	return ExitInternal
}

var stepOrder = []event.Layer{event.LayerLocal, event.LayerVPN, event.LayerDNS, event.LayerTCP, event.LayerTLS, event.LayerHTTP}

// printHuman renders the layered result for a terminal.
func printHuman(w io.Writer, red *secret.Redactor, s config.Settings, res probe.Result, d diagnose.Diagnosis, ev event.Check) {
	p := func(format string, a ...any) { fmt.Fprint(w, red.Scrub(fmt.Sprintf(format, a...))) }
	if name := ev.Monitor.Name; name != "" && name != s.Monitor.URL && "tcp://"+name != s.Monitor.Target && name != s.Monitor.Target {
		p("%s  %s\n", name, ev.Monitor.Target)
	} else {
		p("%s\n", ev.Monitor.Target)
	}
	passed := map[event.Layer]bool{}
	for _, l := range res.Passed {
		passed[l] = true
	}
	timing := map[event.Layer]*int64{event.LayerDNS: res.Timings.DNS, event.LayerTCP: res.Timings.TCP,
		event.LayerTLS: res.Timings.TLS, event.LayerHTTP: res.Timings.TTFB}
	for _, l := range stepOrder {
		mark, note := "", ""
		switch {
		case d.Code != "" && d.Layer == l && d.Status != event.StatusDegraded:
			mark, note = "FAIL", d.Summary
		case passed[l]:
			mark = " ok "
			if t := timing[l]; t != nil {
				note = fmt.Sprintf("%d ms", *t)
			}
			note = strings.TrimSpace(note + " " + stepDetail(l, res, s))
		default:
			continue
		}
		p("  [%s] %-5s %s\n", mark, l, note)
	}
	status := strings.ToUpper(string(d.Status))
	if d.Code != "" {
		p("%s  %s  (side: %s)\n", status, d.Code, d.Side)
		if d.Status == event.StatusDegraded {
			p("  %s\n", d.Summary)
		}
		p("Hint: %s\n", d.Hint)
	} else {
		p("%s\n", status)
	}
	if res.Timings.Total != nil {
		p("total %d ms\n", *res.Timings.Total)
	}
	if s.Monitor.InsecureTLS {
		p("note: TLS verification is disabled (insecure)\n")
	}
}

func stepDetail(l event.Layer, res probe.Result, s config.Settings) string {
	switch l {
	case event.LayerTCP:
		if res.Address != "" {
			return res.Address + " (" + res.Family + ")"
		}
	case event.LayerTLS:
		if !res.TLSNotAfter.IsZero() {
			return "valid until " + res.TLSNotAfter.UTC().Format("2006-01-02")
		}
	case event.LayerHTTP:
		if res.HTTPStatus != 0 {
			return "status " + strconv.Itoa(res.HTTPStatus)
		}
	case event.LayerLocal:
		return "link up, route ok"
	case event.LayerVPN:
		if s.Monitor.RequiresVPN != nil {
			return s.Monitor.RequiresVPN.Interface + " up"
		}
	}
	return ""
}

// printJSON writes v as one JSON line, scrubbed.
func printJSON(env Env, red *secret.Redactor, v any) int {
	line, err := json.Marshal(v)
	if err != nil {
		return ExitInternal
	}
	if red != nil {
		line = red.ScrubBytes(line)
	}
	fmt.Fprintln(env.Stdout, string(line))
	return ExitOK
}
