package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/yoockh/sebab/internal/channels"
)

// runNotifyTest sends one test notification to the desktop and to every
// configured [[notify]] channel, and reports each result.
func runNotifyTest(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("notify-test", env)
	noDesktop := fs.Bool("no-desktop", false, "skip the desktop notification")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	cfg, err := rt.loadConfig()
	if err != nil {
		return fail(env, false, rt.redactor, ExitUsage, validationProblems(err)...)
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	title, body := "Sebab test notification", "If you can read this, Sebab notifications reach you."
	failed := 0
	if !*noDesktop {
		if err := rt.desktopSender(ctx)(ctx, title, body, "normal"); err != nil {
			failed++
			fmt.Fprintf(env.Stdout, "desktop: failed: %s\n", rt.redactor.Scrub(err.Error()))
		} else {
			fmt.Fprintln(env.Stdout, "desktop: sent")
		}
	}
	if len(cfg.Notify) == 0 {
		fmt.Fprintln(env.Stdout, "no [[notify]] channels configured")
	}
	d := channels.NewDispatcher(ctx, nil, rt.store, rt.redactor, nil)
	msg := channels.Message{Title: title, Body: body, Urgency: "normal", Kind: "test", Source: "sebab", Version: 1,
		ID: fmt.Sprintf("test:%d", time.Now().UnixMilli()), Time: time.Now().UTC().Format(time.RFC3339)}
	for _, ch := range cfg.Notify {
		if err := d.Send(ctx, ch, msg); err != nil {
			failed++
			fmt.Fprintf(env.Stdout, "%s: failed: %s\n", ch.Type, rt.redactor.Scrub(err.Error()))
			continue
		}
		fmt.Fprintf(env.Stdout, "%s: sent\n", ch.Type)
	}
	if failed > 0 {
		return ExitDegraded
	}
	return ExitOK
}

// desktopSender returns the D-Bus notifier (or the test hook).
func (rt *runtime) desktopSender(ctx context.Context) notifyFunc {
	if rt.env.hooks != nil && rt.env.hooks.notify != nil {
		return rt.env.hooks.notify
	}
	return realDesktopSender(ctx)
}
