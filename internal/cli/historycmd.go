package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/history"
)

// openHistory opens the history store unless disabled; nil means no history.
func openHistory(rt *runtime, cfg *config.Config) *history.Store {
	if cfg.History.Disabled || rt.paths.StateDir == "" {
		return nil
	}
	h, err := history.Open(filepath.Join(rt.paths.StateDir, "history"), cfg.History.KeepDays, 30)
	if err != nil {
		fmt.Fprintf(rt.env.Stderr, "sebab: history disabled: %v\n", err)
		return nil
	}
	return h
}

// runHistory prints availability, latency percentiles, a sparkline and the
// status changes of one monitor (or all).
func runHistory(_ context.Context, env Env, args []string) int {
	fs := newFlagSet("history", env)
	since := fs.Duration("since", 24*time.Hour, "how far back to look")
	jsonOut := fs.Bool("json", false, "print JSON")
	if code, ok := parseFlags(fs, reorder(args)); !ok {
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
	h := openHistory(rt, cfg)
	if h == nil {
		return errorf(env, "history: disabled or unavailable")
	}
	defer h.Close()
	id := fs.Arg(0)
	from := time.Now().Add(-*since)
	samples, err := h.Samples(id, from, time.Now())
	if err != nil {
		return errorf(env, "history: %v", err)
	}
	trs, err := h.Transitions(id, from)
	if err != nil {
		return errorf(env, "history: %v", err)
	}
	if *jsonOut {
		return printJSON(env, rt.redactor, map[string]any{"stats": history.Summarize(samples), "samples": samples,
			"transitions": trs})
	}
	if len(samples) == 0 {
		fmt.Fprintf(env.Stdout, "No history in the last %s.\n", *since)
		return ExitOK
	}
	st := history.Summarize(samples)
	label := id
	if label == "" {
		label = "all monitors"
	}
	fmt.Fprintf(env.Stdout, "%s, last %s: %d checks, %.2f%% available, p50 %d ms, p95 %d ms\n",
		label, *since, st.Checks, 100*st.Availability, st.P50, st.P95)
	if id != "" {
		var ms []int64
		for _, s := range samples {
			ms = append(ms, s.TotalMS)
		}
		fmt.Fprintf(env.Stdout, "latency %s\n", Sparkline(ms, 60))
	}
	for _, t := range trs {
		fmt.Fprintf(env.Stdout, "%s  %-14s %s -> %s  %s %s\n", time.UnixMilli(t.Time).Format("2006-01-02 15:04:05"),
			t.Monitor, dash(string(t.From)), t.To, t.Code, rt.redactor.Scrub(t.Summary))
	}
	return ExitOK
}

// Sparkline renders values (-1 = failed) as block characters, keeping the
// last width values. Failures show as "x".
func Sparkline(values []int64, width int) string {
	if len(values) > width {
		values = values[len(values)-width:]
	}
	var hi int64
	for _, v := range values {
		hi = max(hi, v)
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, v := range values {
		switch {
		case v < 0:
			b.WriteRune('x')
		case hi == 0:
			b.WriteRune(blocks[0])
		default:
			b.WriteRune(blocks[int(v*int64(len(blocks)-1)/hi)])
		}
	}
	return b.String()
}

// runOverview prints the dashboard summary used by the Noctalia panel: overall
// and per-monitor uptime, a status strip per monitor, and recent status changes.
// It reads the local history directly, so it works with or without an engine.
func runOverview(_ context.Context, env Env, args []string) int {
	fs := newFlagSet("overview", env)
	since := fs.Duration("since", 24*time.Hour, "period to summarise")
	buckets := fs.Int("buckets", 24, "segments in each status strip (1-96)")
	incidents := fs.Int("incidents", 50, "maximum status changes to list")
	_ = fs.Bool("json", true, "print JSON (the only format)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if *buckets < 1 || *buckets > 96 || *since < time.Hour || *since > 30*24*time.Hour {
		return usageError(env, "overview: --buckets must be 1-96 and --since between 1h and 720h")
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	cfg, err := rt.loadConfig()
	if err != nil {
		cfg = &config.Config{}
	}
	h := openHistory(rt, cfg)
	if h == nil {
		return printJSON(env, rt.redactor, history.Overview{V: 1, SinceS: int64(since.Seconds()), Buckets: *buckets,
			Monitors: map[string]history.MonitorOverview{}, Incidents: []history.Transition{}})
	}
	defer h.Close()
	ov, err := h.BuildOverview(time.Now(), *since, *buckets, *incidents)
	if err != nil {
		return errorf(env, "overview: %v", err)
	}
	return printJSON(env, rt.redactor, ov)
}
