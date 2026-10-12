package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/server"
)

func (w *world) startDaemon() func() {
	env, _, errOut := w.env("")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- Run(ctx, []string{"daemon"}, env) }()
	sock := filepath.Join(w.vars["XDG_RUNTIME_DIR"], "sebab", "sebab.sock")
	deadline := time.Now().Add(5 * time.Second)
	for !server.Alive(sock) {
		if time.Now().After(deadline) {
			w.t.Fatalf("daemon did not start: %s", errOut.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			w.t.Fatal("daemon did not stop")
		}
	}
	w.t.Cleanup(stop)
	return stop
}

func (w *world) waitNotified(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range w.notifications() {
			if strings.Contains(n, substr) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no desktop notification containing %q; got %v", substr, w.notifications())
}

func TestDaemonNotificationOwnership(t *testing.T) {
	w := newWorld(t)
	_ = os.MkdirAll(filepath.Dir(w.configPath()), 0o700)
	cfg := &config.Config{Defaults: config.Defaults{FailThreshold: 1}}
	if err := cfg.Save(w.configPath()); err != nil {
		t.Fatal(err)
	}
	w.startDaemon()
	if code, _, _ := w.run("", "daemon"); code != ExitInternal {
		t.Fatal("a second daemon must refuse to start")
	}

	// A Noctalia-style frontend that displays notifications is attached:
	// the daemon must stay quiet so the user sees each alert once.
	front := w.startWatch()
	hello := front.waitFor(t, "hello", isType("hello"))
	if hello["source"] != "daemon" {
		t.Fatalf("watch should attach to the daemon: %+v", hello)
	}
	if code, _, e := w.run("", "monitor", "add", "--name", "Gw", "--target", w.url("/status/502"), "--no-test"); code != 0 {
		t.Fatal(e)
	}
	ev := front.waitFor(t, "down with notify", func(ev map[string]any) bool {
		return checkFor("gw", "down")(ev) && ev["notify"] == true
	})
	_ = ev
	time.Sleep(200 * time.Millisecond)
	if n := w.notifications(); len(n) != 0 {
		t.Fatalf("daemon must not notify while a notifying frontend is attached: %v", n)
	}
	front.stop(t)

	// Nobody displays notifications now: the daemon sends them over D-Bus.
	time.Sleep(100 * time.Millisecond)
	if code, _, e := w.run("", "monitor", "add", "--name", "Gw2", "--target", w.url("/status/503"), "--no-test"); code != 0 {
		t.Fatal(e)
	}
	w.waitNotified(t, "Gw2 is down: Service unavailable (503).")

	// A consumer that does not display notifications (Waybar) changes nothing.
	bar := w.startWatch("--frontend-notifies=false")
	bar.waitFor(t, "hello", isType("hello"))
	if code, _, e := w.run("", "monitor", "add", "--name", "Gw3", "--target", w.url("/status/500"), "--no-test"); code != 0 {
		t.Fatal(e)
	}
	w.waitNotified(t, "Gw3 is down")
	count := 0
	for _, n := range w.notifications() {
		if strings.HasPrefix(n, "Gw2 is down") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("each alert must be delivered once, Gw2 seen %d times", count)
	}
	w.assertNoLeak()
}
