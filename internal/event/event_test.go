package event

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

var fixed = time.Date(2026, 10, 11, 13, 1, 0, 0, time.UTC)

func goldenEvents() map[string]any {
	since := TS(fixed.Add(-2 * time.Minute))
	check := NewCheck(fixed)
	check.Monitor = MonitorRef{ID: "payments-api", Name: "Payments API", Kind: "http", Target: "https://api.example.com/health"}
	check.Status = StatusDown
	check.Observed = StatusDown
	check.Side = SideTheirs
	check.Cause = &Cause{Layer: LayerTLS, Code: "TLS_EXPIRED", Summary: "TLS certificate expired 2 days ago",
		Hint: "Renew the certificate on the server. DNS and TCP are fine."}
	check.Timings = Timings{DNS: MS(12 * time.Millisecond), TCP: MS(38 * time.Millisecond), Total: MS(51 * time.Millisecond)}
	check.Since = &since
	check.Notify = true
	check.Notification = &Notification{ID: "payments-api:down:TLS_EXPIRED:1760187660", Title: "Payments API is down",
		Body: "TLS certificate expired 2 days ago. DNS and TCP fine.", Urgency: "critical"}

	up := NewCheck(fixed)
	up.Monitor = MonitorRef{ID: "web", Name: "Web", Kind: "http", Target: "https://www.example.com/"}
	up.Status = StatusUp
	up.Observed = StatusUp
	up.Timings = Timings{DNS: MS(3 * time.Millisecond), TCP: MS(20 * time.Millisecond), TLS: MS(41 * time.Millisecond),
		TTFB: MS(90 * time.Millisecond), Total: MS(95 * time.Millisecond)}
	up.Details = &Details{HTTPStatus: 200, Address: "192.0.2.10:443", Family: "ipv4"}

	sum := NewSummary(fixed)
	sum.Up, sum.Degraded, sum.Down = 5, 1, 1

	notice := NewNotice(fixed, NoticeOffline, "You are offline: no default route. 7 monitors paused.")
	notice.Notify = true
	notice.Notification = &Notification{ID: "offline:1760187660", Title: "You are offline",
		Body: "No default route. 7 monitors paused.", Urgency: "critical"}

	return map[string]any{
		"hello":     NewHello(fixed, "0.1.0", 7, 15*time.Second, "embedded"),
		"check":     check,
		"check_up":  up,
		"summary":   sum,
		"heartbeat": NewHeartbeat(fixed),
		"error":     NewError(fixed, ErrConfigInvalid, "monitor 'x': interval below 30s"),
		"notice":    notice,
		"monitors": NewMonitors(fixed, []MonitorInfo{{ID: "staging-db", Name: "Staging DB", Kind: "tcp",
			Target: "tcp://db.staging.example:5432", IntervalS: 60}}),
	}
}

func TestGolden(t *testing.T) {
	for name, ev := range goldenEvents() {
		t.Run(name, func(t *testing.T) {
			got, err := Marshal(ev, nil)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update): %v", err)
			}
			if !bytes.Equal(bytes.TrimSpace(want), got) {
				t.Errorf("mismatch\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// The PRD draft lines must decode into the Go types, and unknown fields must be ignored.
func TestDecodePRDExamples(t *testing.T) {
	lines := []string{
		`{"v":1,"type":"hello","ts":"2026-10-11T13:00:00Z","engine":"0.1.0","monitors":7}`,
		`{"v":1,"type":"check","ts":"2026-10-11T13:01:00Z","monitor":{"id":"payments-api","name":"Payments API","kind":"http"},"status":"down","side":"theirs","cause":{"layer":"tls","code":"TLS_EXPIRED","summary":"TLS certificate expired 2 days ago","hint":"Renew the certificate on the server. DNS and TCP are fine."},"timings_ms":{"dns":12,"tcp":38,"tls":null,"ttfb":null,"total":51},"since":"2026-10-11T12:59:00Z","notify":true,"future_field":{"x":1}}`,
		`{"v":1,"type":"summary","ts":"2026-10-11T13:01:00Z","up":5,"degraded":1,"down":1,"unknown":0,"paused":0,"offline":false}`,
		`{"v":1,"type":"heartbeat","ts":"2026-10-11T13:01:30Z"}`,
		`{"v":1,"type":"error","ts":"2026-10-11T13:01:30Z","code":"CONFIG_INVALID","message":"monitor 'x': interval below 30s"}`,
	}
	targets := map[Type]func() any{
		TypeHello: func() any { return &Hello{} }, TypeCheck: func() any { return &Check{} },
		TypeSummary: func() any { return &Summary{} }, TypeHeartbeat: func() any { return &Heartbeat{} },
		TypeError: func() any { return &Error{} },
	}
	for _, line := range lines {
		h, err := Peek([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if h.V != Version {
			t.Errorf("version %d", h.V)
		}
		v := targets[h.Type]()
		if err := json.Unmarshal([]byte(line), v); err != nil {
			t.Errorf("%s: %v", h.Type, err)
		}
	}
	var c Check
	_ = json.Unmarshal([]byte(lines[1]), &c)
	if c.Cause == nil || c.Cause.Code != "TLS_EXPIRED" || c.Timings.TLS != nil || *c.Timings.DNS != 12 {
		t.Errorf("decoded check wrong: %+v", c)
	}
}

func TestTimingsNullEncoding(t *testing.T) {
	b, _ := Marshal(Timings{DNS: MS(5 * time.Millisecond)}, nil)
	if string(b) != `{"dns":5,"tcp":null,"tls":null,"ttfb":null,"total":null}` {
		t.Errorf("got %s", b)
	}
}

type upperScrub struct{}

func (upperScrub) ScrubBytes(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("s3cret"), []byte("[REDACTED]"))
}

func TestEncoderScrubsAndTerminates(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf, upperScrub{})
	if err := enc.Encode(NewError(fixed, ErrInternal, "token s3cret leaked <&>")); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "s3cret") || !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Errorf("bad output %q", out)
	}
	if !strings.Contains(out, "<&>") {
		t.Errorf("html escaping should be off: %q", out)
	}
}

func TestTimestampRoundTrip(t *testing.T) {
	b, _ := json.Marshal(TS(fixed.Add(123 * time.Millisecond)))
	if string(b) != `"2026-10-11T13:01:00.123Z"` {
		t.Errorf("got %s", b)
	}
	var ts Timestamp
	if err := json.Unmarshal(b, &ts); err != nil || !ts.Time().Equal(fixed.Add(123*time.Millisecond)) {
		t.Errorf("round trip %v %v", ts.Time(), err)
	}
}

func TestLenientTimestamp(t *testing.T) {
	var c Check
	if err := json.Unmarshal([]byte(`{"v":1,"type":"check","ts":"not a time","status":"up"}`), &c); err != nil {
		t.Fatalf("a bad timestamp must not fail the event: %v", err)
	}
	if c.Status != StatusUp || !c.TS.Time().IsZero() {
		t.Errorf("decoded %+v", c)
	}
}
