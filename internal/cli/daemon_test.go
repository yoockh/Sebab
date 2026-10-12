package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/server"
	"github.com/yoockh/sebab/lab"
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
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				buf := make([]byte, 1<<20)
				n := goruntime.Stack(buf, true)
				w.t.Errorf("daemon did not stop\n%s", buf[:n])
			}
		})
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

func TestWaybarCommand(t *testing.T) {
	w := newWorld(t)
	if code, _, e := w.run("", "monitor", "add", "--name", "Gateway", "--target", w.url("/status/502"), "--no-test"); code != 0 {
		t.Fatal(e)
	}
	_ = os.MkdirAll(filepath.Dir(w.configPath()), 0o700)
	cfg, _ := config.Load(w.configPath())
	cfg.Defaults.FailThreshold = 1
	_ = cfg.Save(w.configPath())
	env, out, _ := w.env("")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- Run(ctx, []string{"waybar", "--embedded"}, env) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), `"alt":"down"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no down output:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.HasPrefix(out.String(), `{"text":"","alt":"starting"`) {
		t.Errorf("first line should be the starting state:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `{"text":"1","alt":"down","class":["down"],"tooltip":"Gateway: down, gateway returned 502, the upstream app is likely down"`) {
		t.Errorf("down line:\n%s", out.String())
	}
	// Waybar does not show notifications, so the embedded engine does.
	w.waitNotified(t, "Gateway is down")
}

// Phase 2: watch attaches to the daemon and takes over with an embedded engine
// when the daemon goes away.
func TestWatchFallsBackWhenDaemonStops(t *testing.T) {
	w := newWorld(t)
	if code, _, e := w.run("", "monitor", "add", "--name", "Web", "--target", w.url("/ok"), "--no-test"); code != 0 {
		t.Fatal(e)
	}
	stopDaemon := w.startDaemon()
	wt := w.startWatch()
	wt.waitFor(t, "daemon hello", func(ev map[string]any) bool { return ev["type"] == "hello" && ev["source"] == "daemon" })
	stopDaemon()
	wt.waitFor(t, "embedded hello", func(ev map[string]any) bool { return ev["type"] == "hello" && ev["source"] == "embedded" })
	wt.waitFor(t, "check from the embedded engine", checkFor("web", "up"))
	if code, _, _ := w.run("", "refresh"); code != 0 {
		t.Fatal("the embedded engine should now serve the socket")
	}
}

func TestHistoryAndIncidentReport(t *testing.T) {
	w := newWorld(t)
	_ = os.MkdirAll(filepath.Dir(w.configPath()), 0o700)
	cfg := &config.Config{Defaults: config.Defaults{FailThreshold: 1}}
	_ = cfg.Save(w.configPath())
	for _, args := range [][]string{
		{"monitor", "add", "--name", "Web", "--target", w.url("/ok"), "--no-test"},
		{"monitor", "add", "--name", "Gateway", "--target", w.url("/status/502"), "--no-test"},
	} {
		if code, _, e := w.run("", args...); code != 0 {
			t.Fatal(e)
		}
	}
	wt := w.startWatch("--embedded")
	wt.waitFor(t, "gateway down", checkFor("gateway", "down"))
	if code, _, e := w.run("", "refresh"); code != 0 {
		t.Fatal(e)
	}
	ev := wt.waitFor(t, "second web check with recent_ms", func(ev map[string]any) bool {
		r, _ := ev["recent_ms"].([]any)
		return checkFor("web", "up")(ev) && len(r) >= 2
	})
	_ = ev
	code, out, _ := w.run("", "history", "gateway")
	if code != 0 || !strings.Contains(out, "0.00% available") || !strings.Contains(out, "start -> down") && !strings.Contains(out, "- -> down") {
		t.Fatalf("history: %d\n%s", code, out)
	}
	code, out, _ = w.run("", "history", "web")
	if code != 0 || !strings.Contains(out, "100.00% available") || !strings.Contains(out, "latency ") {
		t.Fatalf("history web: %d\n%s", code, out)
	}
	code, md, _ := w.run("", "report", "gateway")
	if code != 0 || !strings.Contains(md, "#### Last 24 h") || !strings.Contains(md, "-> down | HTTP_BAD_GATEWAY") {
		t.Fatalf("incident report:\n%s", md)
	}
	wt.stop(t)
	code, md, _ = w.run("", "report", "gateway", "--fresh")
	if code != 0 || !strings.Contains(md, "#### Last 24 h") {
		t.Fatalf("fresh incident report:\n%s", md)
	}
	w.assertNoLeak()
}

func TestSparkline(t *testing.T) {
	if got := Sparkline([]int64{0, 50, 100, -1}, 10); got != "▁▄█x" {
		t.Errorf("got %q", got)
	}
	if got := Sparkline([]int64{1, 2, 3}, 2); got != "▅█" {
		t.Errorf("width: %q", got)
	}
}

func TestExternalChannelsEndToEnd(t *testing.T) {
	w := newWorld(t)
	var mu sync.Mutex
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.Header.Get("Authorization")+" "+string(b))
		mu.Unlock()
	}))
	defer hook.Close()
	if code, _, e := w.run(lab.Token+"\n", "secret", "set", "hook-token"); code != 0 {
		t.Fatal(e)
	}
	_ = os.MkdirAll(filepath.Dir(w.configPath()), 0o700)
	cfg := &config.Config{Defaults: config.Defaults{FailThreshold: 1},
		Notify: []config.Channel{{Type: "webhook", URL: hook.URL + "/sebab", Secret: "hook-token"}}}
	if err := cfg.Save(w.configPath()); err != nil {
		t.Fatal(err)
	}
	code, out, _ := w.run("", "notify-test")
	if code != 0 || !strings.Contains(out, "webhook: sent") || !strings.Contains(out, "desktop: sent") {
		t.Fatalf("notify-test: %d %s", code, out)
	}
	if code, _, e := w.run("", "monitor", "add", "--name", "Gateway", "--target", w.url("/status/502"), "--no-test"); code != 0 {
		t.Fatal(e)
	}
	wt := w.startWatch("--embedded")
	wt.waitFor(t, "down", checkFor("gateway", "down"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhook calls: %v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(got[1], `"title":"Gateway is down"`) || !strings.HasPrefix(got[1], "Bearer ") {
		t.Fatalf("webhook payload %q", got[1])
	}
	w.assertNoLeak(wt.out.String())
}

// Item 7: the panel's save path persists across a daemon restart, starting
// from no config at all (CONFIG_MISSING).
func TestPanelSavePathPersistsAcrossDaemonRestart(t *testing.T) {
	w := newWorld(t)
	stop := w.startDaemon()
	front := w.startWatch()
	front.waitFor(t, "config missing", func(ev map[string]any) bool { return ev["type"] == "error" && ev["code"] == "CONFIG_MISSING" })
	// Exactly the argv panel.luau builds for "Save" (see save() and commonArgs()).
	code, out, errOut := w.run("", "monitor", "add", "--json", "--no-test", "--name", "Example", "--target", w.url("/ok"),
		"--interval", "60s", "--vpn=", "--insecure=false")
	if code != 0 || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("panel save argv: %d %s %s", code, out, errOut)
	}
	st, err := os.Stat(w.configPath())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config.toml not written with 0600: %v %v", st, err)
	}
	if d, _ := os.Stat(filepath.Dir(w.configPath())); d.Mode().Perm() != 0o700 {
		t.Errorf("config dir mode %v", d.Mode().Perm())
	}
	data, _ := os.ReadFile(w.configPath())
	if !strings.Contains(string(data), "id = 'example'") || !strings.Contains(string(data), "name = 'Example'") {
		t.Fatalf("config content:\n%s", data)
	}
	// The running engine picks it up without a restart (reload over the socket).
	front.waitFor(t, "monitor checked by the running daemon", checkFor("example", "up"))
	front.stop(t)
	stop()

	// Restart the daemon: the monitor is still there.
	w.startDaemon()
	again := w.startWatch()
	ev := again.waitFor(t, "monitors after restart", isType("monitors"))
	list, _ := ev["monitors"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != "example" {
		t.Fatalf("monitors after restart: %+v", ev)
	}
	code, out, _ = w.run("", "monitor", "list", "--json")
	if code != 0 || !strings.Contains(out, `"id":"example"`) {
		t.Fatalf("list after restart: %s", out)
	}
}
