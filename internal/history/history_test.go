package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yoockh/sebab/internal/event"
)

func check(id string, at time.Time, status event.Status, code string, ms int64) event.Check {
	c := event.NewCheck(at)
	c.Monitor = event.MonitorRef{ID: id, Name: "Mon " + id}
	c.Status, c.Observed = status, status
	if code != "" {
		c.Cause = &event.Cause{Code: code, Summary: "summary of " + code}
		c.Side = event.SideTheirs
	}
	if ms >= 0 {
		c.Timings.Total = event.MS(time.Duration(ms) * time.Millisecond)
	}
	return c
}

func TestRecordSamplesTransitionsAndRecent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 7, 3)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	steps := []event.Check{
		check("api", now.Add(-5*time.Minute), event.StatusUp, "", 90),
		check("api", now.Add(-4*time.Minute), event.StatusUp, "", 110),
		check("api", now.Add(-3*time.Minute), event.StatusDown, "HTTP_BAD_GATEWAY", 40),
		check("api", now.Add(-2*time.Minute), event.StatusDown, "HTTP_BAD_GATEWAY", 41),
		check("api", now.Add(-1*time.Minute), event.StatusUp, "", 100),
	}
	for _, c := range steps {
		if err := s.Record(c); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	samples, err := s.Samples("api", now.Add(-time.Hour), now)
	if err != nil || len(samples) != 5 {
		t.Fatalf("samples %d %v", len(samples), err)
	}
	if samples[2].TotalMS != -1 || samples[2].Code != "HTTP_BAD_GATEWAY" {
		t.Errorf("down sample %+v", samples[2])
	}
	trs, _ := s.Transitions("api", now.Add(-time.Hour))
	if len(trs) != 2 || trs[0].To != event.StatusDown || trs[0].Summary == "" || trs[1].To != event.StatusUp {
		t.Fatalf("transitions %+v", trs)
	}
	if r := s.Recent("api"); len(r) != 3 || r[2] != 100 {
		t.Errorf("recent ring %v", r)
	}
	// Reopening seeds the ring and the last status from disk.
	s2, err := Open(dir, 7, 30)
	if err != nil {
		t.Fatal(err)
	}
	if r := s2.Recent("api"); len(r) != 5 {
		t.Errorf("reopened ring %v", r)
	}
	if err := s2.Record(check("api", now, event.StatusUp, "", 95)); err != nil {
		t.Fatal(err)
	}
	if trs, _ := s2.Transitions("api", now.Add(-time.Hour)); len(trs) != 2 {
		t.Errorf("no transition expected for up -> up after reopen, got %d", len(trs))
	}
	st := Summarize(samples)
	if st.Checks != 5 || st.Availability != 0.6 || st.P50 != 100 || st.P95 != 110 {
		t.Errorf("stats %+v", st)
	}
	info, _ := os.Stat(filepath.Join(dir, "incidents.jsonl"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("history files must be private: %v", info.Mode())
	}
}

func TestPruneOldDays(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "checks-2000-01-01.jsonl")
	_ = os.WriteFile(old, []byte(`{"t":1,"m":"x","s":"up","ms":1}`+"\n"), 0o600)
	s, err := Open(dir, 7, 30)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Record(check("x", time.Now(), event.StatusUp, "", 5)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old day file should be pruned")
	}
}

func TestFirstFailureIsATransition(t *testing.T) {
	s, _ := Open(t.TempDir(), 7, 30)
	defer s.Close()
	now := time.Now()
	_ = s.Record(check("db", now, event.StatusDown, "TCP_REFUSED", -1))
	trs, _ := s.Transitions("db", now.Add(-time.Minute))
	if len(trs) != 1 || trs[0].From != "" || trs[0].To != event.StatusDown {
		t.Fatalf("%+v", trs)
	}
	if r := s.Recent("db"); len(r) != 1 || r[0] != -1 {
		t.Fatalf("failed check should be -1 in the ring: %v", r)
	}
}
