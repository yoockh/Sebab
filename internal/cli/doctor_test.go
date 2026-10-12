package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	linuxplat "github.com/yoockh/sebab/internal/platform/linux"
)

func withDoctorDeps(t *testing.T, notif linuxplat.NameStatus, ping bool, family string) {
	t.Helper()
	testDoctorDeps = &doctorDeps{
		sessionName: func(_ context.Context, name string) (linuxplat.NameStatus, error) {
			if name == "org.freedesktop.Notifications" {
				return notif, nil
			}
			return linuxplat.NameStatus{}, nil
		},
		pingAllowed: func() (bool, string, error) { return ping, "1 0", nil },
		osRelease:   map[string]string{"ID": family, "PRETTY_NAME": "Test Linux"},
		lookPath:    func(string) (string, error) { return "", errors.New("not found") },
	}
	t.Cleanup(func() { testDoctorDeps = nil })
}

func doctorFindings(t *testing.T, w *world, args ...string) (int, []finding) {
	t.Helper()
	code, out, _ := w.run("", append([]string{"doctor", "--json"}, args...)...)
	var fs []finding
	if err := json.Unmarshal([]byte(out), &fs); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return code, fs
}

func find(fs []finding, area string) finding {
	for _, f := range fs {
		if f.Area == area {
			return f
		}
	}
	return finding{}
}

func TestDoctorNoKeyringOnArch(t *testing.T) {
	w := newWorld(t)
	w.keyring.down = true
	withDoctorDeps(t, linuxplat.NameStatus{Owned: true, OwnerComm: "noctalia"}, false, "arch")
	code, fs := doctorFindings(t, w)
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	k := find(fs, "keyring")
	if k.State != stateWarn || !strings.Contains(k.Fix, "sudo pacman -S gnome-keyring") || !strings.Contains(k.Detail, "env:VAR") {
		t.Errorf("keyring finding %+v", k)
	}
	if n := find(fs, "notifications"); n.State != stateOK || !strings.Contains(n.Detail, "noctalia") {
		t.Errorf("notifications %+v", n)
	}
	if i := find(fs, "icmp"); i.State != stateInfo || !strings.Contains(i.Fix, "ping_group_range") {
		t.Errorf("icmp %+v", i)
	}
	if c := find(fs, "config"); c.State != stateWarn {
		t.Errorf("missing config should warn: %+v", c)
	}
}

func TestDoctorMissingSecretFailsAndDebianHints(t *testing.T) {
	w := newWorld(t)
	withDoctorDeps(t, linuxplat.NameStatus{}, true, "ubuntu")
	if code, _, _ := w.run("", "monitor", "add", "--name", "A", "--target", w.url("/ok"), "--auth", "bearer",
		"--secret", "nope", "--no-test"); code != 0 {
		t.Fatal("add")
	}
	w.keyring.down = true
	code, fs := doctorFindings(t, w)
	if code != ExitDegraded {
		t.Fatalf("a missing secret should fail doctor, exit %d", code)
	}
	if s := find(fs, "secret"); s.State != stateFail || s.Fix != "sebab secret set nope" {
		t.Errorf("secret finding %+v", s)
	}
	if n := find(fs, "notifications"); n.State != stateWarn || !strings.Contains(n.Fix, "sudo apt install") {
		t.Errorf("notifications %+v", n)
	}
	if k := find(fs, "keyring"); !strings.Contains(k.Fix, "sudo apt install gnome-keyring") {
		t.Errorf("keyring fix %+v", k)
	}
}

func TestDoctorOnlineCanaryUsesConfig(t *testing.T) {
	w := newWorld(t)
	withDoctorDeps(t, linuxplat.NameStatus{Owned: true}, true, "fedora")
	_, fs := doctorFindings(t, w, "--online")
	if c := find(fs, "canary"); c.State != stateOK {
		t.Errorf("canary %+v", c)
	}
}
