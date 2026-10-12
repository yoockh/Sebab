package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/diagnose"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/platform"
	"github.com/yoockh/sebab/internal/probe"
)

var t0 = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// script returns per-monitor results in order; the last one repeats.
type script struct {
	mu    sync.Mutex
	steps map[string][]diagnose.Code // "" means healthy
	calls map[string]int
	retry time.Duration
}

func newScript() *script {
	return &script{steps: map[string][]diagnose.Code{}, calls: map[string]int{}}
}

func (s *script) set(id string, codes ...diagnose.Code) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps[id] = codes
	s.calls[id] = 0
}

func (s *script) count(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[id]
}

func (s *script) Check(_ context.Context, st config.Settings) probe.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := st.Monitor.ID
	steps := s.steps[id]
	n := s.calls[id]
	s.calls[id]++
	var code diagnose.Code
	if len(steps) > 0 {
		code = steps[min(n, len(steps)-1)]
	}
	if code == "" {
		return probe.Result{Passed: []event.Layer{event.LayerDNS, event.LayerTCP, event.LayerHTTP}}
	}
	out := probe.Result{Outcome: &diagnose.Outcome{Code: code, Facts: diagnose.Facts{Host: "h.example", Port: 443,
		Interface: "wg0", HTTPStatus: 502, Now: t0, NotAfter: t0.Add(5 * 24 * time.Hour)}}}
	if code == diagnose.HTTPRateLimited {
		out.Outcome.Facts.RetryAfter = s.retry
	}
	return out
}

type fakeCanary struct {
	mu sync.Mutex
	ok bool
}

func (c *fakeCanary) set(ok bool) { c.mu.Lock(); c.ok = ok; c.mu.Unlock() }

func (c *fakeCanary) Check(context.Context, bool) diagnose.Canary {
	c.mu.Lock()
	defer c.mu.Unlock()
	return diagnose.Canary{Checked: true, OK: c.ok, DNSChecked: true, DNSOK: c.ok}
}

func (c *fakeCanary) Invalidate() {}

type recorder struct {
	mu     sync.Mutex
	events []any
}

func (r *recorder) Emit(ev any) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) all() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]any(nil), r.events...)
}

func (r *recorder) checks(id string) []event.Check {
	var out []event.Check
	for _, ev := range r.all() {
		if c, ok := ev.(event.Check); ok && c.Monitor.ID == id {
			out = append(out, c)
		}
	}
	return out
}

func (r *recorder) notices(kind string) []event.Notice {
	var out []event.Notice
	for _, ev := range r.all() {
		if n, ok := ev.(event.Notice); ok && n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

func (r *recorder) notifications() []*event.Notification {
	var out []*event.Notification
	for _, ev := range r.all() {
		switch e := ev.(type) {
		case event.Check:
			if e.Notify {
				out = append(out, e.Notification)
			}
		case event.Notice:
			if e.Notify {
				out = append(out, e.Notification)
			}
		}
	}
	return out
}

type harness struct {
	t      *testing.T
	clock  *FakeClock
	script *script
	canary *fakeCanary
	rec    *recorder
	eng    *Engine
	cancel context.CancelFunc
	done   chan struct{}
	cfg    *config.Config
}

func monitor(id string) config.Monitor {
	return config.Monitor{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], URL: "https://" + id + ".example/health"}
}

func start(t *testing.T, mons ...config.Monitor) *harness {
	t.Helper()
	hb := config.Duration(15 * time.Second)
	cfg := &config.Config{Defaults: config.Defaults{Heartbeat: &hb}, Monitors: mons}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, clock: NewFakeClock(t0), script: newScript(), canary: &fakeCanary{ok: true}, rec: &recorder{}, cfg: cfg}
	h.eng = New(cfg, Options{Clock: h.clock, Checker: h.script, Canary: h.canary, Sink: h.rec,
		Network: platform.Online(), Version: "test", Source: "embedded", NoJitter: true, OfflineRecheck: 30 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan struct{})
	go func() { _ = h.eng.Run(ctx); close(h.done) }()
	t.Cleanup(h.stop)
	<-h.eng.Started()
	return h
}

func (h *harness) stop() {
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		h.t.Fatal("engine did not stop")
	}
}

// settle waits until every worker has re-armed its timer.
func (h *harness) settle(timers int) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for h.clock.Pending() < timers {
		if time.Now().After(deadline) {
			h.t.Fatalf("timers pending %d, want %d", h.clock.Pending(), timers)
		}
		time.Sleep(time.Millisecond)
	}
	// Let the loop finish emitting what it received.
	h.eng.query(context.Background(), func() {})
}

// tick advances by d and waits for the engine to settle again.
func (h *harness) tick(d time.Duration, timers int) {
	h.t.Helper()
	h.clock.Advance(d)
	h.settle(timers)
}

func (h *harness) waitChecks(id string, n int) []event.Check {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		cs := h.rec.checks(id)
		if len(cs) >= n {
			return cs
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("got %d check events for %s, want %d", len(cs), id, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) waitNotices(kind string, n int) []event.Notice {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ns := h.rec.notices(kind)
		if len(ns) >= n {
			h.eng.query(context.Background(), func() {})
			return ns
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("got %d %s notices, want %d", len(ns), kind, n)
		}
		time.Sleep(time.Millisecond)
	}
}

const interval = 60 * time.Second

func TestFlapControlAndRecovery(t *testing.T) {
	h := start(t, monitor("api"))
	h.script.set("api", "", diagnose.TCPRefused, diagnose.TCPRefused, diagnose.TCPRefused, "", "")
	h.settle(2) // worker + heartbeat
	h.tick(0, 2)
	for i := 0; i < 5; i++ {
		h.tick(interval, 2)
	}
	cs := h.waitChecks("api", 6)
	want := []struct {
		status, observed event.Status
		notify           bool
	}{
		{event.StatusUp, event.StatusUp, false},
		{event.StatusUp, event.StatusDown, false},
		{event.StatusDown, event.StatusDown, true},
		{event.StatusDown, event.StatusDown, false},
		{event.StatusDown, event.StatusUp, false},
		{event.StatusUp, event.StatusUp, true},
	}
	for i, w := range want {
		c := cs[i]
		if c.Status != w.status || c.Observed != w.observed || c.Notify != w.notify {
			t.Errorf("check %d: got %s/%s notify=%v want %s/%s notify=%v", i, c.Status, c.Observed, c.Notify,
				w.status, w.observed, w.notify)
		}
	}
	if n := cs[2].Notification; n == nil || n.Title != "Api is down" || !strings.Contains(n.Body, "nothing listening on 443") {
		t.Errorf("down notification %+v", n)
	}
	if cs[2].Cause == nil || cs[2].Cause.Code != "TCP_REFUSED" || cs[2].Side != event.SideTheirs {
		t.Errorf("cause/side %+v %s", cs[2].Cause, cs[2].Side)
	}
	if n := cs[5].Notification; n == nil || n.Title != "Api recovered" || !strings.Contains(n.Body, "3 minutes") {
		t.Errorf("recovery notification %+v", n)
	}
	if cs[1].Cause == nil || cs[1].Cause.Code != "TCP_REFUSED" {
		t.Errorf("pending failure should carry the observed cause: %+v", cs[1].Cause)
	}
}

func TestCauseChangeNotifiesOnce(t *testing.T) {
	h := start(t, monitor("api"))
	h.script.set("api", diagnose.HTTPBadGateway, diagnose.HTTPBadGateway, diagnose.TLSExpired, diagnose.TLSExpired,
		diagnose.TLSExpired, diagnose.TLSExpired)
	h.settle(2)
	h.tick(0, 2)
	for i := 0; i < 5; i++ {
		h.tick(interval, 2)
	}
	h.waitChecks("api", 6)
	notes := h.rec.notifications()
	if len(notes) != 2 {
		t.Fatalf("want down + cause change, got %d: %+v", len(notes), notes)
	}
	if notes[0].Title != "Api is down" || notes[1].Title != "Api: new cause" || !strings.Contains(notes[1].Body, "expired") {
		t.Errorf("notifications %+v %+v", notes[0], notes[1])
	}
}

func TestDownAtStartupShowsImmediatelyNotifiesAfterThreshold(t *testing.T) {
	h := start(t, monitor("api"))
	h.script.set("api", diagnose.HTTPUnavailable)
	h.settle(2)
	h.tick(0, 2)
	cs := h.waitChecks("api", 1)
	if cs[0].Status != event.StatusDown || cs[0].Notify {
		t.Fatalf("first failing check should display down without notifying: %+v", cs[0])
	}
	h.tick(interval, 2)
	cs = h.waitChecks("api", 2)
	if !cs[1].Notify {
		t.Fatal("second failure should notify")
	}
}

func TestLocalOutageOneNotification(t *testing.T) {
	h := start(t, monitor("a"), monitor("b"), monitor("c"))
	for _, id := range []string{"a", "b", "c"} {
		h.script.set(id, "")
	}
	h.settle(4)
	h.tick(0, 4)
	h.waitChecks("c", 1)
	h.canary.set(false)
	for _, id := range []string{"a", "b", "c"} {
		h.script.set(id, diagnose.TCPTimeout)
	}
	h.tick(interval, 5) // workers + heartbeat + offline recheck timer
	h.tick(interval, 5) // still offline: workers skip, recheck keeps failing
	offline := h.rec.notices(event.NoticeOffline)
	if len(offline) != 1 || !offline[0].Notify || !strings.Contains(offline[0].Message, "3 monitors paused") {
		t.Fatalf("want exactly one offline notice, got %+v", offline)
	}
	for _, id := range []string{"a", "b", "c"} {
		cs := h.rec.checks(id)
		last := cs[len(cs)-1]
		if last.Status != event.StatusUnknown || last.Cause == nil || last.Cause.Code != "LOCAL_OFFLINE" || last.Notify {
			t.Errorf("%s: %+v", id, last)
		}
	}
	callsWhileOffline := h.script.count("a")
	h.canary.set(true)
	for _, id := range []string{"a", "b", "c"} {
		h.script.set(id, "")
	}
	h.tick(30*time.Second, 4) // recheck succeeds, workers triggered
	online := h.waitNotices(event.NoticeOnline, 1)
	if len(online) != 1 || online[0].Notification.Title != "Back online" {
		t.Fatalf("want one back-online notice, got %+v", online)
	}
	h.waitChecks("a", len(h.rec.checks("a"))) // flush
	for _, n := range h.rec.notifications() {
		if strings.Contains(n.Title, "recovered") || strings.Contains(n.Title, "is down") {
			t.Errorf("no per-monitor alerts during a local outage: %+v", n)
		}
	}
	if len(h.rec.notifications()) != 2 {
		t.Errorf("want exactly offline + online notifications, got %d", len(h.rec.notifications()))
	}
	if callsWhileOffline > 4 {
		t.Errorf("workers kept probing while offline: %d calls", callsWhileOffline)
	}
	var sum *event.Summary
	for _, ev := range h.rec.all() {
		if s, ok := ev.(event.Summary); ok && s.Offline {
			sum = &s
		}
	}
	if sum == nil || sum.Unknown != 3 || sum.OfflineCause != "LOCAL_OFFLINE" {
		t.Errorf("offline summary %+v", sum)
	}
}

func TestNoRouteGoesOfflineDirectly(t *testing.T) {
	h := start(t, monitor("a"), monitor("b"))
	h.script.set("a", diagnose.LocalNoRoute)
	h.script.set("b", diagnose.LocalNoRoute)
	h.settle(3)
	h.tick(0, 4)
	offline := h.rec.notices(event.NoticeOffline)
	if len(offline) != 1 || offline[0].Notification.Title != "You are offline" ||
		!strings.Contains(offline[0].Message, "No default route") {
		t.Fatalf("offline notices %+v", offline)
	}
}

func TestVPNDownPausesAndResumes(t *testing.T) {
	vpn := func(id string) config.Monitor {
		m := monitor(id)
		m.RequiresVPN = &config.VPN{Interface: "wg0"}
		return m
	}
	h := start(t, vpn("admin"), vpn("wiki"), monitor("public"))
	h.script.set("admin", diagnose.VPNDown)
	h.script.set("wiki", diagnose.VPNDown)
	h.script.set("public", "")
	h.settle(4)
	h.tick(0, 4)
	h.waitChecks("wiki", 1)
	down := h.rec.notices(event.NoticeVPNDown)
	if len(down) != 1 || !strings.Contains(down[0].Message, "Admin, Wiki paused: VPN interface wg0 is down") {
		t.Fatalf("vpn down notices %+v", down)
	}
	for _, id := range []string{"admin", "wiki"} {
		cs := h.rec.checks(id)
		if last := cs[len(cs)-1]; last.Status != event.StatusPaused || last.Cause == nil || last.Cause.Code != "VPN_DOWN" {
			t.Errorf("%s: %+v", id, last)
		}
	}
	h.script.set("admin", "")
	h.script.set("wiki", "")
	h.tick(interval, 4)
	up := h.rec.notices(event.NoticeVPNUp)
	if len(up) != 1 || up[0].Notification.Title != "VPN wg0 is back" {
		t.Fatalf("vpn up notices %+v", up)
	}
	for _, id := range []string{"admin", "wiki"} {
		cs := h.waitChecks(id, 2)
		if last := cs[len(cs)-1]; last.Status != event.StatusUp {
			t.Errorf("%s should resume: %+v", id, last)
		}
	}
	if pub := h.rec.checks("public"); pub[len(pub)-1].Status != event.StatusUp {
		t.Error("monitor without VPN must be unaffected")
	}
}

func TestSuspendJumpSkipsOneCycle(t *testing.T) {
	h := start(t, monitor("api"))
	h.script.set("api", "")
	h.settle(2)
	h.tick(0, 2)
	h.waitChecks("api", 1)
	h.clock.Jump(10 * interval) // laptop slept for 10 minutes
	h.tick(interval, 2)
	if got := h.script.count("api"); got != 1 {
		t.Fatalf("check after resume should be skipped, calls=%d", got)
	}
	if len(h.rec.notices(event.NoticeResumed)) != 1 {
		t.Fatal("want a resumed notice")
	}
	h.tick(interval, 2)
	if got := h.script.count("api"); got != 2 {
		t.Fatalf("next cycle should check again, calls=%d", got)
	}
}

func TestTLSExpiryWarningOncePerDay(t *testing.T) {
	m := monitor("api")
	m.Interval = durPtr(30 * time.Minute)
	h := start(t, m)
	h.script.set("api", diagnose.TLSExpiringSoon)
	h.settle(2)
	h.tick(0, 2)
	for i := 0; i < 50; i++ { // 25 hours
		h.tick(30*time.Minute, 2)
	}
	var tls int
	for _, n := range h.rec.notifications() {
		if strings.Contains(n.Title, "certificate expires soon") {
			tls++
		}
	}
	if tls != 2 {
		t.Fatalf("want one expiry notification per day (2 over 25h), got %d", tls)
	}
	cs := h.rec.checks("api")
	if last := cs[len(cs)-1]; last.Status != event.StatusDegraded {
		t.Errorf("expiring cert should be degraded: %s", last.Status)
	}
}

func TestRetryAfterBackoff(t *testing.T) {
	h := start(t, monitor("api"))
	h.script.retry = 5 * time.Minute
	h.script.set("api", diagnose.HTTPRateLimited)
	h.settle(2)
	h.tick(0, 2)
	h.tick(interval, 2)
	h.tick(interval, 2)
	if got := h.script.count("api"); got != 1 {
		t.Fatalf("429 with Retry-After 300s should delay the next check, calls=%d", got)
	}
	h.tick(3*time.Minute, 2)
	if got := h.script.count("api"); got != 2 {
		t.Fatalf("check should resume after Retry-After, calls=%d", got)
	}
}

func TestReloadAddRemovePause(t *testing.T) {
	h := start(t, monitor("a"))
	h.script.set("a", "")
	h.script.set("b", "")
	h.settle(2)
	h.tick(0, 2)
	paused := monitor("b")
	paused.Paused = true
	cfg := &config.Config{Defaults: h.cfg.Defaults, Monitors: []config.Monitor{paused}}
	h.eng.Reload(cfg)
	h.settle(1)
	var last event.Monitors
	for _, ev := range h.rec.all() {
		if m, ok := ev.(event.Monitors); ok {
			last = m
		}
	}
	if len(last.Monitors) != 1 || last.Monitors[0].ID != "b" || !last.Monitors[0].Paused {
		t.Fatalf("monitors after reload %+v", last.Monitors)
	}
	cs := h.waitChecks("b", 1)
	if cs[0].Status != event.StatusPaused {
		t.Errorf("paused monitor should emit a paused check: %+v", cs[0])
	}
	if len(h.rec.notices(event.NoticeReloaded)) != 1 {
		t.Error("want a reload notice")
	}
	h.tick(interval, 1)
	if h.script.count("b") != 0 {
		t.Error("paused monitor must not be checked")
	}
}

func TestHeartbeatAndSnapshot(t *testing.T) {
	h := start(t, monitor("api"))
	h.script.set("api", diagnose.HTTPAuth, diagnose.HTTPAuth)
	h.settle(2)
	h.tick(0, 2)
	for i := 0; i < 4; i++ { // the fake clock fires a re-armed timer once per Advance
		h.tick(15*time.Second, 2)
	}
	beats := 0
	for _, ev := range h.rec.all() {
		if _, ok := ev.(event.Heartbeat); ok {
			beats++
		}
	}
	if beats < 4 {
		t.Errorf("want heartbeats every 15s, got %d in 60s", beats)
	}
	snap := h.eng.Snapshot(context.Background())
	if _, ok := snap[0].(event.Hello); !ok {
		t.Fatalf("snapshot must start with hello: %T", snap[0])
	}
	var found bool
	for _, ev := range snap {
		if c, ok := ev.(event.Check); ok {
			found = true
			if c.Notify || c.Notification != nil || c.Status != event.StatusDown || c.Side != event.SideYours {
				t.Errorf("snapshot check %+v", c)
			}
		}
	}
	if !found {
		t.Error("snapshot lacks the check event")
	}
	if c, ok := h.eng.LastCheck(context.Background(), "api"); !ok || c.Cause.Code != "HTTP_AUTH" {
		t.Errorf("last check %+v %v", c, ok)
	}
}

func TestTriggerChecksImmediately(t *testing.T) {
	h := start(t, monitor("api"))
	h.script.set("api", "")
	h.settle(2)
	h.tick(0, 2)
	h.eng.Trigger("")
	deadline := time.Now().Add(2 * time.Second)
	for h.script.count("api") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("trigger did not run a check")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCanaryDisabledReportsUnknownSide(t *testing.T) {
	cfg := &config.Config{Monitors: []config.Monitor{monitor("db")}}
	rec := &recorder{}
	sc := newScript()
	sc.set("db", diagnose.TCPRefused)
	clock := NewFakeClock(t0)
	eng := New(cfg, Options{Clock: clock, Checker: sc, Sink: rec, NoJitter: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = eng.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	<-eng.Started()
	for clock.Pending() < 2 {
		time.Sleep(time.Millisecond)
	}
	clock.Advance(0)
	deadline := time.Now().Add(2 * time.Second)
	for len(rec.checks("db")) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cs := rec.checks("db")
	if len(cs) == 0 || cs[0].Side != event.SideUnknown {
		t.Fatalf("canary disabled should give side unknown: %+v", cs)
	}
}

func durPtr(d time.Duration) *config.Duration { v := config.Duration(d); return &v }

func TestLogindSleepPausesAndGraceDelaysChecks(t *testing.T) {
	h := start(t, monitor("api"))
	h.script.set("api", "")
	h.settle(2)
	h.tick(0, 2)
	h.waitChecks("api", 1)
	h.eng.SetSleeping(true)
	h.eng.query(context.Background(), func() {})
	h.tick(interval, 2)
	if got := h.script.count("api"); got != 1 {
		t.Fatalf("no checks while sleeping, calls=%d", got)
	}
	h.eng.SetSleeping(false)
	h.eng.query(context.Background(), func() {})
	h.tick(5*time.Second, 3) // inside the 15s grace: worker, heartbeat, wake timer
	if got := h.script.count("api"); got != 1 {
		t.Fatalf("no checks during the resume grace, calls=%d", got)
	}
	h.clock.Advance(10 * time.Second) // grace ends: wake timer triggers every monitor
	deadline := time.Now().Add(3 * time.Second)
	for h.script.count("api") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("checks should resume after the grace period")
		}
		time.Sleep(time.Millisecond)
	}
	var resumed int
	for _, n := range h.rec.notices(event.NoticeResumed) {
		if strings.Contains(n.Message, "resumed") || strings.Contains(n.Message, "suspending") {
			resumed++
		}
	}
	if resumed != 2 {
		t.Errorf("want suspend + resume notices, got %d", resumed)
	}
	for _, n := range h.rec.notifications() {
		t.Errorf("sleep handling must not notify: %+v", n)
	}
}
