package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/platform"
	linuxplat "github.com/yoockh/sebab/internal/platform/linux"
	"github.com/yoockh/sebab/internal/secret"
	"github.com/yoockh/sebab/internal/server"
)

// Doctor check states.
const (
	stateOK   = "ok"
	stateInfo = "info"
	stateWarn = "warn"
	stateFail = "fail"
)

type finding struct {
	Area   string `json:"area"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// doctorDeps are the system probes doctor uses (swapped in tests).
type doctorDeps struct {
	sessionName func(ctx context.Context, name string) (linuxplat.NameStatus, error)
	pingAllowed func() (bool, string, error)
	osRelease   map[string]string
	lookPath    func(string) (string, error)
}

func realDoctorDeps() doctorDeps {
	return doctorDeps{
		sessionName: linuxplat.SessionName,
		pingAllowed: func() (bool, string, error) {
			gids, _ := os.Getgroups()
			return linuxplat.PingAllowed("/proc", append(gids, os.Getgid()))
		},
		osRelease: linuxplat.OSRelease("/etc/os-release"),
		lookPath:  exec.LookPath,
	}
}

var testDoctorDeps *doctorDeps // set by tests

func runDoctor(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("doctor", env)
	jsonOut := fs.Bool("json", false, "print JSON")
	online := fs.Bool("online", false, "also connect to the canary targets (contacts the configured third parties)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	deps := realDoctorDeps()
	if testDoctorDeps != nil {
		deps = *testDoctorDeps
	}
	d := &doctor{rt: rt, deps: deps, family: linuxplat.Family(deps.osRelease)}
	d.run(ctx, *online)
	if *jsonOut {
		printJSON(env, rt.redactor, d.findings)
	} else {
		d.print()
	}
	for _, f := range d.findings {
		if f.State == stateFail {
			return ExitDegraded
		}
	}
	return ExitOK
}

type doctor struct {
	rt       *runtime
	deps     doctorDeps
	family   string
	findings []finding
	cfg      *config.Config
}

func (d *doctor) add(area, state, detail, fix string) {
	d.findings = append(d.findings, finding{Area: area, State: state, Detail: d.rt.redactor.Scrub(detail), Fix: fix})
}

func (d *doctor) run(ctx context.Context, online bool) {
	osName := d.deps.osRelease["PRETTY_NAME"]
	if osName == "" {
		osName = "unknown distribution"
	}
	d.add("system", stateInfo, fmt.Sprintf("sebab %s on %s", versionString(), osName), "")
	d.checkConfig()
	d.checkKeyring(ctx)
	d.checkSecrets()
	d.checkNotifications(ctx)
	d.checkNetwork()
	d.checkVPNs()
	d.checkVPNTools()
	d.checkICMP()
	d.checkProxies()
	d.checkEngine()
	d.checkFrontends()
	if online {
		d.checkCanary(ctx)
	}
}

func (d *doctor) checkConfig() {
	path := d.rt.paths.ConfigFile
	cfg, err := config.Load(path)
	switch {
	case err == nil:
		d.cfg = cfg
		d.add("config", stateOK, fmt.Sprintf("%s: %d monitors", path, len(cfg.Monitors)), "")
	case strings.Contains(err.Error(), config.ErrNotFound.Error()):
		d.cfg = &config.Config{}
		d.add("config", stateWarn, path+" does not exist yet",
			"sebab monitor add --name NAME --target https://example.com/health")
	default:
		d.cfg = &config.Config{}
		d.add("config", stateFail, err.Error(), "fix the listed problems in "+path)
	}
}

func (d *doctor) checkKeyring(ctx context.Context) {
	fix := "install a Secret Service provider: " + linuxplat.Install(d.family, "gnome-keyring") +
		" (or enable KeePassXC's Secret Service integration), then log in again so it starts"
	if err := d.rt.store.KeyringAvailable(); err == nil {
		owner := ""
		if st, err := d.deps.sessionName(ctx, "org.freedesktop.secrets"); err == nil && st.OwnerComm != "" {
			owner = " (" + st.OwnerComm + ")"
		}
		d.add("keyring", stateOK, "Secret Service keyring available"+owner, "")
	} else {
		d.add("keyring", stateWarn, "no Secret Service keyring: secrets fall back to env:VAR references or "+
			d.rt.paths.SecretsFile()+" (0600)", fix)
	}
	if st, err := os.Stat(d.rt.paths.SecretsFile()); err == nil {
		if st.Mode().Perm()&0o077 != 0 {
			d.add("secrets file", stateFail, fmt.Sprintf("%s has mode %o", d.rt.paths.SecretsFile(), st.Mode().Perm()),
				"chmod 600 "+d.rt.paths.SecretsFile())
		} else {
			d.add("secrets file", stateWarn, d.rt.paths.SecretsFile()+" is in use (plain text, mode 0600)",
				"move secrets to a keyring when one is available: sebab secret set NAME")
		}
	}
}

func (d *doctor) checkSecrets() {
	for _, ch := range d.cfg.Notify {
		if ch.Secret == "" {
			continue
		}
		if v, _, err := d.rt.store.Resolve(ch.Secret); err != nil {
			d.add("notify", stateFail, fmt.Sprintf("%s channel: secret %q not found", ch.Type, ch.Secret),
				"sebab secret set "+ch.Secret)
		} else {
			d.rt.redactor.Add(v)
			d.add("notify", stateOK, fmt.Sprintf("%s channel configured (test it with 'sebab notify-test')", ch.Type), "")
		}
	}
	for _, m := range d.cfg.Monitors {
		if m.Auth == nil {
			continue
		}
		v, backend, err := d.rt.store.Resolve(m.Auth.Secret)
		if err != nil {
			fix := "sebab secret set " + m.Auth.Secret
			if secret.IsEnvRef(m.Auth.Secret) {
				fix = "export " + strings.TrimPrefix(m.Auth.Secret, "env:") + " in the environment that starts sebab"
			}
			d.add("secret", stateFail, fmt.Sprintf("monitor '%s': secret %q not found", m.ID, m.Auth.Secret), fix)
			continue
		}
		d.rt.redactor.Add(v)
		d.add("secret", stateOK, fmt.Sprintf("monitor '%s': secret %q found in %s", m.ID, m.Auth.Secret, backend), "")
	}
}

func (d *doctor) checkNotifications(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, linuxplat.DefaultTimeout)
	defer cancel()
	st, err := d.deps.sessionName(ctx, "org.freedesktop.Notifications")
	switch {
	case err != nil:
		d.add("notifications", stateWarn, "no D-Bus session bus: "+err.Error(),
			"run sebab inside your graphical session (DBUS_SESSION_BUS_ADDRESS must be set)")
	case st.Owned:
		who := st.OwnerComm
		if who == "" {
			who = "a notification daemon"
		}
		d.add("notifications", stateOK, "notifications are handled by "+who, "")
	case st.Activatable:
		d.add("notifications", stateOK, "a notification daemon is D-Bus activatable", "")
	default:
		d.add("notifications", stateWarn, "no notification daemon on the session bus",
			"use a shell with notifications (Noctalia, mako, dunst, swaync): "+linuxplat.Install(d.family, "mako"))
	}
}

func (d *doctor) checkNetwork() {
	links, err := d.rt.network.Links()
	if err != nil {
		d.add("network", stateWarn, "cannot list interfaces: "+err.Error(), "")
		return
	}
	if !platform.HasActiveLink(links) {
		d.add("network", stateFail, "no active network interface", "connect to Wi-Fi or Ethernet")
		return
	}
	if ok, err := d.rt.network.HasDefaultRoute(); err == nil && !ok {
		d.add("network", stateWarn, "no default route", "reconnect, or check your router")
		return
	}
	d.add("network", stateOK, "active link and default route", "")
}

func (d *doctor) checkVPNs() {
	links, _ := d.rt.network.Links()
	seen := map[string]bool{}
	for _, m := range d.cfg.Monitors {
		if m.RequiresVPN == nil || seen[m.RequiresVPN.Interface] {
			continue
		}
		iface := m.RequiresVPN.Interface
		seen[iface] = true
		l, ok := platform.FindLink(links, iface)
		switch {
		case !ok:
			d.add("vpn", stateInfo, iface+" does not exist right now (its monitors are paused)", "connect the VPN; sebab never starts it")
		case !l.Up || !l.Running:
			d.add("vpn", stateInfo, iface+" is down (its monitors are paused)", "connect the VPN; sebab never starts it")
		default:
			d.add("vpn", stateOK, iface+" is up", "")
		}
	}
}

// checkVPNTools lists VPN tooling found on PATH. Sebab never drives these; it
// only watches the interfaces they create.
func (d *doctor) checkVPNTools() {
	var found []string
	for _, tool := range []string{"wg-quick", "nmcli", "openvpn", "tailscale", "openconnect"} {
		if _, err := d.deps.lookPath(tool); err == nil {
			found = append(found, tool)
		}
	}
	needsVPN := false
	for _, m := range d.cfg.Monitors {
		if m.RequiresVPN != nil {
			needsVPN = true
		}
	}
	switch {
	case len(found) > 0:
		d.add("vpn tools", stateInfo, "found "+strings.Join(found, ", ")+" (sebab only watches the interface, never starts a VPN)", "")
	case needsVPN:
		d.add("vpn tools", stateInfo, "no VPN client found on PATH although monitors require a VPN interface",
			"install your VPN client, for example "+linuxplat.Install(d.family, "wireguard-tools"))
	}
}

func (d *doctor) checkICMP() {
	ok, rng, err := d.deps.pingAllowed()
	switch {
	case err != nil:
		d.add("icmp", stateInfo, "cannot read net.ipv4.ping_group_range: "+err.Error(), "")
	case ok:
		d.add("icmp", stateOK, "unprivileged ICMP allowed (ping_group_range "+rng+")", "")
	default:
		d.add("icmp", stateInfo, "unprivileged ICMP not allowed (ping_group_range "+rng+"); sebab does not need it",
			"optional: sudo sysctl -w net.ipv4.ping_group_range=\"0 2147483647\"")
	}
}

func (d *doctor) checkProxies() {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		if v := d.rt.env.Getenv(k); v != "" {
			d.add("proxy", stateInfo, k+"="+secret.RedactURL(v)+" (HTTP checks use it; DNS and TCP steps then refer to the proxy)", "")
		}
	}
}

func (d *doctor) checkEngine() {
	sock := d.rt.paths.SocketPath()
	if sock == "" {
		d.add("engine", stateWarn, "XDG_RUNTIME_DIR is unset: no control socket, frontends cannot share an engine",
			"run sebab from your login session")
		return
	}
	resp, err := server.Call(context.Background(), sock, server.Request{Cmd: "status"}, time.Second)
	if err != nil {
		d.add("engine", stateInfo, "no engine running (frontends start one with 'sebab watch')", "")
		return
	}
	d.add("engine", stateOK, fmt.Sprintf("%s engine %s running (pid %d)", resp.Source, resp.Engine, resp.PID), "")
}

func (d *doctor) checkFrontends() {
	if p, err := d.deps.lookPath("noctalia"); err == nil {
		d.add("frontend", stateInfo, "Noctalia found at "+p+": enable the plugin with 'noctalia msg plugins enable yoockh/sebab'", "")
	}
	if p, err := d.deps.lookPath("waybar"); err == nil {
		d.add("frontend", stateInfo, "Waybar found at "+p+": see frontends/waybar for the custom module", "")
	}
}

func (d *doctor) checkCanary(ctx context.Context) {
	ec := d.cfg.EngineSettings()
	if len(ec.Canary) == 0 {
		d.add("canary", stateInfo, "canary disabled: side is reported as unknown for DNS and TCP failures", "")
		return
	}
	v := d.rt.canary(ec).Check(ctx, true)
	switch {
	case v.Checked && !v.OK:
		d.add("canary", stateFail, "no canary target answers: "+strings.Join(ec.Canary, ", "), "check your connection or set [defaults] canary")
	case v.DNSChecked && !v.DNSOK:
		d.add("canary", stateWarn, "canary DNS names do not resolve: "+strings.Join(ec.DNSCanary, ", "), "check your resolver")
	default:
		d.add("canary", stateOK, "canary reachable: "+strings.Join(ec.Canary, ", "), "")
	}
}

func (d *doctor) print() {
	w := d.rt.env.Stdout
	for _, f := range d.findings {
		fmt.Fprintf(w, "[%-4s] %-14s %s\n", f.State, f.Area, f.Detail)
		if f.Fix != "" && f.State != stateOK {
			fmt.Fprintf(w, "       %-14s fix: %s\n", "", f.Fix)
		}
	}
}
