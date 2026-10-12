package linux

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// NameStatus describes a well-known D-Bus name on the session bus.
type NameStatus struct {
	Owned       bool
	Activatable bool
	OwnerComm   string // process name of the owner, when known
}

// SessionName inspects a name on the session bus.
func SessionName(ctx context.Context, name string) (NameStatus, error) {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return NameStatus{}, fmt.Errorf("connect to the session bus: %w", err)
	}
	defer conn.Close()
	bus := conn.BusObject()
	var st NameStatus
	if err := bus.CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, name).Store(&st.Owned); err != nil {
		return st, fmt.Errorf("NameHasOwner: %w", err)
	}
	var activatable []string
	if err := bus.CallWithContext(ctx, "org.freedesktop.DBus.ListActivatableNames", 0).Store(&activatable); err == nil {
		for _, n := range activatable {
			if n == name {
				st.Activatable = true
			}
		}
	}
	if st.Owned {
		var pid uint32
		if err := bus.CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, name).Store(&pid); err == nil {
			if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
				st.OwnerComm = strings.TrimSpace(string(comm))
			}
		}
	}
	return st, nil
}

// PingAllowed reports whether unprivileged ICMP sockets are allowed for any of
// the given group ids (net.ipv4.ping_group_range).
func PingAllowed(procRoot string, gids []int) (bool, string, error) {
	data, err := os.ReadFile(procRoot + "/sys/net/ipv4/ping_group_range")
	if err != nil {
		return false, "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return false, string(data), fmt.Errorf("unexpected ping_group_range %q", data)
	}
	lo, err1 := strconv.Atoi(fields[0])
	hi, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return false, string(data), fmt.Errorf("unexpected ping_group_range %q", data)
	}
	rng := fields[0] + " " + fields[1]
	for _, g := range gids {
		if g >= lo && g <= hi {
			return true, rng, nil
		}
	}
	return false, rng, nil
}

// OSRelease parses /etc/os-release style files.
func OSRelease(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return out
}

// Family maps os-release ID/ID_LIKE to a package family: arch, debian, fedora, suse or "".
func Family(osr map[string]string) string {
	ids := strings.Fields(osr["ID"] + " " + osr["ID_LIKE"])
	for _, id := range ids {
		switch id {
		case "arch", "manjaro", "endeavouros", "cachyos":
			return "arch"
		case "debian", "ubuntu", "linuxmint", "pop":
			return "debian"
		case "fedora", "rhel", "centos":
			return "fedora"
		case "opensuse", "suse", "opensuse-tumbleweed":
			return "suse"
		}
	}
	return ""
}

// Install returns the install command for packages on a distro family.
func Install(family string, pkgs ...string) string {
	p := strings.Join(pkgs, " ")
	switch family {
	case "arch":
		return "sudo pacman -S " + p
	case "debian":
		return "sudo apt install " + p
	case "fedora":
		return "sudo dnf install " + p
	case "suse":
		return "sudo zypper install " + p
	}
	return "install " + p + " with your package manager"
}

// DefaultTimeout bounds D-Bus calls made by doctor.
const DefaultTimeout = 2 * time.Second
