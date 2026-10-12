// Package history keeps a local record of checks and incidents under
// $XDG_STATE_HOME/sebab/history: one compact JSON line per check in a file per
// UTC day (checks-YYYY-MM-DD.jsonl), and status transitions in incidents.jsonl.
// Old day files are pruned after the retention period. Nothing leaves the machine.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yoockh/sebab/internal/event"
)

// Sample is one stored check.
type Sample struct {
	Time    int64        `json:"t"`           // unix milliseconds
	Monitor string       `json:"m"`           // monitor id
	Status  event.Status `json:"s"`           // displayed status
	Code    string       `json:"c,omitempty"` // cause code
	TotalMS int64        `json:"ms"`          // total time, -1 when the check did not complete
}

// Transition is one status change of a monitor.
type Transition struct {
	Time    int64        `json:"t"`
	Monitor string       `json:"m"`
	Name    string       `json:"n"`
	From    event.Status `json:"from"`
	To      event.Status `json:"to"`
	Code    string       `json:"c,omitempty"`
	Summary string       `json:"sum,omitempty"`
	Side    event.Side   `json:"side,omitempty"`
}

// Store writes and reads history. Safe for concurrent use.
type Store struct {
	Dir      string
	KeepDays int // day files older than this are removed (default 7)

	mu       sync.Mutex
	day      string
	file     *os.File
	last     map[string]event.Status // last stored status per monitor, for transitions
	recent   map[string][]int64      // ring of recent totals per monitor
	ringSize int
}

// Open prepares the directory and loads recent samples for sparklines.
func Open(dir string, keepDays, ringSize int) (*Store, error) {
	if keepDays <= 0 {
		keepDays = 7
	}
	if ringSize <= 0 {
		ringSize = 30
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create history dir: %w", err)
	}
	s := &Store{Dir: dir, KeepDays: keepDays, last: map[string]event.Status{}, recent: map[string][]int64{},
		ringSize: ringSize}
	samples, err := s.Samples("", time.Now().Add(-48*time.Hour), time.Now())
	if err != nil {
		return nil, err
	}
	for _, smp := range samples {
		s.remember(smp)
	}
	return s, nil
}

func (s *Store) remember(smp Sample) {
	s.last[smp.Monitor] = smp.Status
	r := append(s.recent[smp.Monitor], smp.TotalMS)
	if len(r) > s.ringSize {
		r = r[len(r)-s.ringSize:]
	}
	s.recent[smp.Monitor] = r
}

// Recent returns the latest totals for a monitor (oldest first, -1 = failed).
func (s *Store) Recent(id string) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.recent[id]...)
}

// Record stores a check event and, when the status changed, a transition.
func (s *Store) Record(c event.Check) error {
	t := c.TS.Time()
	if t.IsZero() {
		t = time.Now()
	}
	smp := Sample{Time: t.UnixMilli(), Monitor: c.Monitor.ID, Status: c.Status, TotalMS: -1}
	if c.Cause != nil {
		smp.Code = c.Cause.Code
	}
	if c.Timings.Total != nil && c.Observed != event.StatusDown && c.Status != event.StatusUnknown {
		smp.TotalMS = *c.Timings.Total
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, seen := s.last[c.Monitor.ID]
	if err := s.appendSample(t, smp); err != nil {
		return err
	}
	s.remember(smp)
	if seen && prev != c.Status || !seen && c.Status != event.StatusUp {
		tr := Transition{Time: smp.Time, Monitor: c.Monitor.ID, Name: c.Monitor.Name, From: prev, To: c.Status,
			Code: smp.Code, Side: c.Side}
		if c.Cause != nil {
			tr.Summary = c.Cause.Summary
		}
		return s.appendLine(filepath.Join(s.Dir, "incidents.jsonl"), tr)
	}
	return nil
}

func (s *Store) appendSample(t time.Time, smp Sample) error {
	day := t.UTC().Format("2006-01-02")
	if day != s.day || s.file == nil {
		if s.file != nil {
			s.file.Close()
		}
		f, err := os.OpenFile(filepath.Join(s.Dir, "checks-"+day+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("open history file: %w", err)
		}
		s.file, s.day = f, day
		s.prune(t)
	}
	line, err := json.Marshal(smp)
	if err != nil {
		return err
	}
	_, err = s.file.Write(append(line, '\n'))
	return err
}

func (s *Store) appendLine(path string, v any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// prune removes day files older than KeepDays and trims incidents to 10x that.
func (s *Store) prune(now time.Time) {
	cutoff := now.UTC().AddDate(0, 0, -s.KeepDays).Format("2006-01-02")
	entries, _ := os.ReadDir(s.Dir)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "checks-") && strings.HasSuffix(name, ".jsonl") {
			if strings.TrimSuffix(strings.TrimPrefix(name, "checks-"), ".jsonl") < cutoff {
				_ = os.Remove(filepath.Join(s.Dir, name))
			}
		}
	}
	keep := now.AddDate(0, 0, -10*s.KeepDays).UnixMilli()
	trs, err := s.readTransitions()
	if err != nil {
		return
	}
	var kept []Transition
	for _, t := range trs {
		if t.Time >= keep {
			kept = append(kept, t)
		}
	}
	if len(kept) == len(trs) {
		return
	}
	var buf bytes.Buffer
	for _, t := range kept {
		line, _ := json.Marshal(t)
		buf.Write(append(line, '\n'))
	}
	_ = os.WriteFile(filepath.Join(s.Dir, "incidents.jsonl"), buf.Bytes(), 0o600)
}

// Close closes the current day file.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
}

// Samples returns stored checks for a monitor ("" = all) in [from, to].
func (s *Store) Samples(id string, from, to time.Time) ([]Sample, error) {
	var out []Sample
	for d := from.UTC().Truncate(24 * time.Hour); !d.After(to.UTC()); d = d.Add(24 * time.Hour) {
		path := filepath.Join(s.Dir, "checks-"+d.Format("2006-01-02")+".jsonl")
		err := readLines(path, func(line []byte) {
			var smp Sample
			if json.Unmarshal(line, &smp) != nil {
				return
			}
			if (id == "" || smp.Monitor == id) && smp.Time >= from.UnixMilli() && smp.Time <= to.UnixMilli() {
				out = append(out, smp)
			}
		})
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}

// Transitions returns status changes for a monitor ("" = all) since from.
func (s *Store) Transitions(id string, from time.Time) ([]Transition, error) {
	all, err := s.readTransitions()
	if err != nil {
		return nil, err
	}
	var out []Transition
	for _, t := range all {
		if (id == "" || t.Monitor == id) && t.Time >= from.UnixMilli() {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *Store) readTransitions() ([]Transition, error) {
	var out []Transition
	err := readLines(filepath.Join(s.Dir, "incidents.jsonl"), func(line []byte) {
		var t Transition
		if json.Unmarshal(line, &t) == nil {
			out = append(out, t)
		}
	})
	return out, err
}

func readLines(path string, fn func([]byte)) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read history: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fn(sc.Bytes())
	}
	return sc.Err()
}

// Stats summarises samples: availability (share of up/degraded checks among
// checks that were judged) and latency percentiles of completed checks.
type Stats struct {
	Checks       int
	Availability float64 // 0..1
	P50, P95     int64
}

// Summarize computes Stats over samples.
func Summarize(samples []Sample) Stats {
	var st Stats
	var lat []int64
	judged, good := 0, 0
	for _, s := range samples {
		st.Checks++
		switch s.Status {
		case event.StatusUp, event.StatusDegraded:
			judged++
			good++
		case event.StatusDown:
			judged++
		}
		if s.TotalMS >= 0 {
			lat = append(lat, s.TotalMS)
		}
	}
	if judged > 0 {
		st.Availability = float64(good) / float64(judged)
	}
	if len(lat) > 0 {
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		st.P50 = lat[len(lat)*50/100]
		st.P95 = lat[min(len(lat)-1, len(lat)*95/100)]
	}
	return st
}
