package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/engine"
	"github.com/yoockh/sebab/internal/probe"
	"github.com/yoockh/sebab/internal/secret"
	"github.com/yoockh/sebab/internal/server"
)

func runMonitor(ctx context.Context, env Env, args []string) int {
	if len(args) == 0 {
		return usageError(env, "monitor: subcommand required: add, list, show, edit, rm, pause, resume, mute, unmute")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return monitorAdd(ctx, env, rest)
	case "list", "ls":
		return monitorList(env, rest)
	case "show":
		return monitorShow(env, rest)
	case "edit":
		return monitorEdit(ctx, env, rest)
	case "rm", "remove", "delete":
		return monitorRemove(env, rest)
	case "pause":
		return monitorSetPaused(env, rest, true)
	case "resume", "unpause":
		return monitorSetPaused(env, rest, false)
	case "mute":
		return monitorSetFlag(env, rest, "mute", func(m *config.Monitor) { m.Muted = true })
	case "unmute":
		return monitorSetFlag(env, rest, "unmute", func(m *config.Monitor) { m.Muted = false })
	}
	return usageError(env, "monitor: unknown subcommand %q", sub)
}

// result is the --json answer of mutating monitor commands (read by the panel).
type result struct {
	OK      bool     `json:"ok"`
	ID      string   `json:"id,omitempty"`
	Errors  []string `json:"errors,omitempty"`
	Backend string   `json:"secret_backend,omitempty"`
	Warning string   `json:"warning,omitempty"`
}

func fail(env Env, jsonOut bool, red *secret.Redactor, code int, problems ...string) int {
	if jsonOut {
		printJSON(env, red, result{Errors: problems})
		return code
	}
	for _, p := range problems {
		fmt.Fprintf(env.Stderr, "sebab: %s\n", red.Scrub(p))
	}
	return code
}

func validationProblems(err error) []string {
	var ve *config.ValidationError
	if errors.As(err, &ve) {
		return ve.Problems
	}
	return []string{err.Error()}
}

func monitorAdd(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("monitor add", env)
	var f monitorFlags
	f.register(fs, false)
	noTest := fs.Bool("no-test", false, "save without running a test check first")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	cfg, err := rt.loadConfig()
	if err != nil {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, validationProblems(err)...)
	}
	set := visited(fs)
	if !set["name"] || strings.TrimSpace(f.name) == "" {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, "--name is required")
	}
	if !set["target"] {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, "--target is required (a URL or host:port)")
	}
	var m config.Monitor
	if err := f.applyFlags(&m, set); err != nil {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, err.Error())
	}
	if m.ID == "" {
		m.ID = cfg.UniqueID(m.Name)
	}
	value, has, err := f.secretValue(rt)
	if err != nil {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, err.Error())
	}
	if m.Auth != nil && m.Auth.Secret == "" {
		m.Auth.Secret = m.ID + "-token"
	}
	if m.Auth != nil && has {
		addSecret(rt.redactor, m.Auth, value)
	}
	if err := cfg.Add(m); err != nil {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, validationProblems(err)...)
	}
	if !*noTest && !f.jsonOut {
		testBeforeSave(ctx, rt, cfg, m, value, has)
	}
	backend, warning, code := storeSecret(rt, m, value, has, f.secretBknd)
	if code != ExitOK {
		return code
	}
	if err := cfg.Save(rt.paths.ConfigFile); err != nil {
		return fail(env, f.jsonOut, rt.redactor, ExitInternal, validationProblems(err)...)
	}
	notifyEngine(rt)
	if f.jsonOut {
		return printJSON(env, rt.redactor, result{OK: true, ID: m.ID, Backend: backend, Warning: warning})
	}
	fmt.Fprintf(env.Stdout, "Added monitor '%s' to %s.\n", m.ID, rt.paths.ConfigFile)
	return ExitOK
}

// testBeforeSave runs one check and prints the diagnosis (Story 1).
func testBeforeSave(ctx context.Context, rt *runtime, cfg *config.Config, m config.Monitor, value string, has bool) {
	s := cfg.Resolve(m)
	spec, _ := engine.BuildSpec(s, rt.store, rt.redactor)
	if has && m.Auth != nil {
		spec.Auth = &probe.Credentials{Type: m.Auth.Type, Header: m.Auth.Header, Username: m.Auth.Username, Secret: value}
	}
	res := rt.prober.Run(ctx, spec)
	ev, d := buildCheckEvent(ctx, rt, cfg, s, res, false)
	fmt.Fprintln(rt.env.Stdout, "Test check:")
	printHuman(rt.env.Stdout, rt.redactor, s, res, d, ev)
	fmt.Fprintln(rt.env.Stdout)
}

// storeSecret saves a handed-off secret under the monitor's reference.
func storeSecret(rt *runtime, m config.Monitor, value string, has bool, backend string) (string, string, int) {
	if m.Auth == nil || !has {
		return "", "", ExitOK
	}
	if secret.IsEnvRef(m.Auth.Secret) {
		return "", "", fail(rt.env, false, rt.redactor, ExitUsage, "an env: reference reads the environment; do not pass a secret value")
	}
	used, err := rt.store.Set(m.Auth.Secret, value, backend)
	if err != nil {
		return "", "", fail(rt.env, false, rt.redactor, ExitInternal, "store secret: "+err.Error())
	}
	warning := ""
	if used == secret.BackendFile {
		warning = fileFallbackWarning(rt)
		fmt.Fprintln(rt.env.Stderr, "sebab: warning: "+warning)
	}
	return used, warning, ExitOK
}

func fileFallbackWarning(rt *runtime) string {
	return fmt.Sprintf("no Secret Service keyring is running, so the secret was stored in %s (mode 0600). "+
		"Run 'sebab doctor' for how to set up a keyring.", rt.paths.SecretsFile())
}

// notifyEngine asks a running engine to reload now (the file watcher would
// notice within a second anyway).
func notifyEngine(rt *runtime) {
	if p := rt.paths.SocketPath(); p != "" {
		_, _ = server.Call(context.Background(), p, server.Request{Cmd: "reload"}, time.Second)
	}
}

func monitorList(env Env, args []string) int {
	fs := newFlagSet("monitor list", env)
	jsonOut := fs.Bool("json", false, "print JSON")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	cfg, err := rt.loadConfig()
	if err != nil {
		return fail(env, *jsonOut, rt.redactor, ExitUsage, validationProblems(err)...)
	}
	infos := make([]any, 0, len(cfg.Monitors))
	for _, s := range cfg.ResolveAll() {
		infos = append(infos, monitorView(s, false))
	}
	if *jsonOut {
		return printJSON(env, rt.redactor, infos)
	}
	if len(cfg.Monitors) == 0 {
		fmt.Fprintf(env.Stdout, "No monitors yet. Add one with: sebab monitor add --name NAME --target URL\n")
		return ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tTARGET\tINTERVAL\tAUTH\tVPN\tPAUSED")
	for _, s := range cfg.ResolveAll() {
		v := monitorView(s, false)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%v\n", v.ID, v.Name, v.Target, s.Interval, dash(v.Auth), dash(v.VPN), v.Paused)
	}
	tw.Flush()
	return ExitOK
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// view is the JSON shape of one monitor for frontends (never a secret value).
type view struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	Target           string   `json:"target"`
	IntervalS        int      `json:"interval_s"`
	TimeoutS         float64  `json:"timeout_s"`
	ExpectStatus     []int    `json:"expect_status"`
	Paused           bool     `json:"paused"`
	Muted            bool     `json:"muted"`
	Auth             string   `json:"auth,omitempty"`
	AuthSecret       string   `json:"auth_secret,omitempty"` // the reference name, not the value
	AuthHeader       string   `json:"auth_header,omitempty"`
	AuthUsername     string   `json:"auth_username,omitempty"`
	VPN              string   `json:"vpn,omitempty"`
	InsecureTLS      bool     `json:"insecure_tls"`
	CAFile           string   `json:"ca_file,omitempty"`
	FollowRedirects  bool     `json:"follow_redirects"`
	AllowedRedirects []string `json:"allowed_redirect_hosts,omitempty"`
}

func monitorView(s config.Settings, raw bool) view {
	m := s.Monitor
	target := secret.RedactURL(m.Endpoint())
	if raw {
		target = m.Endpoint()
	}
	v := view{ID: m.ID, Name: m.Name, Kind: m.Kind(), Target: target, IntervalS: int(s.Interval.Seconds()),
		TimeoutS: s.Timeout.Seconds(), ExpectStatus: m.ExpectStatus, Paused: m.Paused, Muted: m.Muted, InsecureTLS: m.InsecureTLS,
		CAFile: m.CAFile, FollowRedirects: s.FollowRedirects, AllowedRedirects: m.AllowedRedirectHosts}
	if v.ExpectStatus == nil {
		v.ExpectStatus = []int{}
	}
	if m.Auth != nil {
		v.Auth, v.AuthSecret, v.AuthHeader, v.AuthUsername = m.Auth.Type, m.Auth.Secret, m.Auth.Header, m.Auth.Username
	}
	if m.RequiresVPN != nil {
		v.VPN = m.RequiresVPN.Interface
	}
	return v
}

func monitorShow(env Env, args []string) int {
	fs := newFlagSet("monitor show", env)
	raw := fs.Bool("raw", false, "show the target unredacted (for editing)")
	_ = fs.Bool("json", true, "print JSON (default)")
	if code, ok := parseFlags(fs, reorder(args)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(env, "monitor show: monitor id required")
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	cfg, err := rt.loadConfig()
	if err != nil {
		return fail(env, true, rt.redactor, ExitUsage, validationProblems(err)...)
	}
	i := cfg.Find(fs.Arg(0))
	if i < 0 {
		return fail(env, true, rt.redactor, ExitUsage, fmt.Sprintf("monitor %q not found", fs.Arg(0)))
	}
	return printJSON(env, nil, monitorView(cfg.Resolve(cfg.Monitors[i]), *raw))
}

func monitorEdit(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("monitor edit", env)
	var f monitorFlags
	f.register(fs, false)
	noTest := fs.Bool("no-test", false, "save without running a test check first")
	if code, ok := parseFlags(fs, reorder(args)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(env, "monitor edit: monitor id required")
	}
	id := fs.Arg(0)
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	cfg, err := rt.loadConfig()
	if err != nil {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, validationProblems(err)...)
	}
	i := cfg.Find(id)
	if i < 0 {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, fmt.Sprintf("monitor %q not found", id))
	}
	m := cfg.Monitors[i]
	if m.Auth != nil {
		a := *m.Auth
		m.Auth = &a
	}
	set := visited(fs)
	if err := f.applyFlags(&m, set); err != nil {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, err.Error())
	}
	value, has, err := f.secretValue(rt)
	if err != nil {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, err.Error())
	}
	if m.Auth != nil && m.Auth.Secret == "" {
		m.Auth.Secret = m.ID + "-token"
	}
	if m.Auth != nil && has {
		addSecret(rt.redactor, m.Auth, value)
	}
	if err := cfg.Replace(id, m); err != nil {
		return fail(env, f.jsonOut, rt.redactor, ExitUsage, validationProblems(err)...)
	}
	if !*noTest && !f.jsonOut {
		testBeforeSave(ctx, rt, cfg, m, value, has)
	}
	backend, warning, code := storeSecret(rt, m, value, has, f.secretBknd)
	if code != ExitOK {
		return code
	}
	if err := cfg.Save(rt.paths.ConfigFile); err != nil {
		return fail(env, f.jsonOut, rt.redactor, ExitInternal, validationProblems(err)...)
	}
	notifyEngine(rt)
	if f.jsonOut {
		return printJSON(env, rt.redactor, result{OK: true, ID: m.ID, Backend: backend, Warning: warning})
	}
	fmt.Fprintf(env.Stdout, "Updated monitor '%s'.\n", m.ID)
	return ExitOK
}

func monitorRemove(env Env, args []string) int {
	fs := newFlagSet("monitor rm", env)
	jsonOut := fs.Bool("json", false, "print JSON")
	if code, ok := parseFlags(fs, reorder(args)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(env, "monitor rm: monitor id required")
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	cfg, err := rt.loadConfig()
	if err != nil {
		return fail(env, *jsonOut, rt.redactor, ExitUsage, validationProblems(err)...)
	}
	id := fs.Arg(0)
	i := cfg.Find(id)
	if i < 0 {
		return fail(env, *jsonOut, rt.redactor, ExitUsage, fmt.Sprintf("monitor %q not found", id))
	}
	removed := cfg.Monitors[i]
	_ = cfg.Remove(id)
	if err := cfg.Save(rt.paths.ConfigFile); err != nil {
		return fail(env, *jsonOut, rt.redactor, ExitInternal, validationProblems(err)...)
	}
	notifyEngine(rt)
	if *jsonOut {
		return printJSON(env, nil, result{OK: true, ID: id})
	}
	fmt.Fprintf(env.Stdout, "Removed monitor '%s'.\n", id)
	if removed.Auth != nil && !secret.IsEnvRef(removed.Auth.Secret) {
		fmt.Fprintf(env.Stdout, "Its secret '%s' was kept; remove it with: sebab secret rm %s\n",
			removed.Auth.Secret, removed.Auth.Secret)
	}
	return ExitOK
}

func monitorSetPaused(env Env, args []string, paused bool) int {
	name := "resume"
	if paused {
		name = "pause"
	}
	return monitorSetFlag(env, args, name, func(m *config.Monitor) { m.Paused = paused })
}

// monitorSetFlag applies a small change to one monitor (pause, mute, ...).
func monitorSetFlag(env Env, args []string, name string, apply func(*config.Monitor)) int {
	fs := newFlagSet("monitor "+name, env)
	jsonOut := fs.Bool("json", false, "print JSON")
	if code, ok := parseFlags(fs, reorder(args)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(env, "monitor %s: monitor id required", name)
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	cfg, err := rt.loadConfig()
	if err != nil {
		return fail(env, *jsonOut, rt.redactor, ExitUsage, validationProblems(err)...)
	}
	id := fs.Arg(0)
	i := cfg.Find(id)
	if i < 0 {
		return fail(env, *jsonOut, rt.redactor, ExitUsage, fmt.Sprintf("monitor %q not found", id))
	}
	apply(&cfg.Monitors[i])
	if err := cfg.Save(rt.paths.ConfigFile); err != nil {
		return fail(env, *jsonOut, rt.redactor, ExitInternal, validationProblems(err)...)
	}
	notifyEngine(rt)
	if *jsonOut {
		return printJSON(env, nil, result{OK: true, ID: id})
	}
	fmt.Fprintf(env.Stdout, "Monitor '%s': %s done.\n", id, name)
	return ExitOK
}
