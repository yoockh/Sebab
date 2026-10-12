package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/yoockh/sebab/internal/engine"
	"github.com/yoockh/sebab/internal/event"
	linuxplat "github.com/yoockh/sebab/internal/platform/linux"
	"github.com/yoockh/sebab/internal/secret"
	"github.com/yoockh/sebab/internal/server"
)

// notifyFunc displays one notification.
type notifyFunc func(ctx context.Context, title, body, urgency string) error

// desktopSink sends the engine's notifications over D-Bus when nobody else
// displays them: not the local consumer (localNotifies) and no attached
// frontend that declared it shows notifications. That rule is what keeps a
// Noctalia user from getting each alert twice.
type desktopSink struct {
	send          notifyFunc
	bc            *server.Broadcaster
	red           *secret.Redactor
	localNotifies bool
	queue         chan event.Notification
}

func newDesktopSink(ctx context.Context, send notifyFunc, bc *server.Broadcaster, red *secret.Redactor,
	localNotifies bool, logf func(string, ...any)) *desktopSink {
	s := &desktopSink{send: send, bc: bc, red: red, localNotifies: localNotifies, queue: make(chan event.Notification, 32)}
	go func() {
		failed := false
		for {
			select {
			case <-ctx.Done():
				return
			case n := <-s.queue:
				cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := s.send(cctx, red.Scrub(n.Title), red.Scrub(n.Body), n.Urgency)
				cancel()
				if err != nil && !failed {
					logf("desktop notifications unavailable: %v", err)
				}
				failed = err != nil
			}
		}
	}()
	return s
}

// Emit implements engine.Sink. It never blocks the engine.
func (s *desktopSink) Emit(ev any) {
	var n *event.Notification
	switch e := ev.(type) {
	case event.Check:
		if e.Notify {
			n = e.Notification
		}
	case event.Notice:
		if e.Notify {
			n = e.Notification
		}
	}
	if n == nil || s.localNotifies || s.bc.HasNotifyingSubscriber() {
		return
	}
	select {
	case s.queue <- *n:
	default: // a burst beyond 32 pending notifications is dropped
	}
}

// extraSinks returns platform sinks for an engine.
func extraSinks(ctx context.Context, rt *runtime, bc *server.Broadcaster, localNotifies bool) []engine.Sink {
	send := rt.desktopSender(ctx)
	logf := func(format string, a ...any) { fmt.Fprintf(rt.env.Stderr, "sebab: "+format+"\n", a...) }
	return []engine.Sink{newDesktopSink(ctx, send, bc, rt.redactor, localNotifies, logf)}
}

// realDesktopSender sends through org.freedesktop.Notifications until ctx ends.
func realDesktopSender(ctx context.Context) notifyFunc {
	n := &linuxplat.Notifier{}
	go func() { <-ctx.Done(); n.Close() }()
	return func(ctx context.Context, title, body, urgency string) error {
		_, err := n.Notify(ctx, title, body, urgency)
		return err
	}
}

// watchSleep forwards logind suspend/resume to the engine. Without a system
// bus (containers, tests) it silently does nothing; the wall-clock jump guard
// still catches a resume.
func watchSleep(ctx context.Context, rt *runtime, eng *engine.Engine) {
	if rt.env.hooks != nil && rt.env.hooks.noSleepWatch {
		return
	}
	_ = linuxplat.WatchSleep(ctx, "", eng.SetSleeping)
}
