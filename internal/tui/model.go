// Package tui is the terminal frontend (`sebab tui`). The model consumes the
// event stream and Render draws it as plain text with optional ANSI colours,
// so both are testable without a terminal.
package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/report"
)

// Monitor is the TUI view of one monitor.
type Monitor struct {
	ID, Name, Target string
	Status           event.Status
	Side             event.Side
	Cause            *event.Cause
	Timings          event.Timings
	Since, Checked   time.Time
	Recent           []int64
	Paused, Muted    bool
	Insecure         bool
}

// Model holds the state rendered by the TUI. Safe for concurrent use.
type Model struct {
	mu        sync.Mutex
	order     []string
	monitors  map[string]*Monitor
	engine    string
	source    string
	summary   event.Summary
	lastError string
	notice    string
	noticeAt  time.Time
	started   bool
}

// NewModel returns an empty model.
func NewModel() *Model { return &Model{monitors: map[string]*Monitor{}} }

// Apply consumes one event line.
func (m *Model) Apply(line []byte) {
	h, err := event.Peek(line)
	if err != nil || h.V != event.Version {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch h.Type {
	case event.TypeHello:
		var e event.Hello
		if json.Unmarshal(line, &e) == nil {
			m.engine, m.source, m.started = e.Engine, e.Source, true
		}
	case event.TypeMonitors:
		var e event.Monitors
		if json.Unmarshal(line, &e) != nil {
			return
		}
		next := map[string]*Monitor{}
		m.order = m.order[:0]
		for _, info := range e.Monitors {
			mon := m.monitors[info.ID]
			if mon == nil {
				mon = &Monitor{ID: info.ID}
			}
			mon.Name, mon.Target, mon.Paused, mon.Muted, mon.Insecure = info.Name, info.Target, info.Paused, info.Muted, info.InsecureTLS
			next[info.ID] = mon
			m.order = append(m.order, info.ID)
		}
		m.monitors = next
	case event.TypeCheck:
		var e event.Check
		if json.Unmarshal(line, &e) != nil {
			return
		}
		mon := m.monitors[e.Monitor.ID]
		if mon == nil {
			mon = &Monitor{ID: e.Monitor.ID, Name: e.Monitor.Name, Target: e.Monitor.Target}
			m.monitors[e.Monitor.ID] = mon
			m.order = append(m.order, e.Monitor.ID)
		}
		mon.Status, mon.Side, mon.Cause, mon.Timings = e.Status, e.Side, e.Cause, e.Timings
		mon.Checked, mon.Recent = e.TS.Time(), e.RecentMS
		mon.Since = time.Time{}
		if e.Since != nil {
			mon.Since = e.Since.Time()
		}
	case event.TypeSummary:
		var e event.Summary
		if json.Unmarshal(line, &e) == nil {
			m.summary = e
		}
	case event.TypeError:
		var e event.Error
		if json.Unmarshal(line, &e) == nil {
			m.lastError = e.Message
		}
	case event.TypeNotice:
		var e event.Notice
		if json.Unmarshal(line, &e) == nil {
			m.notice, m.noticeAt = e.Message, e.TS.Time()
		}
	}
}

// IDs returns the monitor ids in display order.
func (m *Model) IDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.order...)
}

// Style colours text; Plain leaves it unchanged.
type Style func(text string, status event.Status) string

// Plain is a Style without colours (NO_COLOR, tests).
func Plain(text string, _ event.Status) string { return text }

// ANSI colours by status.
func ANSI(text string, s event.Status) string {
	code := map[event.Status]string{event.StatusUp: "32", event.StatusDegraded: "33", event.StatusDown: "31;1",
		event.StatusUnknown: "35", event.StatusPaused: "90"}[s]
	if code == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// View is what to draw.
type View struct {
	Width, Height int
	Selected      int
	Expanded      bool
	Now           time.Time
	Style         Style
	Message       string // transient status line (for example "checking now")
}

// Render draws the model into lines no wider than the view.
func (m *Model) Render(v View) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v.Style == nil {
		v.Style = Plain
	}
	if v.Width < 40 {
		v.Width = 40
	}
	var lines []string
	add := func(s string) { lines = append(lines, clip(s, v.Width)) }
	title := "Sebab"
	if m.started {
		title += fmt.Sprintf("  engine %s (%s)", m.engine, m.source)
	} else {
		title += "  starting the engine..."
	}
	add(title)
	add(m.summaryLine(v))
	if m.summary.Offline {
		add(v.Style("You are offline ("+m.summary.OfflineCause+"): monitors resume when the network is back.", event.StatusUnknown))
	}
	if m.lastError != "" {
		add(v.Style("! "+m.lastError, event.StatusDown))
	}
	add(strings.Repeat("-", min(v.Width, 100)))
	if len(m.order) == 0 && m.started {
		add("No monitors. Add one with: sebab monitor add --name NAME --target URL")
	}
	for i, id := range m.order {
		mon := m.monitors[id]
		marker := "  "
		if i == v.Selected {
			marker = "> "
		}
		status := pad(strings.ToUpper(string(orDash(mon.Status))), 8)
		lat := "-"
		if mon.Timings.Total != nil && mon.Status != event.StatusDown {
			lat = fmt.Sprintf("%d ms", *mon.Timings.Total)
		}
		ago := "never"
		if !mon.Checked.IsZero() {
			ago = agoText(v.Now.Sub(mon.Checked))
		}
		flags := ""
		if mon.Muted {
			flags += " [muted]"
		}
		if mon.Insecure {
			flags += " [insecure]"
		}
		why := ""
		if mon.Cause != nil && mon.Status != event.StatusUp {
			why = "  " + mon.Cause.Summary
		}
		add(fmt.Sprintf("%s%s %-24s %8s %9s%s%s", marker, v.Style(status, mon.Status), clip(mon.Name, 24), lat, ago, flags, why))
		if i == v.Selected && v.Expanded {
			for _, d := range m.details(mon, v) {
				add("      " + d)
			}
		}
	}
	add(strings.Repeat("-", min(v.Width, 100)))
	if v.Message != "" {
		add(v.Message)
	} else if m.notice != "" && v.Now.Sub(m.noticeAt) < 2*time.Minute {
		add("notice: " + m.notice)
	}
	add("up/down select  enter details  r check now  q quit")
	if v.Height > 0 && len(lines) > v.Height {
		lines = append(lines[:v.Height-1], lines[len(lines)-1])
	}
	return strings.Join(lines, "\n")
}

func (m *Model) summaryLine(v View) string {
	s := m.summary
	parts := []string{}
	for _, p := range []struct {
		n int
		s event.Status
	}{{s.Up, event.StatusUp}, {s.Degraded, event.StatusDegraded}, {s.Down, event.StatusDown},
		{s.Unknown, event.StatusUnknown}, {s.Paused, event.StatusPaused}} {
		if p.n > 0 {
			parts = append(parts, v.Style(fmt.Sprintf("%d %s", p.n, p.s), p.s))
		}
	}
	if len(parts) == 0 {
		return "no results yet"
	}
	return strings.Join(parts, "  ")
}

func (m *Model) details(mon *Monitor, v View) []string {
	out := []string{mon.Target}
	if mon.Cause != nil {
		out = append(out, fmt.Sprintf("%s (%s layer, %s side)", mon.Cause.Code, mon.Cause.Layer, orDash(event.Status(mon.Side))),
			mon.Cause.Summary, "hint: "+mon.Cause.Hint)
	}
	if t := report.Timings(mon.Timings, mon.Cause); t != "" {
		out = append(out, "timings: "+t)
	}
	if !mon.Since.IsZero() && mon.Status != event.StatusUp {
		out = append(out, fmt.Sprintf("%s since %s (%s)", mon.Status, mon.Since.Local().Format("15:04"),
			agoText(v.Now.Sub(mon.Since))))
	}
	if len(mon.Recent) > 1 {
		out = append(out, "latency "+spark(mon.Recent))
	}
	return out
}

func spark(values []int64) string {
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

func orDash(s event.Status) event.Status {
	if s == "" {
		return "-"
	}
	return s
}

func agoText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(max(d, 0).Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

func pad(s string, n int) string {
	for utf8.RuneCountInString(s) < n {
		s += " "
	}
	return s
}

// clip shortens s to n visible runes, ignoring ANSI escape sequences.
func clip(s string, n int) string {
	var b strings.Builder
	visible := 0
	inEsc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
			b.WriteRune(r)
		case inEsc:
			b.WriteRune(r)
			if r == 'm' {
				inEsc = false
			}
		case visible < n:
			b.WriteRune(r)
			visible++
		}
	}
	if strings.Contains(s, "\x1b[") && !strings.HasSuffix(b.String(), "\x1b[0m") {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}
