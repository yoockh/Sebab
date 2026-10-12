package engine

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/diagnose"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/probe"
)

// Checker runs one layered check for a monitor.
type Checker interface {
	Check(ctx context.Context, s config.Settings) probe.Result
}

// Canary decides whether the local side is fine after a failure.
type Canary interface {
	Check(ctx context.Context, withDNS bool) diagnose.Canary
	Invalidate()
}

// maxJitter bounds the random delay before a monitor's first check, so monitors
// added together do not all fire at once.
const maxJitter = 10 * time.Second

// maxBackoff caps a Retry-After backoff.
const maxBackoff = time.Hour

// observation is what a worker reports after one check.
type observation struct {
	id       string
	gen      int
	result   probe.Result
	diag     diagnose.Diagnosis
	skipped  bool // suspend guard skipped this cycle
	finished time.Time
}

type worker struct {
	id       string
	gen      int
	settings config.Settings
	trigger  chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
}

// runWorker schedules checks for one monitor until ctx ends.
func (e *Engine) runWorker(ctx context.Context, w *worker, firstDelay time.Duration) {
	defer close(w.done)
	interval := w.settings.Interval
	timer := e.clock.NewTimer(firstDelay)
	defer timer.Stop()
	lastWall := e.clock.Now().Round(0)
	expected := firstDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C():
		case <-w.trigger:
			timer.Stop()
		}
		now := e.clock.Now().Round(0)
		gap := now.Sub(lastWall)
		lastWall = now
		if gap > expected+2*interval && expected > 0 {
			// Wall clock jumped (suspend/resume): skip one cycle so the network
			// has time to come back before we judge anything.
			if !e.send(ctx, observation{id: w.id, gen: w.gen, skipped: true}) {
				return
			}
			expected = interval
			timer.Reset(interval)
			continue
		}
		if e.isOffline() || e.paused() {
			expected = interval
			timer.Reset(interval)
			continue
		}
		obs := e.observe(ctx, w)
		if obs.result.Canceled {
			return
		}
		if !e.send(ctx, obs) {
			return
		}
		next := interval
		if ra := obs.result.Outcome; ra != nil && ra.Facts.RetryAfter > next {
			next = min(ra.Facts.RetryAfter, maxBackoff)
		}
		expected = next
		lastWall = e.clock.Now().Round(0)
		timer.Reset(next)
	}
}

// observe runs the check and, on a remote failure, the canary.
func (e *Engine) observe(ctx context.Context, w *worker) observation {
	res := e.checker.Check(ctx, w.settings)
	obs := observation{id: w.id, gen: w.gen, result: res, finished: e.clock.Now()}
	if res.Outcome == nil {
		obs.diag = diagnose.Diagnosis{Status: event.StatusUp}
		return obs
	}
	var canary diagnose.Canary
	code := res.Outcome.Code
	if res.Failed() && !diagnose.IsLocal(code) && code != diagnose.VPNDown && e.canary != nil {
		canary = e.canary.Check(ctx, diagnose.LayerOf(code) == event.LayerDNS)
	} else if e.canary == nil {
		canary = diagnose.Canary{Disabled: true}
	}
	obs.diag = diagnose.Diagnose(*res.Outcome, res.Passed, canary)
	return obs
}

func (e *Engine) send(ctx context.Context, obs observation) bool {
	select {
	case e.obsCh <- obs:
		return true
	case <-ctx.Done():
		return false
	}
}

func jitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(limit)))
}
