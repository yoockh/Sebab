package waybar

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func feed(t *testing.T, a *Aggregator, lines ...string) Output {
	t.Helper()
	var out Output
	for _, l := range lines {
		out, _ = a.Apply([]byte(l))
	}
	return out
}

const hello = `{"v":1,"type":"hello","ts":"2026-10-11T13:00:00Z","engine":"0.1.0","monitors":2,"heartbeat_ms":15000}`
const mons = `{"v":1,"type":"monitors","ts":"2026-10-11T13:00:00Z","monitors":[{"id":"api","name":"Payments API","kind":"http","target":"x","interval_s":60,"paused":false},{"id":"db","name":"DB <prod>","kind":"tcp","target":"y","interval_s":60,"paused":false}]}`

func TestPRDMapping(t *testing.T) {
	a := NewAggregator(nil)
	out := feed(t, a, hello, mons,
		`{"v":1,"type":"check","ts":"x","monitor":{"id":"db","name":"DB <prod>","kind":"tcp"},"status":"up","timings_ms":{},"notify":false}`,
		`{"v":1,"type":"check","ts":"x","monitor":{"id":"api","name":"Payments API","kind":"http"},"status":"down","side":"theirs","cause":{"layer":"tls","code":"TLS_EXPIRED","summary":"TLS certificate expired","hint":"h"},"timings_ms":{},"notify":true}`)
	b, _ := json.Marshal(out)
	want := `{"text":"1","alt":"down","class":["down"],"tooltip":"Payments API: down, TLS certificate expired","percentage":50}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

func TestStatesAndEscaping(t *testing.T) {
	a := NewAggregator(nil)
	if out := a.Render(); out.Alt != "starting" {
		t.Fatalf("starting: %+v", out)
	}
	out := feed(t, a, hello, `{"v":1,"type":"monitors","ts":"x","monitors":[]}`)
	if out.Alt != "empty" {
		t.Fatalf("empty: %+v", out)
	}
	out = feed(t, a, mons, `{"v":1,"type":"check","ts":"x","monitor":{"id":"db","name":"DB <prod>","kind":"tcp"},"status":"degraded","cause":{"layer":"http","code":"HTTP_SLOW","summary":"took 4 s & more","hint":""},"timings_ms":{},"notify":false}`)
	if out.Alt != "degraded" || out.Text != "1" || !strings.Contains(out.Tooltip, "DB &lt;prod&gt;: degraded, took 4 s &amp; more") {
		t.Fatalf("degraded/escaping: %+v", out)
	}
	out = feed(t, a, `{"v":1,"type":"summary","ts":"x","up":0,"degraded":0,"down":0,"unknown":2,"paused":0,"offline":true,"offline_cause":"LOCAL_NO_ROUTE"}`)
	if out.Alt != "offline" || !strings.Contains(out.Tooltip, "LOCAL_NO_ROUTE") {
		t.Fatalf("offline: %+v", out)
	}
	out = feed(t, a, `{"v":1,"type":"summary","ts":"x","up":2,"degraded":0,"down":0,"unknown":0,"paused":0,"offline":false}`,
		`{"v":1,"type":"check","ts":"x","monitor":{"id":"db","name":"DB","kind":"tcp"},"status":"up","timings_ms":{},"notify":false}`,
		`{"v":1,"type":"check","ts":"x","monitor":{"id":"api","name":"API","kind":"http"},"status":"up","timings_ms":{},"notify":false}`)
	if out.Alt != "up" || out.Text != "" || out.Tooltip != "All 2 monitors up" || out.Percentage != 100 {
		t.Fatalf("all up: %+v", out)
	}
}

func TestOnlyChangesAreReported(t *testing.T) {
	a := NewAggregator(nil)
	feed(t, a, hello, mons)
	if _, changed := a.Apply([]byte(`{"v":1,"type":"heartbeat","ts":"x"}`)); changed {
		t.Error("a heartbeat must not produce output")
	}
	if _, changed := a.Apply([]byte(`garbage`)); changed {
		t.Error("garbage must be ignored")
	}
	if _, changed := a.Apply([]byte(`{"v":1,"type":"future","ts":"x"}`)); changed {
		t.Error("unknown types must be ignored")
	}
}

func TestStallDetection(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := NewAggregator(func() time.Time { return now })
	feed(t, a, hello, mons)
	now = now.Add(30 * time.Second)
	if out, _ := a.Tick(); out.Alt == "stalled" {
		t.Fatal("not stalled after 2 intervals")
	}
	now = now.Add(20 * time.Second)
	if out, changed := a.Tick(); out.Alt != "stalled" || !changed {
		t.Fatalf("stalled after 3 intervals: %+v", out)
	}
	if out, _ := a.Apply([]byte(`{"v":1,"type":"heartbeat","ts":"x"}`)); out.Alt == "stalled" {
		t.Fatal("a heartbeat clears the stall")
	}
}
