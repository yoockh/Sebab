// Package waybar turns the Sebab event stream into Waybar custom-module JSON:
// one object per line, emitted only when the rendered state changes.
//
//	{"text":"1","alt":"down","class":["down"],"tooltip":"Payments API: TLS certificate expired","percentage":80}
//
// Waybar parses text and tooltip as Pango markup unless the module sets
// "escape": true, so values are markup-escaped here.
package waybar

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/yoockh/sebab/internal/event"
)

// Output is one Waybar update.
type Output struct {
	Text       string   `json:"text"`
	Alt        string   `json:"alt"`
	Class      []string `json:"class"`
	Tooltip    string   `json:"tooltip"`
	Percentage int      `json:"percentage"`
}

type monitor struct {
	name    string
	status  event.Status
	summary string
}

// Aggregator tracks state from events. Safe for concurrent use.
type Aggregator struct {
	mu        sync.Mutex
	order     []string
	monitors  map[string]*monitor
	offline   bool
	cause     string
	started   bool
	heartbeat time.Duration
	lastBeat  time.Time
	stalled   bool
	lastOut   string
	now       func() time.Time
}

// NewAggregator returns an aggregator using now as its clock.
func NewAggregator(now func() time.Time) *Aggregator {
	if now == nil {
		now = time.Now
	}
	return &Aggregator{monitors: map[string]*monitor{}, heartbeat: 15 * time.Second, now: now, lastBeat: now()}
}

// Apply consumes one event line and reports whether the output changed.
func (a *Aggregator) Apply(line []byte) (Output, bool) {
	h, err := event.Peek(line)
	if err != nil || h.V != event.Version {
		return Output{}, false
	}
	a.mu.Lock()
	a.lastBeat, a.stalled = a.now(), false
	switch h.Type {
	case event.TypeHello:
		var e event.Hello
		if json.Unmarshal(line, &e) == nil && e.HeartbeatMS > 0 {
			a.heartbeat = time.Duration(e.HeartbeatMS) * time.Millisecond
		}
		a.started = true
	case event.TypeMonitors:
		var e event.Monitors
		if json.Unmarshal(line, &e) == nil {
			next := map[string]*monitor{}
			a.order = a.order[:0]
			for _, m := range e.Monitors {
				cur := a.monitors[m.ID]
				if cur == nil {
					cur = &monitor{}
				}
				cur.name = m.Name
				if m.Paused {
					cur.status = event.StatusPaused
				}
				next[m.ID] = cur
				a.order = append(a.order, m.ID)
			}
			a.monitors = next
		}
	case event.TypeCheck:
		var e event.Check
		if json.Unmarshal(line, &e) == nil {
			m := a.monitors[e.Monitor.ID]
			if m == nil {
				m = &monitor{}
				a.monitors[e.Monitor.ID] = m
				a.order = append(a.order, e.Monitor.ID)
			}
			m.name, m.status, m.summary = e.Monitor.Name, e.Status, ""
			if e.Cause != nil {
				m.summary = e.Cause.Summary
			}
		}
	case event.TypeSummary:
		var e event.Summary
		if json.Unmarshal(line, &e) == nil {
			a.offline, a.cause = e.Offline, e.OfflineCause
		}
	}
	a.mu.Unlock()
	return a.changed()
}

// Tick checks for a stalled engine (no event for 3 heartbeat intervals).
func (a *Aggregator) Tick() (Output, bool) {
	a.mu.Lock()
	if a.started && a.now().Sub(a.lastBeat) > 3*a.heartbeat {
		a.stalled = true
	}
	a.mu.Unlock()
	return a.changed()
}

// SetStalled forces the stalled state (the engine stream ended).
func (a *Aggregator) SetStalled() (Output, bool) {
	a.mu.Lock()
	a.stalled = true
	a.mu.Unlock()
	return a.changed()
}

func (a *Aggregator) changed() (Output, bool) {
	out := a.Render()
	b, _ := json.Marshal(out)
	a.mu.Lock()
	defer a.mu.Unlock()
	if string(b) == a.lastOut {
		return out, false
	}
	a.lastOut = string(b)
	return out, true
}

// Render builds the current output.
func (a *Aggregator) Render() Output {
	a.mu.Lock()
	defer a.mu.Unlock()
	esc := html.EscapeString
	switch {
	case a.stalled:
		return Output{Text: "stalled", Alt: "stalled", Class: []string{"stalled"},
			Tooltip: "Sebab engine stalled: no heartbeat for 3 intervals"}
	case !a.started:
		return Output{Alt: "starting", Class: []string{"starting"}, Tooltip: "Sebab: starting the engine"}
	case len(a.order) == 0:
		return Output{Alt: "empty", Class: []string{"empty"}, Tooltip: "Sebab: no monitors. Add one with: sebab monitor add"}
	case a.offline:
		return Output{Text: "offline", Alt: "offline", Class: []string{"offline"},
			Tooltip: esc("You are offline (" + a.cause + "). Monitors resume when the network is back.")}
	}
	counts := map[event.Status]int{}
	var lines []string
	for _, id := range a.order {
		m := a.monitors[id]
		counts[m.status]++
		if m.status != event.StatusUp && m.status != "" {
			line := fmt.Sprintf("%s: %s", m.name, m.status)
			if m.summary != "" {
				line += ", " + m.summary
			}
			lines = append(lines, esc(line))
		}
	}
	total := len(a.order)
	pct := 100 * counts[event.StatusUp] / total
	switch {
	case counts[event.StatusDown] > 0:
		return Output{Text: fmt.Sprint(counts[event.StatusDown]), Alt: "down", Class: []string{"down"},
			Tooltip: strings.Join(lines, "\n"), Percentage: pct}
	case counts[event.StatusDegraded] > 0:
		return Output{Text: fmt.Sprint(counts[event.StatusDegraded]), Alt: "degraded", Class: []string{"degraded"},
			Tooltip: strings.Join(lines, "\n"), Percentage: pct}
	case len(lines) > 0:
		return Output{Alt: "up", Class: []string{"up", "partial"}, Tooltip: strings.Join(lines, "\n"), Percentage: pct}
	}
	tip := fmt.Sprintf("All %d monitors up", total)
	if total == 1 {
		tip = "1 monitor up"
	}
	return Output{Alt: "up", Class: []string{"up"}, Tooltip: tip, Percentage: 100}
}
