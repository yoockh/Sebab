package engine

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/diagnose"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/platform"
)

// Sink receives every event the engine emits. Implementations must be safe for
// concurrent use and must not block for long.
type Sink interface {
	Emit(ev any)
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(ev any)

// Emit implements Sink.
func (f SinkFunc) Emit(ev any) { f(ev) }

// History stores observations and serves recent latencies for sparklines.
type History interface {
	Record(c event.Check) error
	Recent(id string) []int64
}

// CanaryConfigurer is implemented by canaries whose targets follow the config.
type CanaryConfigurer interface {
	Configure(targets, dnsNames []string)
}

// Options wire an engine.
type Options struct {
	Clock          Clock
	Checker        Checker
	Canary         Canary           // nil disables side detection
	Network        platform.Network // used to confirm recovery from a local outage
	Sink           Sink
	Version        string
	Source         string        // "embedded" or "daemon"
	OfflineRecheck time.Duration // default 30s
	JitterMax      time.Duration // default 10s; 0 in tests via NoJitter
	NoJitter       bool
	ResumeGrace    time.Duration  // wait after resume before checking; default 15s
	History        History        // nil disables history
	Location       *time.Location // for quiet hours; default time.Local
}

// Engine is the scheduler and state machine. Create with New, start with Run.
type Engine struct {
	clock          Clock
	checker        Checker
	canary         Canary
	network        platform.Network
	sink           Sink
	history        History
	loc            *time.Location
	version        string
	source         string
	offlineRecheck time.Duration
	jitterMax      time.Duration

	obsCh     chan observation
	reloadCh  chan *config.Config
	triggerCh chan string
	queryCh   chan func()
	recheckCh chan recheck
	sleepCh   chan bool
	offline   atomic.Bool
	sleeping  atomic.Bool
	graceTill atomic.Int64 // unix nanos; checks are skipped until then after a resume
	started   chan struct{}
	startOnce sync.Once

	// Owned by the loop goroutine.
	cfg         *config.Config
	heartbeat   time.Duration
	mons        map[string]*monState
	order       []string
	workers     map[string]*worker
	offlineDiag *diagnose.Diagnosis
	vpnDown     map[string]bool
	lastSummary *event.Summary
	gen         int
	lastResumed time.Time
	runCtx      context.Context
	offTimer    Timer
	rechecking  bool
	wakeTimer   Timer
	resumeGrace time.Duration
	quiet       *config.QuietHours
}

type recheck struct {
	ok   bool
	diag diagnose.Diagnosis
}

// New creates an engine for cfg.
func New(cfg *config.Config, opts Options) *Engine {
	if opts.Clock == nil {
		opts.Clock = RealClock{}
	}
	if opts.OfflineRecheck <= 0 {
		opts.OfflineRecheck = 30 * time.Second
	}
	if opts.JitterMax <= 0 {
		opts.JitterMax = maxJitter
	}
	if opts.NoJitter {
		opts.JitterMax = 0
	}
	if opts.Sink == nil {
		opts.Sink = SinkFunc(func(any) {})
	}
	if opts.ResumeGrace <= 0 {
		opts.ResumeGrace = 15 * time.Second
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	return &Engine{
		clock: opts.Clock, checker: opts.Checker, canary: opts.Canary, network: opts.Network,
		sink: opts.Sink, history: opts.History, loc: opts.Location, version: opts.Version, source: opts.Source,
		offlineRecheck: opts.OfflineRecheck, jitterMax: opts.JitterMax,
		obsCh: make(chan observation), reloadCh: make(chan *config.Config, 1),
		triggerCh: make(chan string, 64), queryCh: make(chan func()), recheckCh: make(chan recheck, 1),
		sleepCh: make(chan bool, 4), started: make(chan struct{}), resumeGrace: opts.ResumeGrace,
		cfg: cfg, mons: map[string]*monState{}, workers: map[string]*worker{}, vpnDown: map[string]bool{},
	}
}

func (e *Engine) isOffline() bool { return e.offline.Load() }

// paused reports whether workers must skip checks right now (asleep, or in the
// grace period after a resume).
func (e *Engine) paused() bool {
	return e.sleeping.Load() || e.clock.Now().UnixNano() < e.graceTill.Load()
}

// SetSleeping is called from a logind PrepareForSleep watcher: true before the
// system suspends, false after it resumes.
func (e *Engine) SetSleeping(sleeping bool) {
	select {
	case e.sleepCh <- sleeping:
	default:
	}
}

// Started is closed once Run has emitted the initial events.
func (e *Engine) Started() <-chan struct{} { return e.started }

// Run executes the engine until ctx ends. It never panics on bad results; all
// goroutines it starts are stopped before it returns.
func (e *Engine) Run(ctx context.Context) error {
	e.runCtx = ctx
	e.applyConfig(e.cfg, true)
	e.startOnce.Do(func() { close(e.started) })
	hb := e.clock.NewTimer(e.heartbeat)
	defer hb.Stop()
	defer e.stopAll()
	for {
		var offC, wakeC <-chan time.Time
		if e.offTimer != nil {
			offC = e.offTimer.C()
		}
		if e.wakeTimer != nil {
			wakeC = e.wakeTimer.C()
		}
		select {
		case <-ctx.Done():
			return nil
		case obs := <-e.obsCh:
			e.handle(obs)
		case cfg := <-e.reloadCh:
			e.applyConfig(cfg, false)
			hb.Reset(e.heartbeat)
		case id := <-e.triggerCh:
			e.trigger(id)
		case fn := <-e.queryCh:
			fn()
		case <-hb.C():
			e.sink.Emit(event.NewHeartbeat(e.clock.Now()))
			hb.Reset(e.heartbeat)
		case <-offC:
			e.startRecheck()
		case r := <-e.recheckCh:
			e.onRecheck(r)
		case s := <-e.sleepCh:
			e.onSleep(s)
		case <-wakeC:
			e.wakeTimer = nil
			e.trigger("")
		}
	}
}

// Reload replaces the configuration. A pending, not yet applied reload is dropped.
func (e *Engine) Reload(cfg *config.Config) {
	for {
		select {
		case e.reloadCh <- cfg:
			return
		default:
			select {
			case <-e.reloadCh:
			default:
			}
		}
	}
}

// Trigger asks for an immediate check of one monitor, or all when id is "".
func (e *Engine) Trigger(id string) {
	select {
	case e.triggerCh <- id:
	default:
	}
}

// query runs fn on the loop goroutine and waits for it.
func (e *Engine) query(ctx context.Context, fn func()) bool {
	done := make(chan struct{})
	select {
	case e.queryCh <- func() { fn(); close(done) }:
	case <-ctx.Done():
		return false
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// Snapshot returns the events a new subscriber needs to render the current
// state: hello, monitors, the latest check per monitor and a summary. None of
// them carries notify, so a late subscriber never re-alerts.
func (e *Engine) Snapshot(ctx context.Context) []any {
	var out []any
	e.query(ctx, func() {
		now := e.clock.Now()
		out = append(out, e.hello(now), e.monitorsEvent(now))
		for _, id := range e.order {
			m := e.mons[id]
			if m.checkedAt.IsZero() && m.display() != event.StatusPaused && e.offlineDiag == nil {
				continue
			}
			ev := m.checkEvent(now, e.offlineFor(m))
			if e.history != nil {
				ev.RecentMS = e.history.Recent(id)
			}
			out = append(out, ev)
		}
		out = append(out, e.summary(now))
	})
	return out
}

// LastCheck returns the latest check event for a monitor.
func (e *Engine) LastCheck(ctx context.Context, id string) (event.Check, bool) {
	var ev event.Check
	found := false
	e.query(ctx, func() {
		if m, ok := e.mons[id]; ok && (!m.checkedAt.IsZero() || m.display() == event.StatusPaused) {
			ev, found = m.checkEvent(e.clock.Now(), e.offlineFor(m)), true
		}
	})
	return ev, found
}

func (e *Engine) hello(now time.Time) event.Hello {
	return event.NewHello(now, e.version, len(e.order), e.heartbeat, e.source)
}

func (e *Engine) monitorsEvent(now time.Time) event.Monitors {
	list := make([]event.MonitorInfo, 0, len(e.order))
	for _, id := range e.order {
		list = append(list, e.mons[id].info)
	}
	return event.NewMonitors(now, list)
}

func (e *Engine) offlineFor(m *monState) *diagnose.Diagnosis {
	if e.offlineDiag == nil || m.display() == event.StatusPaused {
		return nil
	}
	return e.offlineDiag
}

// applyConfig diffs cfg against the running monitors.
func (e *Engine) applyConfig(cfg *config.Config, initial bool) {
	now := e.clock.Now()
	e.cfg = cfg
	ec := cfg.EngineSettings()
	e.heartbeat = ec.Heartbeat
	e.quiet = ec.QuietHours
	if c, ok := e.canary.(CanaryConfigurer); ok {
		c.Configure(ec.Canary, ec.DNSCanary)
	}
	next := map[string]bool{}
	var order []string
	for _, s := range cfg.ResolveAll() {
		id := s.Monitor.ID
		next[id] = true
		order = append(order, id)
		old, ok := e.mons[id]
		if ok && reflect.DeepEqual(old.settings, s) {
			continue
		}
		if ok && sameProbe(old.settings, s) {
			// Only the name or mute flag changed: keep state and the worker.
			old.settings, old.info = s, monitorInfo(s)
			continue
		}
		if ok {
			e.stopWorker(id)
		}
		m := &monState{settings: s, info: monitorInfo(s)}
		e.mons[id] = m
		if !s.Monitor.Paused {
			e.startWorker(s)
		}
	}
	for id := range e.mons {
		if !next[id] {
			e.stopWorker(id)
			delete(e.mons, id)
		}
	}
	e.order = order
	if initial {
		e.sink.Emit(e.hello(now))
	} else {
		e.sink.Emit(event.NewNotice(now, event.NoticeReloaded, fmt.Sprintf("configuration reloaded: %d monitors", len(order))))
	}
	e.sink.Emit(e.monitorsEvent(now))
	for _, id := range order {
		if m := e.mons[id]; m.display() == event.StatusPaused {
			e.emitCheck(m, nil)
		}
	}
	e.emitSummary(true)
}

// sameProbe reports whether two settings differ only in presentation fields.
func sameProbe(a, b config.Settings) bool {
	a.Monitor.Name, b.Monitor.Name = "", ""
	a.Monitor.Muted, b.Monitor.Muted = false, false
	return reflect.DeepEqual(a, b)
}

// quietNow reports whether notifications are suppressed by quiet hours.
func (e *Engine) quietNow() bool {
	return e.quiet != nil && e.quiet.Contains(e.clock.Now().In(e.loc))
}

func (e *Engine) startWorker(s config.Settings) {
	e.gen++
	ctx, cancel := context.WithCancel(e.runCtx)
	w := &worker{id: s.Monitor.ID, gen: e.gen, settings: s, trigger: make(chan struct{}, 1),
		cancel: cancel, done: make(chan struct{})}
	e.workers[w.id] = w
	limit := min(e.jitterMax, s.Interval)
	go e.runWorker(ctx, w, jitter(limit))
}

func (e *Engine) stopWorker(id string) {
	w, ok := e.workers[id]
	if !ok {
		return
	}
	delete(e.workers, id)
	w.cancel()
	// Drain observations while waiting so a worker blocked on send can exit.
	for {
		select {
		case <-w.done:
			return
		case obs := <-e.obsCh:
			if obs.id != id {
				e.handle(obs)
			}
		}
	}
}

func (e *Engine) stopAll() {
	for id := range e.workers {
		e.stopWorker(id)
	}
}

func (e *Engine) trigger(id string) {
	for wid, w := range e.workers {
		if id == "" || wid == id {
			select {
			case w.trigger <- struct{}{}:
			default:
			}
		}
	}
}

// handle applies one observation from a worker.
func (e *Engine) handle(obs observation) {
	m, ok := e.mons[obs.id]
	w, wok := e.workers[obs.id]
	if !ok || !wok || w.gen != obs.gen {
		return // stale: the monitor was removed or restarted
	}
	now := e.clock.Now()
	if obs.skipped {
		if now.Sub(e.lastResumed) > time.Minute {
			e.lastResumed = now
			e.sink.Emit(event.NewNotice(now, event.NoticeResumed, "clock jump detected (suspend/resume): skipping one check cycle"))
		}
		return
	}
	if e.isOffline() {
		return
	}
	m.result, m.checkedAt = obs.result, obs.finished
	d := obs.diag
	switch {
	case diagnose.IsLocal(d.Code):
		e.enterOffline(d)
		return
	case d.Code == diagnose.VPNDown:
		e.vpnWentDown(m, d)
		return
	}
	if m.vpnPaused {
		e.vpnCameBack(m)
	}
	t := m.apply(d, now)
	e.emitCheckRec(m, t.notify, true)
	e.emitSummary(false)
}

func (e *Engine) emitCheck(m *monState, n *event.Notification) {
	e.emitCheckRec(m, n, false)
}

// emitCheckRec renders and emits a check event; record stores it in history
// (only for real observations, not for re-renders after a reload or outage).
func (e *Engine) emitCheckRec(m *monState, n *event.Notification, record bool) {
	ev := m.checkEvent(e.clock.Now(), e.offlineFor(m))
	if e.history != nil {
		if record {
			_ = e.history.Record(ev)
		}
		ev.RecentMS = e.history.Recent(m.info.ID)
	}
	last := ev
	m.last = &last
	if n != nil && !m.settings.Monitor.Muted && !e.quietNow() {
		ev.Notify, ev.Notification = true, n
	}
	e.sink.Emit(ev)
}

func (e *Engine) summary(now time.Time) event.Summary {
	s := event.NewSummary(now)
	for _, id := range e.order {
		m := e.mons[id]
		st := m.display()
		if e.offlineFor(m) != nil {
			st = event.StatusUnknown
		}
		switch st {
		case event.StatusUp:
			s.Up++
		case event.StatusDegraded:
			s.Degraded++
		case event.StatusDown:
			s.Down++
		case event.StatusPaused:
			s.Paused++
		default:
			if !m.checkedAt.IsZero() || e.offlineDiag != nil {
				s.Unknown++
			}
		}
	}
	if e.offlineDiag != nil {
		s.Offline, s.OfflineCause = true, string(e.offlineDiag.Code)
	}
	return s
}

func (e *Engine) emitSummary(force bool) {
	s := e.summary(e.clock.Now())
	if !force && e.lastSummary != nil {
		a, b := *e.lastSummary, s
		a.Header, b.Header = event.Header{}, event.Header{}
		if a == b {
			return
		}
	}
	e.lastSummary = &s
	e.sink.Emit(s)
}

// active counts monitors that are not paused (by user or VPN).
func (e *Engine) active() int {
	n := 0
	for _, id := range e.order {
		if e.mons[id].display() != event.StatusPaused {
			n++
		}
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// notifyNotice attaches a notification unless quiet hours suppress it.
func (e *Engine) notifyNotice(n *event.Notice, note *event.Notification) {
	if e.quietNow() {
		return
	}
	n.Notify, n.Notification = true, note
}

// enterOffline switches every monitor to UNKNOWN and sends one notification.
func (e *Engine) enterOffline(d diagnose.Diagnosis) {
	now := e.clock.Now()
	e.offline.Store(true)
	e.offlineDiag = &d
	n := e.active()
	title := "You are offline"
	if d.Code == diagnose.LocalDNSBroken {
		title = "Your DNS is failing"
	}
	msg := fmt.Sprintf("%s %s paused until the network returns.", sentence(d.Summary), plural(n, "monitor", "monitors"))
	notice := event.NewNotice(now, event.NoticeOffline, msg)
	e.notifyNotice(&notice, &event.Notification{ID: fmt.Sprintf("offline:%d", now.UnixMilli()), Title: title,
		Body: msg, Urgency: "critical"})
	e.sink.Emit(notice)
	for _, id := range e.order {
		e.emitCheckRec(e.mons[id], nil, true)
	}
	e.emitSummary(false)
	e.scheduleRecheck()
}

// onSleep pauses checks before suspend and, after resume, waits resumeGrace for
// the network to come back before checking everything once.
func (e *Engine) onSleep(sleeping bool) {
	now := e.clock.Now()
	if sleeping {
		e.sleeping.Store(true)
		e.sink.Emit(event.NewNotice(now, event.NoticeResumed, "system is suspending: checks paused"))
		return
	}
	if !e.sleeping.Swap(false) {
		return
	}
	e.graceTill.Store(now.Add(e.resumeGrace).UnixNano())
	if e.canary != nil {
		e.canary.Invalidate()
	}
	e.sink.Emit(event.NewNotice(now, event.NoticeResumed,
		fmt.Sprintf("system resumed: checks continue in %s", e.resumeGrace)))
	if e.wakeTimer != nil {
		e.wakeTimer.Stop()
	}
	e.wakeTimer = e.clock.NewTimer(e.resumeGrace)
}

func (e *Engine) scheduleRecheck() {
	if e.offTimer == nil {
		e.offTimer = e.clock.NewTimer(e.offlineRecheck)
		return
	}
	e.offTimer.Reset(e.offlineRecheck)
}

// startRecheck tests the local side off the loop goroutine.
func (e *Engine) startRecheck() {
	if e.rechecking || e.offlineDiag == nil {
		return
	}
	e.rechecking = true
	wantDNS := e.offlineDiag.Code == diagnose.LocalDNSBroken
	ctx := e.runCtx
	go func() {
		r := e.localCheck(ctx, wantDNS)
		select {
		case e.recheckCh <- r:
		case <-ctx.Done():
		}
	}()
}

func (e *Engine) localCheck(ctx context.Context, wantDNS bool) recheck {
	local := func(code diagnose.Code) recheck {
		return recheck{diag: diagnose.Diagnose(diagnose.Outcome{Code: code}, nil, diagnose.Canary{})}
	}
	if e.network != nil {
		if links, err := e.network.Links(); err == nil && !platform.HasActiveLink(links) {
			return local(diagnose.LocalNoLink)
		}
		if ok, err := e.network.HasDefaultRoute(); err == nil && !ok {
			return local(diagnose.LocalNoRoute)
		}
	}
	if e.canary == nil {
		return recheck{ok: true}
	}
	e.canary.Invalidate()
	v := e.canary.Check(ctx, wantDNS)
	switch {
	case v.Checked && !v.OK:
		return local(diagnose.LocalOffline)
	case wantDNS && v.DNSChecked && !v.DNSOK:
		return local(diagnose.LocalDNSBroken)
	}
	return recheck{ok: true}
}

func (e *Engine) onRecheck(r recheck) {
	e.rechecking = false
	if !e.isOffline() {
		return
	}
	if !r.ok {
		e.offlineDiag = &r.diag
		e.scheduleRecheck()
		return
	}
	e.exitOffline()
}

// exitOffline restores the pre-outage states (no recovery spam), sends one
// "back online" notification and re-checks everything.
func (e *Engine) exitOffline() {
	now := e.clock.Now()
	e.offline.Store(false)
	e.offlineDiag = nil
	if e.offTimer != nil {
		e.offTimer.Stop()
	}
	msg := fmt.Sprintf("Checks resumed for %s.", plural(e.active(), "monitor", "monitors"))
	notice := event.NewNotice(now, event.NoticeOnline, msg)
	e.notifyNotice(&notice, &event.Notification{ID: fmt.Sprintf("online:%d", now.UnixMilli()), Title: "Back online",
		Body: msg, Urgency: "normal"})
	e.sink.Emit(notice)
	for _, id := range e.order {
		if m := e.mons[id]; !m.checkedAt.IsZero() || m.display() == event.StatusPaused {
			e.emitCheck(m, nil)
		}
	}
	e.emitSummary(false)
	e.trigger("")
}

func ifaceOf(m *monState) string {
	if v := m.settings.Monitor.RequiresVPN; v != nil {
		return v.Interface
	}
	return ""
}

// vpnWentDown pauses every monitor that needs the interface and notifies once.
func (e *Engine) vpnWentDown(m *monState, d diagnose.Diagnosis) {
	iface := ifaceOf(m)
	m.observed, m.observedDiag = event.StatusPaused, d
	if e.vpnDown[iface] {
		m.vpnPaused = true
		e.emitCheckRec(m, nil, true)
		e.emitSummary(false)
		return
	}
	e.vpnDown[iface] = true
	var names []string
	allMuted := true
	for _, id := range e.order {
		o := e.mons[id]
		if ifaceOf(o) != iface || o.settings.Monitor.Paused {
			continue
		}
		names = append(names, o.info.Name)
		allMuted = allMuted && o.settings.Monitor.Muted
		o.vpnPaused = true
		if o != m {
			o.observedDiag = d
		}
	}
	now := e.clock.Now()
	msg := fmt.Sprintf("%s paused: VPN interface %s is down.", strings.Join(names, ", "), iface)
	notice := event.NewNotice(now, event.NoticeVPNDown, msg)
	notice.Monitors = names
	if !allMuted {
		e.notifyNotice(&notice, &event.Notification{ID: fmt.Sprintf("vpn:%s:down:%d", iface, now.UnixMilli()),
			Title: "VPN " + iface + " is down", Body: msg, Urgency: "normal"})
	}
	e.sink.Emit(notice)
	for _, id := range e.order {
		if o := e.mons[id]; o.vpnPaused && ifaceOf(o) == iface {
			e.emitCheckRec(o, nil, true)
		}
	}
	e.emitSummary(false)
}

// vpnCameBack resumes the interface's monitors and notifies once.
func (e *Engine) vpnCameBack(m *monState) {
	iface := ifaceOf(m)
	m.vpnPaused = false
	if !e.vpnDown[iface] {
		return
	}
	delete(e.vpnDown, iface)
	n := 0
	for _, id := range e.order {
		o := e.mons[id]
		if ifaceOf(o) == iface && !o.settings.Monitor.Paused {
			n++
			if o != m && o.vpnPaused {
				o.vpnPaused = false
				if w, ok := e.workers[id]; ok {
					select {
					case w.trigger <- struct{}{}:
					default:
					}
				}
			}
		}
	}
	now := e.clock.Now()
	msg := fmt.Sprintf("%s resumed.", plural(n, "monitor", "monitors"))
	notice := event.NewNotice(now, event.NoticeVPNUp, msg)
	e.notifyNotice(&notice, &event.Notification{ID: fmt.Sprintf("vpn:%s:up:%d", iface, now.UnixMilli()),
		Title: "VPN " + iface + " is back", Body: msg, Urgency: "low"})
	e.sink.Emit(notice)
}
