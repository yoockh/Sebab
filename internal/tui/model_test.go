package tui

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 11, 13, 2, 0, 0, time.UTC)

func feed(m *Model, lines ...string) {
	for _, l := range lines {
		m.Apply([]byte(l))
	}
}

const (
	hello = `{"v":1,"type":"hello","ts":"2026-10-11T13:00:00Z","engine":"0.1.0","monitors":2,"heartbeat_ms":15000,"source":"daemon"}`
	mons  = `{"v":1,"type":"monitors","ts":"2026-10-11T13:00:00Z","monitors":[{"id":"api","name":"Payments API","kind":"http","target":"https://api.example.com/health","interval_s":60,"paused":false,"muted":true},{"id":"db","name":"DB","kind":"tcp","target":"tcp://db.example:5432","interval_s":60,"paused":false}]}`
	down  = `{"v":1,"type":"check","ts":"2026-10-11T13:01:00Z","monitor":{"id":"api","name":"Payments API","kind":"http"},"status":"down","side":"theirs","cause":{"layer":"tls","code":"TLS_EXPIRED","summary":"TLS certificate expired 2 days ago","hint":"Renew it."},"timings_ms":{"dns":12,"tcp":38,"tls":null,"ttfb":null,"total":51},"since":"2026-10-11T12:59:00Z","recent_ms":[90,100,-1],"notify":true}`
	up    = `{"v":1,"type":"check","ts":"2026-10-11T13:01:30Z","monitor":{"id":"db","name":"DB","kind":"tcp"},"status":"up","timings_ms":{"dns":1,"tcp":2,"tls":null,"ttfb":null,"total":3},"notify":false}`
	sum   = `{"v":1,"type":"summary","ts":"2026-10-11T13:01:30Z","up":1,"degraded":0,"down":1,"unknown":0,"paused":0,"offline":false}`
)

func TestRenderList(t *testing.T) {
	m := NewModel()
	feed(m, hello, mons, down, up, sum)
	out := m.Render(View{Width: 120, Now: now})
	for _, want := range []string{"engine 0.1.0 (daemon)", "1 up  1 down", "> DOWN", "Payments API",
		"TLS certificate expired 2 days ago", "[muted]", "  UP       DB", "3 ms", "30s ago", "q quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if ids := m.IDs(); len(ids) != 2 || ids[0] != "api" {
		t.Errorf("ids %v", ids)
	}
}

func TestRenderDetailsAndSelection(t *testing.T) {
	m := NewModel()
	feed(m, hello, mons, down, up, sum)
	out := m.Render(View{Width: 120, Now: now, Selected: 0, Expanded: true})
	for _, want := range []string{"https://api.example.com/health", "TLS_EXPIRED (tls layer, theirs side)", "hint: Renew it.",
		"timings: DNS 12 ms, TCP 38 ms, TLS failed, total 51 ms", "down since", "(3m ago)", "latency ▇█x"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	out = m.Render(View{Width: 120, Now: now, Selected: 1})
	if !strings.Contains(out, "> UP") {
		t.Errorf("selection marker on the second row:\n%s", out)
	}
}

func TestOfflineErrorAndEmpty(t *testing.T) {
	m := NewModel()
	if out := m.Render(View{Width: 80, Now: now}); !strings.Contains(out, "starting the engine") {
		t.Errorf("starting: %s", out)
	}
	feed(m, hello, `{"v":1,"type":"monitors","ts":"x","monitors":[]}`)
	if out := m.Render(View{Width: 80, Now: now}); !strings.Contains(out, "No monitors") {
		t.Errorf("empty: %s", out)
	}
	feed(m, `{"v":1,"type":"summary","ts":"x","up":0,"degraded":0,"down":0,"unknown":2,"paused":0,"offline":true,"offline_cause":"LOCAL_NO_ROUTE"}`,
		`{"v":1,"type":"error","ts":"x","code":"CONFIG_INVALID","message":"monitor 'x': interval below 30s"}`)
	out := m.Render(View{Width: 80, Now: now})
	if !strings.Contains(out, "You are offline (LOCAL_NO_ROUTE)") || !strings.Contains(out, "! monitor 'x': interval below 30s") {
		t.Errorf("offline/error:\n%s", out)
	}
}

func TestWidthAndHeightLimits(t *testing.T) {
	m := NewModel()
	feed(m, hello, mons, down, up, sum)
	out := m.Render(View{Width: 50, Height: 6, Now: now, Style: ANSI})
	lines := strings.Split(out, "\n")
	if len(lines) != 6 || !strings.Contains(lines[5], "q quit") {
		t.Fatalf("height limit keeps the help line:\n%s", out)
	}
	for _, l := range lines {
		if n := len([]rune(stripANSI(l))); n > 50 {
			t.Errorf("line wider than 50 (%d): %q", n, l)
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			in = true
		case in:
			if r == 'm' {
				in = false
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestDecodeKeys(t *testing.T) {
	got := DecodeKeys([]byte("\x1b[A\x1b[Bjk\rrq\x03"))
	want := []Key{KeyUp, KeyDown, KeyDown, KeyUp, KeyEnter, KeyRefresh, KeyQuit, KeyQuit}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v", got)
		}
	}
}
