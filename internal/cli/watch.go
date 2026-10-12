package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/engine"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/filewatch"
	"github.com/yoockh/sebab/internal/history"
	"github.com/yoockh/sebab/internal/report"
	"github.com/yoockh/sebab/internal/secret"
	"github.com/yoockh/sebab/internal/server"
)

var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func runWatch(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("watch", env)
	format := fs.String("format", "jsonl", "output format: jsonl")
	session := fs.String("session", "", "session token, so 'sebab kill-session' can stop this process")
	embedded := fs.Bool("embedded", false, "always run an embedded engine, never attach to a daemon")
	frontendNotifies := fs.Bool("frontend-notifies", true,
		"the program reading this stream displays notifications (false: the engine sends desktop notifications)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if *format != "jsonl" {
		return usageError(env, "watch: unsupported format %q", *format)
	}
	if *session != "" && !sessionPattern.MatchString(*session) {
		return usageError(env, "watch: --session must be letters, digits, '-' or '_'")
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	if *session != "" {
		release := writeSessionPID(rt, *session)
		defer release()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &guardedWriter{w: env.Stdout, onErr: cancel}
	sock := rt.paths.SocketPath()
	if !*embedded && sock != "" && server.Alive(sock) {
		// One engine, many frontends: stream from the running engine.
		if err := server.Subscribe(ctx, sock, out, *frontendNotifies); err == nil || ctx.Err() != nil {
			return ExitOK
		}
		// The engine went away; take over with an embedded one.
	}
	return runEngine(ctx, rt, out, "embedded", sock, *frontendNotifies)
}

// runDaemon runs the shared engine without a frontend (systemd user service).
func runDaemon(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("daemon", env)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	sock := rt.paths.SocketPath()
	if sock == "" {
		return errorf(env, "daemon: XDG_RUNTIME_DIR is unset; run it from your login session (systemd --user)")
	}
	if server.Alive(sock) {
		return errorf(env, "daemon: another sebab engine already serves %s", sock)
	}
	return runEngine(ctx, rt, nil, "daemon", sock, false)
}

// guardedWriter cancels the engine when stdout breaks (the frontend went away).
type guardedWriter struct {
	mu    sync.Mutex
	w     io.Writer
	onErr func()
	dead  bool
}

func (g *guardedWriter) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.dead {
		return 0, io.ErrClosedPipe
	}
	n, err := g.w.Write(p)
	if err != nil {
		g.dead = true
		g.onErr()
	}
	return n, err
}

// runEngine runs an engine until ctx ends: events go to out (if not nil) and to
// socket subscribers; config edits are applied live.
func runEngine(ctx context.Context, rt *runtime, out io.Writer, source, sock string, localNotifies bool) int {
	sinks := multiSink{}
	bc := server.NewBroadcaster(rt.redactor)
	sinks = append(sinks, bc)
	if out != nil {
		enc := event.NewEncoder(out, rt.redactor)
		sinks = append(sinks, engine.SinkFunc(func(ev any) { _ = enc.Encode(ev) }))
	}
	sinks = append(sinks, extraSinks(ctx, rt, bc, localNotifies)...)
	cfg, cfgErr := loadForEngine(rt)
	checker := &engine.ProbeChecker{Prober: rt.prober, Secrets: rt.store, Redactor: rt.redactor,
		OnError: func(ev event.Error) { sinks.Emit(ev) }}
	opts := engine.Options{Clock: rt.clock(), Checker: checker, Canary: rt.canary(cfg.EngineSettings()),
		Network: rt.network, Sink: sinks, Version: versionString(), Source: source}
	if h := openHistory(rt, cfg); h != nil {
		defer h.Close()
		opts.History = h
	}
	if rt.env.hooks != nil && !rt.env.hooks.jitter {
		opts.NoJitter = true
	}
	eng := engine.New(cfg, opts)
	reload := func() error {
		c, err := config.Load(rt.paths.ConfigFile)
		if errors.Is(err, config.ErrNotFound) {
			c, err = &config.Config{}, nil
		}
		if err != nil {
			sinks.Emit(event.NewError(time.Now(), event.ErrConfigInvalid, rt.redactor.Scrub(err.Error())))
			return err
		}
		eng.Reload(c)
		return nil
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if sock != "" {
		srv := &server.Server{Path: sock, Events: bc, Scrub: rt.redactor, Version: versionString(), Source: source,
			Backend: &engineBackend{eng: eng, reload: reload, red: rt.redactor, hist: opts.History}}
		if err := srv.Listen(); err == nil {
			wg.Add(1)
			go func() { defer wg.Done(); srv.Serve(ctx) }()
		} else {
			fmt.Fprintf(rt.env.Stderr, "sebab: control socket unavailable: %v\n", err)
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		filewatch.Watch(ctx, rt.paths.ConfigFile, 300*time.Millisecond, func() { _ = reload() })
	}()
	go func() {
		<-eng.Started()
		if cfgErr != nil {
			sinks.Emit(*cfgErr)
		}
	}()
	wg.Add(1)
	go func() { defer wg.Done(); watchSleep(ctx, rt, eng) }()
	if err := eng.Run(ctx); err != nil {
		return errorf(rt.env, "engine: %v", err)
	}
	return ExitOK
}

// loadForEngine loads the config; problems become an error event and the engine
// starts with what it has so a later fix is picked up live.
func loadForEngine(rt *runtime) (*config.Config, *event.Error) {
	cfg, err := config.Load(rt.paths.ConfigFile)
	switch {
	case err == nil:
		return cfg, nil
	case errors.Is(err, config.ErrNotFound):
		ev := event.NewError(time.Now(), event.ErrConfigMissing,
			"no config yet: add a monitor with 'sebab monitor add' or from the panel")
		return &config.Config{}, &ev
	default:
		ev := event.NewError(time.Now(), event.ErrConfigInvalid, rt.redactor.Scrub(err.Error()))
		return &config.Config{}, &ev
	}
}

type multiSink []engine.Sink

func (m multiSink) Emit(ev any) {
	for _, s := range m {
		s.Emit(ev)
	}
}

// engineBackend adapts the engine to the socket server.
type engineBackend struct {
	eng    *engine.Engine
	reload func() error
	red    *secret.Redactor
	hist   engine.History
}

func (b *engineBackend) Snapshot(ctx context.Context) []any { return b.eng.Snapshot(ctx) }
func (b *engineBackend) Trigger(id string)                  { b.eng.Trigger(id) }
func (b *engineBackend) Reload() error                      { return b.reload() }

func (b *engineBackend) Report(ctx context.Context, id string) (string, error) {
	ev, ok := b.eng.LastCheck(ctx, id)
	if !ok {
		return "", fmt.Errorf("no result for monitor %q yet", id)
	}
	md := report.Markdown(ev, versionString(), b.red)
	if h, ok := b.hist.(*history.Store); ok && h != nil {
		md += report.Timeline(h, id, 24*time.Hour, b.red)
	}
	return md, nil
}

func sessionFile(rt *runtime, session string) string {
	if rt.paths.RuntimeDir == "" {
		return ""
	}
	return filepath.Join(rt.paths.RuntimeDir, "watch-"+session+".pid")
}

func writeSessionPID(rt *runtime, session string) func() {
	path := sessionFile(rt, session)
	if path == "" {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}
	}
	if err := config.WriteFileAtomic(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

// runKillSession stops the watch process started with --session ID. The
// Noctalia service uses it to restart a stalled stream.
func runKillSession(_ context.Context, env Env, args []string) int {
	fs := newFlagSet("kill-session", env)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 || !sessionPattern.MatchString(fs.Arg(0)) {
		return usageError(env, "kill-session: a session token is required")
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	session := fs.Arg(0)
	path := sessionFile(rt, session)
	data, err := os.ReadFile(path)
	if err != nil {
		return ExitOK // already gone
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		_ = os.Remove(path)
		return ExitOK
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || !strings.Contains(string(cmdline), "watch") || !strings.Contains(string(cmdline), session) {
		_ = os.Remove(path) // stale pid file: never signal an unrelated process
		return ExitOK
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return errorf(env, "kill-session: %v", err)
	}
	return ExitOK
}

// runRefresh asks the running engine for an immediate check.
func runRefresh(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("refresh", env)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	sock := rt.paths.SocketPath()
	if sock == "" || !server.Alive(sock) {
		return errorf(env, "refresh: no running engine (start 'sebab watch' or 'sebab daemon')")
	}
	if _, err := server.Call(ctx, sock, server.Request{Cmd: "check", Monitor: fs.Arg(0)}, 2*time.Second); err != nil {
		return errorf(env, "refresh: %v", err)
	}
	return ExitOK
}

// runReport prints the Markdown diagnosis of a monitor. It asks the running
// engine for the latest result, or runs a fresh check with --fresh or when no
// engine runs.
func runReport(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("report", env)
	fresh := fs.Bool("fresh", false, "run a new check instead of using the engine's latest result")
	if code, ok := parseFlags(fs, reorder(args)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(env, "report: monitor id required")
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	sock := rt.paths.SocketPath()
	if !*fresh && sock != "" && server.Alive(sock) {
		resp, err := server.Call(ctx, sock, server.Request{Cmd: "report", Monitor: fs.Arg(0)}, 3*time.Second)
		if err == nil {
			fmt.Fprint(env.Stdout, rt.redactor.Scrub(resp.Markdown))
			return ExitOK
		}
	}
	code := runCheck(ctx, env, []string{"--monitor", fs.Arg(0), "--markdown"})
	if code == ExitUsage || code == ExitInternal {
		return code
	}
	return ExitOK
}
