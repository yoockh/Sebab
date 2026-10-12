package history

import (
	"sort"
	"time"

	"github.com/yoockh/sebab/internal/event"
)

// Overview is the compact dashboard summary of the last period: per-monitor
// uptime and a bucketed status strip, plus recent status changes. It is the
// payload of `sebab overview --json` (its own "v": 1, additive to schema v1).
type Overview struct {
	V         int                        `json:"v"`
	SinceS    int64                      `json:"since_s"`
	Buckets   int                        `json:"buckets"`
	Checks    int                        `json:"checks"`
	Uptime    *float64                   `json:"uptime"` // null when nothing was judged yet
	Monitors  map[string]MonitorOverview `json:"monitors"`
	Incidents []Transition               `json:"incidents"` // newest first
}

// MonitorOverview is one monitor's part of an Overview.
type MonitorOverview struct {
	Checks int      `json:"checks"`
	Uptime *float64 `json:"uptime"`
	// Strip holds one status per bucket, oldest first: the worst status seen in
	// that bucket ("up", "degraded", "down", "unknown", "paused") or "" when
	// there was no check.
	Strip []string `json:"strip"`
}

var severity = map[event.Status]int{"": 0, event.StatusUp: 1, event.StatusPaused: 2, event.StatusUnknown: 3,
	event.StatusDegraded: 4, event.StatusDown: 5}

// BuildOverview summarises the store over [now-period, now] in n buckets,
// keeping at most maxIncidents status changes.
func (s *Store) BuildOverview(now time.Time, period time.Duration, n, maxIncidents int) (Overview, error) {
	if n <= 0 {
		n = 24
	}
	from := now.Add(-period)
	samples, err := s.Samples("", from, now)
	if err != nil {
		return Overview{}, err
	}
	trs, err := s.Transitions("", from)
	if err != nil {
		return Overview{}, err
	}
	ov := Overview{V: 1, SinceS: int64(period.Seconds()), Buckets: n, Checks: len(samples),
		Monitors: map[string]MonitorOverview{}}
	byMonitor := map[string][]Sample{}
	for _, smp := range samples {
		byMonitor[smp.Monitor] = append(byMonitor[smp.Monitor], smp)
	}
	width := period / time.Duration(n)
	for id, list := range byMonitor {
		mo := MonitorOverview{Checks: len(list), Strip: make([]string, n)}
		if st := Summarize(list); st.judged() {
			u := st.Availability
			mo.Uptime = &u
		}
		for _, smp := range list {
			i := int(time.UnixMilli(smp.Time).Sub(from) / width)
			if i < 0 || i >= n {
				continue
			}
			if severity[smp.Status] > severity[event.Status(mo.Strip[i])] {
				mo.Strip[i] = string(smp.Status)
			}
		}
		ov.Monitors[id] = mo
	}
	if st := Summarize(samples); st.judged() {
		u := st.Availability
		ov.Uptime = &u
	}
	sort.SliceStable(trs, func(i, j int) bool { return trs[i].Time > trs[j].Time })
	if maxIncidents > 0 && len(trs) > maxIncidents {
		trs = trs[:maxIncidents]
	}
	if trs == nil {
		trs = []Transition{}
	}
	ov.Incidents = trs
	return ov, nil
}
