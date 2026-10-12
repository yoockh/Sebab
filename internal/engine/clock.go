// Package engine schedules checks, runs the per-monitor state machine with flap
// control, handles local outages and VPN preconditions, decides notifications,
// and emits the event stream. Clocks, checkers and the canary are injected.
package engine

import (
	"sort"
	"sync"
	"time"
)

// Clock abstracts time so tests can drive the scheduler deterministically.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is the subset of *time.Timer the engine uses.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration)
}

// RealClock uses the system clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// NewTimer implements Clock.
func (RealClock) NewTimer(d time.Duration) Timer { return &realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time   { return r.t.C }
func (r *realTimer) Stop() bool            { return r.t.Stop() }
func (r *realTimer) Reset(d time.Duration) { r.t.Reset(d) }

// FakeClock is a manual clock for tests. Advance moves time and fires due
// timers; Jump moves wall time without firing timers (simulating suspend).
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// NewFakeClock starts at t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t} }

// Now implements Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer implements Clock.
func (c *FakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, ch: make(chan time.Time, 1), when: c.now.Add(d), active: true}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves the clock forward by d, firing timers in deadline order.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	for {
		var due []*fakeTimer
		for _, t := range c.timers {
			if t.active && !t.when.After(target) {
				due = append(due, t)
			}
		}
		if len(due) == 0 {
			break
		}
		sort.Slice(due, func(i, j int) bool { return due[i].when.Before(due[j].when) })
		t := due[0]
		if t.when.After(c.now) {
			c.now = t.when
		}
		t.active = false
		select {
		case t.ch <- c.now:
		default:
		}
	}
	c.now = target
	c.mu.Unlock()
}

// Jump moves wall time forward without firing timers, like a suspended laptop
// whose monotonic timers did not run.
func (c *FakeClock) Jump(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, t := range c.timers {
		if t.active {
			t.when = t.when.Add(d)
		}
	}
}

// Pending returns the number of active timers (for test synchronisation).
func (c *FakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if t.active {
			n++
		}
	}
	return n
}

type fakeTimer struct {
	clock  *FakeClock
	ch     chan time.Time
	when   time.Time
	active bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := t.active
	t.active = false
	return was
}

func (t *fakeTimer) Reset(d time.Duration) {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.when = t.clock.now.Add(d)
	t.active = true
}
