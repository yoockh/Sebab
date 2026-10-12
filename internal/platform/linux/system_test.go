package linux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPingAllowed(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "sys/net/ipv4"), 0o755)
	write := func(s string) {
		_ = os.WriteFile(filepath.Join(dir, "sys/net/ipv4/ping_group_range"), []byte(s), 0o644)
	}
	write("0\t2147483647\n")
	if ok, _, err := PingAllowed(dir, []int{1000}); err != nil || !ok {
		t.Fatalf("systemd default should allow: %v %v", ok, err)
	}
	write("1\t0\n")
	if ok, rng, err := PingAllowed(dir, []int{1000}); err != nil || ok || rng != "1 0" {
		t.Fatalf("kernel default should deny: %v %q %v", ok, rng, err)
	}
	write("garbage")
	if _, _, err := PingAllowed(dir, []int{1000}); err == nil {
		t.Fatal("garbage should error")
	}
}

func TestOSReleaseAndFamily(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	_ = os.WriteFile(path, []byte("NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\n# c\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\n"), 0o644)
	osr := OSRelease(path)
	if osr["PRETTY_NAME"] != "Ubuntu 24.04 LTS" || Family(osr) != "debian" {
		t.Fatalf("%v %s", osr, Family(osr))
	}
	cases := map[string]string{"arch": "arch", "fedora": "fedora", "opensuse-tumbleweed": "suse", "gentoo": ""}
	for id, want := range cases {
		if got := Family(map[string]string{"ID": id}); got != want {
			t.Errorf("%s: %q want %q", id, got, want)
		}
	}
	if Install("arch", "gnome-keyring") != "sudo pacman -S gnome-keyring" || Install("debian", "x") != "sudo apt install x" {
		t.Error("install hints")
	}
	if len(OSRelease(filepath.Join(t.TempDir(), "missing"))) != 0 {
		t.Error("missing file should give an empty map")
	}
}
