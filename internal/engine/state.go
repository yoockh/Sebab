package engine

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/diagnose"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/probe"
	"github.com/yoockh/sebab/internal/secret"
)

// tlsWarnEvery limits TLS expiry notifications per monitor (Story 10).
const tlsWarnEvery = 24 * time.Hour

// monState is the engine's view of one monitor. Only the engine loop touches it.
type monState struct {
	settings config.Settings
	info     event.MonitorInfo

	// confirmed is the flap-controlled state; empty until the first confirmation.
	confirmed     event.Status
	confirmedDiag diagnose.Diagnosis
	since         time.Time
	downSince     time.Time

	// streak counts consecutive observations of the same (status, code) that
	// differ from the confirmed state.
	streakStatus event.Status
	streakCode   diagnose.Code
	streakCount  int

	observed     event.Status
	observedDiag diagnose.Diagnosis
	result       probe.Result
	checkedAt    time.Time

	vpnPaused   bool // display PAUSED because the VPN is down
	lastTLSWarn time.Time
	last        *event.Check
}

// transition is the outcome of applying an observation.
type transition struct {
	changed bool
	from    event.Status
	fromDia diagnose.Diagnosis
	notify  *event.Notification
}

func streakKey(d diagnose.Diagnosis) (event.Status, diagnose.Code) {
	if d.Status == event.StatusUp {
		return event.StatusUp, ""
	}
	return d.Status, d.Code
}

// apply runs flap control for one observation and returns what changed.
func (m *monState) apply(d diagnose.Diagnosis, now time.Time) transition {
	m.observed, m.observedDiag = d.Status, d
	s, c := streakKey(d)
	cs, cc := streakKey(m.confirmedDiag)
	if m.confirmed != "" && s == cs && c == cc {
		m.streakCount = 0
		m.confirmedDiag = d // refresh texts (for example "expires in 4 days")
		return m.tlsReminder(d, now)
	}
	if s == m.streakStatus && c == m.streakCode {
		m.streakCount++
	} else {
		m.streakStatus, m.streakCode, m.streakCount = s, c, 1
	}
	threshold := m.settings.FailThreshold
	if s == event.StatusUp {
		threshold = m.settings.RecoverThreshold
	}
	if m.confirmed == "" && s == event.StatusUp {
		threshold = 1 // a healthy first check needs no confirmation
	}
	if m.streakCount < threshold {
		return transition{}
	}
	return m.confirm(d, now)
}

func (m *monState) confirm(d diagnose.Diagnosis, now time.Time) transition {
	t := transition{changed: true, from: m.confirmed, fromDia: m.confirmedDiag}
	prev := m.confirmed
	m.confirmed, m.confirmedDiag = d.Status, d
	m.since = now
	m.streakStatus, m.streakCode, m.streakCount = "", "", 0
	name := m.info.Name
	switch d.Status {
	case event.StatusDown:
		if prev == event.StatusDown {
			t.notify = m.note("cause", now, name+": new cause", body(d), "critical")
		} else {
			m.downSince = now
			t.notify = m.note("down", now, name+" is down", body(d), "critical")
		}
	case event.StatusDegraded:
		if d.Code == diagnose.TLSExpiringSoon {
			t.notify = m.tlsReminder(d, now).notify
		} else {
			t.notify = m.note("degraded", now, name+" is degraded", body(d), "normal")
		}
	case event.StatusUp:
		if prev == event.StatusDown && !m.downSince.IsZero() {
			t.notify = m.note("recovered", now, name+" recovered",
				fmt.Sprintf("Back up after %s.", diagnose.Ago(now.Sub(m.downSince))), "normal")
		}
		m.downSince = time.Time{}
	}
	return t
}

// tlsReminder emits at most one expiry notification per monitor per day.
func (m *monState) tlsReminder(d diagnose.Diagnosis, now time.Time) transition {
	if d.Code != diagnose.TLSExpiringSoon || (!m.lastTLSWarn.IsZero() && now.Sub(m.lastTLSWarn) < tlsWarnEvery) {
		return transition{}
	}
	m.lastTLSWarn = now
	return transition{notify: m.note("tls-expiry", now, m.info.Name+": certificate expires soon", body(d), "normal")}
}

func (m *monState) note(kind string, now time.Time, title, text, urgency string) *event.Notification {
	return &event.Notification{
		ID:    fmt.Sprintf("%s:%s:%d", m.info.ID, kind, now.UnixMilli()),
		Title: title, Body: text, Urgency: urgency,
	}
}

// body is the notification text: the summary as a sentence, then the hint.
func body(d diagnose.Diagnosis) string {
	s := sentence(d.Summary)
	if d.Hint != "" {
		s += " " + d.Hint
	}
	return s
}

func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[n:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// display is the status frontends show.
func (m *monState) display() event.Status {
	switch {
	case m.settings.Monitor.Paused || m.vpnPaused:
		return event.StatusPaused
	case m.confirmed == "":
		return m.observed
	default:
		return m.confirmed
	}
}

// checkEvent renders the current state as a check event.
func (m *monState) checkEvent(now time.Time, offline *diagnose.Diagnosis) event.Check {
	ev := event.NewCheck(now)
	ev.Monitor = event.MonitorRef{ID: m.info.ID, Name: m.info.Name, Kind: m.info.Kind, Target: m.info.Target,
		InsecureTLS: m.settings.Monitor.InsecureTLS}
	ev.Status = m.display()
	ev.Observed = m.observed
	if !m.checkedAt.IsZero() {
		ev.Timings = m.result.Timings
		ev.Details = details(m.result)
	}
	var cause *diagnose.Diagnosis
	switch {
	case offline != nil && ev.Status != event.StatusPaused:
		ev.Status = event.StatusUnknown
		cause = offline
	case m.vpnPaused:
		cause = &m.observedDiag
	case m.observed != "" && m.observed != event.StatusUp:
		cause = &m.observedDiag
	case m.confirmed != "" && m.confirmed != event.StatusUp:
		cause = &m.confirmedDiag
	}
	if cause != nil && cause.Code != "" {
		ev.Cause = cause.Cause()
		ev.Side = cause.Side
	}
	if !m.since.IsZero() && ev.Status == m.confirmed {
		since := event.TS(m.since)
		ev.Since = &since
	}
	return ev
}

func details(r probe.Result) *event.Details {
	d := &event.Details{HTTPStatus: r.HTTPStatus, Address: r.Address, Family: r.Family}
	if !r.TLSNotAfter.IsZero() {
		ts := event.TS(r.TLSNotAfter)
		d.TLSNotAfter = &ts
	}
	if r.Outcome != nil && r.Outcome.Facts.RetryAfter > 0 {
		d.RetryAfterSec = int(r.Outcome.Facts.RetryAfter.Seconds())
	}
	if *d == (event.Details{}) {
		return nil
	}
	return d
}

// monitorInfo describes a monitor for the monitors event (target redacted).
func monitorInfo(s config.Settings) event.MonitorInfo {
	m := s.Monitor
	info := event.MonitorInfo{ID: m.ID, Name: m.Name, Kind: m.Kind(), Target: secret.RedactURL(m.Endpoint()),
		IntervalS: int(s.Interval.Seconds()), Paused: m.Paused, Muted: m.Muted, InsecureTLS: m.InsecureTLS}
	if m.RequiresVPN != nil {
		info.RequiresVPN = m.RequiresVPN.Interface
	}
	if m.Auth != nil {
		info.Auth = m.Auth.Type
	}
	return info
}
