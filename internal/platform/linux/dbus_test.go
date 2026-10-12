package linux

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// privateBus starts a throwaway dbus-daemon so the tests never touch the
// user's real session or system bus.
func privateBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address=1", "--address=unix:dir="+t.TempDir())
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("read bus address: %v", err)
	}
	return strings.TrimSpace(line)
}

type fakeNotifications struct {
	mu    sync.Mutex
	calls []map[string]any
}

func (f *fakeNotifications) Notify(app string, replaces uint32, icon, summary, body string, actions []string,
	hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, map[string]any{"app": app, "summary": summary, "body": body, "icon": icon,
		"urgency": hints["urgency"].Value()})
	return uint32(len(f.calls)), nil
}

func TestNotifierSendsToNotificationService(t *testing.T) {
	addr := privateBus(t)
	srv, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	fake := &fakeNotifications{}
	if err := srv.Export(fake, "/org/freedesktop/Notifications", "org.freedesktop.Notifications"); err != nil {
		t.Fatal(err)
	}
	if reply, err := srv.RequestName("org.freedesktop.Notifications", dbus.NameFlagDoNotQueue); err != nil ||
		reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request name: %v %v", reply, err)
	}
	n := &Notifier{Address: addr}
	defer n.Close()
	id, err := n.Notify(context.Background(), "Payments API is down", "TLS certificate expired 2 days ago.", "critical")
	if err != nil || id != 1 {
		t.Fatalf("notify: %d %v", id, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	c := fake.calls[0]
	if c["app"] != "Sebab" || c["summary"] != "Payments API is down" || c["urgency"] != byte(2) || c["icon"] != "network-error" {
		t.Errorf("call %+v", c)
	}
}

func TestNotifierReportsMissingService(t *testing.T) {
	addr := privateBus(t)
	n := &Notifier{Address: addr}
	defer n.Close()
	if _, err := n.Notify(context.Background(), "t", "b", "normal"); err == nil {
		t.Fatal("want an error when nobody owns org.freedesktop.Notifications")
	}
}

func TestWatchSleepReceivesPrepareForSleep(t *testing.T) {
	addr := privateBus(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan bool, 4)
	errc := make(chan error, 1)
	go func() { errc <- WatchSleep(ctx, addr, func(b bool) { got <- b }) }()
	emitter, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer emitter.Close()
	deadline := time.Now().Add(3 * time.Second)
	var seen []bool
	for len(seen) < 2 && time.Now().Before(deadline) {
		// The watcher subscribes asynchronously; keep emitting until both arrive.
		_ = emitter.Emit("/org/freedesktop/login1", "org.freedesktop.login1.Manager.PrepareForSleep", len(seen) == 0)
		select {
		case b := <-got:
			seen = append(seen, b)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if len(seen) < 2 || !seen[0] || seen[1] {
		t.Fatalf("want [true false], got %v", seen)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("watcher error: %v", err)
	}
}
