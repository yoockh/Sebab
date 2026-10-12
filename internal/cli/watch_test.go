package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/platform"
	"github.com/yoockh/sebab/lab"

	"github.com/zalando/go-keyring"
)

// syncBuffer is a goroutine-safe bytes.Buffer for capturing command output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type memKeyring struct {
	mu   sync.Mutex
	data map[string]string
	down bool
}

func (m *memKeyring) Get(s, u string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return "", errors.New("org.freedesktop.secrets was not provided by any .service files")
	}
	v, ok := m.data[s+"/"+u]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (m *memKeyring) Set(s, u, p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[s+"/"+u] = p
	return nil
}

func (m *memKeyring) Delete(s, u string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[s+"/"+u]; !ok {
		return keyring.ErrNotFound
	}
	delete(m.data, s+"/"+u)
	return nil
}

// world is an isolated environment: temp XDG dirs, the failure lab, a fake keyring.
type world struct {
	t       *testing.T
	lab     *lab.Lab
	vars    map[string]string
	keyring *memKeyring
	outputs []*syncBuffer // every stdout/stderr ever produced, for leak checks
	mu      sync.Mutex
}

func newWorld(t *testing.T) *world {
	t.Helper()
	l, err := lab.Start(lab.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	// Short base dir: Unix socket paths are limited to 108 bytes.
	base, err := os.MkdirTemp("", "sebab-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	for _, d := range []string{"home", "config", "state", "run"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ca := filepath.Join(base, "lab-ca.pem")
	if err := os.WriteFile(ca, l.Certs.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return &world{t: t, lab: l, keyring: &memKeyring{data: map[string]string{}}, vars: map[string]string{
		"HOME": filepath.Join(base, "home"), "XDG_CONFIG_HOME": filepath.Join(base, "config"),
		"XDG_STATE_HOME": filepath.Join(base, "state"), "XDG_RUNTIME_DIR": filepath.Join(base, "run"),
		"SEBAB_LAB_DNS": l.DNS.Addr, "LAB_CA": ca, "API_TOKEN": lab.Token,
	}}
}

func (w *world) env(stdin string) (Env, *syncBuffer, *syncBuffer) {
	out, errOut := &syncBuffer{}, &syncBuffer{}
	w.mu.Lock()
	w.outputs = append(w.outputs, out, errOut)
	w.mu.Unlock()
	return Env{Stdin: strings.NewReader(stdin), Stdout: out, Stderr: errOut,
		Getenv: func(k string) string { return w.vars[k] },
		hooks: &hooks{network: platform.Online(), dialer: w.lab.Dialer, keyring: w.keyring,
			canary:  []string{"tcp://127.0.0.1:" + strconv.Itoa(w.lab.HTTPPort)},
			dnsName: []string{"canary.lab.test"}}}, out, errOut
}

func (w *world) run(stdin string, args ...string) (int, string, string) {
	w.t.Helper()
	env, out, errOut := w.env(stdin)
	code := Run(context.Background(), args, env)
	return code, out.String(), errOut.String()
}

func (w *world) url(path string) string { return w.lab.HTTPURL("app.lab.test", path) }

func (w *world) tlsURL(server, path string) string { return w.lab.TLSURL(server, "app.lab.test", path) }

func (w *world) configPath() string {
	return filepath.Join(w.vars["XDG_CONFIG_HOME"], "sebab", "config.toml")
}

// assertNoLeak greps every captured output and the config for the canary secret.
func (w *world) assertNoLeak(extra ...string) {
	w.t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	texts := append([]string(nil), extra...)
	for _, b := range w.outputs {
		texts = append(texts, b.String())
	}
	if data, err := os.ReadFile(w.configPath()); err == nil {
		texts = append(texts, string(data))
	}
	for i, txt := range texts {
		if strings.Contains(txt, lab.Token) {
			w.t.Fatalf("canary secret leaked in output #%d: %q", i, txt)
		}
	}
}

func decodeCheck(t *testing.T, out string) event.Check {
	t.Helper()
	var c event.Check
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &c); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return c
}

type watcher struct {
	out    *syncBuffer
	cancel context.CancelFunc
	done   chan int
}

func (w *world) startWatch(args ...string) *watcher {
	env, out, _ := w.env("")
	ctx, cancel := context.WithCancel(context.Background())
	wt := &watcher{out: out, cancel: cancel, done: make(chan int, 1)}
	go func() { wt.done <- Run(ctx, append([]string{"watch"}, args...), env) }()
	w.t.Cleanup(func() { wt.stop(w.t) })
	return wt
}

func (wt *watcher) stop(t *testing.T) {
	wt.cancel()
	select {
	case <-wt.done:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not stop")
	}
}

// events parses the watch output so far.
func (wt *watcher) events() []map[string]any {
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(wt.out.String()))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func (wt *watcher) waitFor(t *testing.T, what string, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range wt.events() {
			if pred(ev) {
				return ev
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; got:\n%s", what, wt.out.String())
	return nil
}

func isType(typ string) func(map[string]any) bool {
	return func(ev map[string]any) bool { return ev["type"] == typ }
}

func checkFor(id string, status string) func(map[string]any) bool {
	return func(ev map[string]any) bool {
		m, _ := ev["monitor"].(map[string]any)
		return ev["type"] == "check" && m["id"] == id && (status == "" || ev["status"] == status)
	}
}

func countType(evs []map[string]any, pred func(map[string]any) bool) int {
	n := 0
	for _, ev := range evs {
		if pred(ev) {
			n++
		}
	}
	return n
}

func TestWatchRunsEngineServesSocketAndReloads(t *testing.T) {
	w := newWorld(t)
	if code, _, e := w.run("", "monitor", "add", "--name", "Web", "--target", w.url("/ok"), "--no-test"); code != 0 {
		t.Fatalf("add: %d %s", code, e)
	}
	wt := w.startWatch("--session", "test1")
	hello := wt.waitFor(t, "hello", isType("hello"))
	if hello["source"] != "embedded" || hello["monitors"].(float64) != 1 {
		t.Fatalf("hello %+v", hello)
	}
	wt.waitFor(t, "monitors event", isType("monitors"))
	wt.waitFor(t, "first check", checkFor("web", "up"))

	// Right click in the widget: refresh through the control socket.
	before := countType(wt.events(), checkFor("web", ""))
	if code, _, e := w.run("", "refresh"); code != 0 {
		t.Fatalf("refresh: %d %s", code, e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for countType(wt.events(), checkFor("web", "")) <= before {
		if time.Now().After(deadline) {
			t.Fatal("refresh did not trigger a check")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Story 1: a monitor added from another frontend appears without a restart.
	if code, _, e := w.run("", "monitor", "add", "--name", "Gateway", "--target", w.url("/status/502"), "--no-test"); code != 0 {
		t.Fatalf("add 2: %d %s", code, e)
	}
	wt.waitFor(t, "new monitor checked", checkFor("gateway", ""))

	// A second frontend attaches to the same engine instead of probing twice.
	second := w.startWatch()
	h2 := second.waitFor(t, "attached hello", isType("hello"))
	if h2["monitors"].(float64) != 2 {
		t.Fatalf("attached snapshot should list both monitors: %+v", h2)
	}
	second.waitFor(t, "snapshot check", checkFor("web", "up"))
	for _, ev := range second.events() {
		if ev["notify"] == true {
			t.Fatalf("a snapshot must never re-notify: %+v", ev)
		}
	}

	// Copy diagnosis through the engine.
	code, md, _ := w.run("", "report", "gateway")
	if code != 0 || !strings.Contains(md, "HTTP_BAD_GATEWAY") || !strings.Contains(md, "### Sebab diagnosis: Gateway") {
		t.Fatalf("report %d %q", code, md)
	}

	// The plugin restarts stalled streams with kill-session.
	if code, _, e := w.run("", "kill-session", "test1"); code != 0 {
		t.Fatalf("kill-session: %d %s", code, e)
	}
	w.assertNoLeak()
}

func TestWatchWithoutConfigExplains(t *testing.T) {
	w := newWorld(t)
	wt := w.startWatch("--embedded")
	ev := wt.waitFor(t, "config missing error", isType("error"))
	if ev["code"] != "CONFIG_MISSING" {
		t.Fatalf("error %+v", ev)
	}
}

func TestWatchReportsInvalidConfigAndRecovers(t *testing.T) {
	w := newWorld(t)
	_ = os.MkdirAll(filepath.Dir(w.configPath()), 0o700)
	_ = os.WriteFile(w.configPath(), []byte("[[monitor]]\nid=\"x\"\nname=\"X\"\nurl=\""+w.url("/ok")+"\"\ninterval=\"5s\"\n"), 0o600)
	wt := w.startWatch("--embedded")
	ev := wt.waitFor(t, "invalid config error", isType("error"))
	if ev["code"] != "CONFIG_INVALID" || !strings.Contains(ev["message"].(string), "interval below 30s") {
		t.Fatalf("error %+v", ev)
	}
	_ = os.WriteFile(w.configPath(), []byte("[[monitor]]\nid=\"x\"\nname=\"X\"\nurl=\""+w.url("/ok")+"\"\n"), 0o600)
	wt.waitFor(t, "check after fix", checkFor("x", "up"))
}

// Story 7 / guardrail: the secret never appears in the event stream, in
// notifications, in reports or in any command output, even when the request
// fails and is reported in detail.
func TestSecretNeverLeaksThroughStream(t *testing.T) {
	w := newWorld(t)
	code, _, e := w.run(lab.Token+"\n", "monitor", "add", "--name", "Admin", "--target",
		w.url("/auth/header")+"?api_key=query-key-9d1", "--auth", "header", "--header-name", "X-API-Key",
		"--secret", "admin-token", "--secret-stdin", "--no-test")
	if code != 0 {
		t.Fatalf("add: %d %s", code, e)
	}
	// Make the request fail with the secret in play: the server wants another key.
	if code, _, e := w.run("rejected-"+lab.Token+"\n", "secret", "set", "admin-token"); code != 0 {
		t.Fatalf("set: %s", e)
	}
	cfg, err := config.Load(w.configPath())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Defaults.FailThreshold = 1
	if err := cfg.Save(w.configPath()); err != nil {
		t.Fatal(err)
	}
	wt := w.startWatch("--embedded")
	ev := wt.waitFor(t, "auth failure", checkFor("admin", "down"))
	cause, _ := ev["cause"].(map[string]any)
	if cause["code"] != "HTTP_AUTH" || ev["notify"] != true {
		t.Fatalf("want a notifying HTTP_AUTH check, got %+v", ev)
	}
	_, md, _ := w.run("", "report", "admin")
	_, human, _ := w.run("", "check", "--monitor", "admin")
	_, list, _ := w.run("", "monitor", "list", "--json")
	if !strings.Contains(list, "api_key=[REDACTED]") || strings.Contains(wt.out.String(), "query-key-9d1") {
		t.Errorf("list should redact sensitive query parameters: %s", list)
	}
	w.assertNoLeak(wt.out.String(), md, human, list)
}
