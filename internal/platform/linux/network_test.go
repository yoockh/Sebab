package linux

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/yoockh/sebab/internal/platform"
)

const routeV4Default = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
wlo1	00000000	0101A8C0	0003	0	0	600	00000000	0	0	0
docker0	000011AC	00000000	0001	0	0	0	0000FFFF	0	0	0
`

const routeV4None = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
docker0	000011AC	00000000	0001	0	0	0	0000FFFF	0	0	0
`

const routeV6RejectOnly = `00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo
fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001     wlo1
`

const routeV6Default = `00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000258 0000000d 00000000 00000003     wlo1
`

func fixture(t *testing.T, v4, v6 string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	if v4 != "" {
		_ = os.WriteFile(filepath.Join(dir, "net", "route"), []byte(v4), 0o644)
	}
	if v6 != "" {
		_ = os.WriteFile(filepath.Join(dir, "net", "ipv6_route"), []byte(v6), 0o644)
	}
	return dir
}

func TestDefaultRoute(t *testing.T) {
	cases := []struct {
		name   string
		v4, v6 string
		want   bool
	}{
		{"ipv4 default", routeV4Default, routeV6RejectOnly, true},
		{"no default", routeV4None, routeV6RejectOnly, false},
		{"ipv6 only default", routeV4None, routeV6Default, true},
		{"no ipv6 file", routeV4Default, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &Network{ProcRoot: fixture(t, tc.v4, tc.v6)}
			got, err := n.HasDefaultRoute()
			if err != nil || got != tc.want {
				t.Errorf("got %v err %v want %v", got, err, tc.want)
			}
		})
	}
}

func TestDefaultRouteMissingProc(t *testing.T) {
	n := &Network{ProcRoot: t.TempDir()}
	if _, err := n.HasDefaultRoute(); err == nil {
		t.Error("expected error when procfs is unreadable")
	}
}

func TestLinksFlags(t *testing.T) {
	n := &Network{ListInterfaces: func() ([]net.Interface, error) {
		return []net.Interface{
			{Name: "lo", Flags: net.FlagUp | net.FlagLoopback | net.FlagRunning},
			{Name: "wg0", Flags: net.FlagUp | net.FlagRunning | net.FlagPointToPoint},
			{Name: "eth0", Flags: net.FlagUp},
		}, nil
	}}
	links, err := n.Links()
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := platform.FindLink(links, "wg0"); !ok || !l.Up || !l.Running {
		t.Errorf("wg0 %+v", l)
	}
	if !platform.HasActiveLink(links) {
		t.Error("wg0 counts as an active link")
	}
	links = links[2:]
	if platform.HasActiveLink(links) {
		t.Error("eth0 without carrier is not active")
	}
}

func TestRealSystemDoesNotError(t *testing.T) {
	n := NewNetwork()
	if _, err := n.Links(); err != nil {
		t.Fatalf("links: %v", err)
	}
	if _, err := os.Stat("/proc/net/route"); err == nil {
		if _, err := n.HasDefaultRoute(); err != nil {
			t.Fatalf("routes: %v", err)
		}
	}
}
