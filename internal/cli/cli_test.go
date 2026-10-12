package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/lab"
)

func TestCheckExitCodesPerLayer(t *testing.T) {
	w := newWorld(t)
	ca := w.vars["LAB_CA"]
	cases := []struct {
		args []string
		exit int
		code string
	}{
		{[]string{w.url("/ok")}, ExitOK, ""},
		{[]string{w.lab.HTTPURL(lab.NameNXDomain, "/")}, ExitDNS, "DNS_NXDOMAIN"},
		{[]string{fmt.Sprintf("db.lab.test:%d", w.lab.RefusedPort)}, ExitTCP, "TCP_REFUSED"},
		{[]string{"--ca-file", ca, w.tlsURL(lab.TLSExpired, "/ok")}, ExitTLS, "TLS_EXPIRED"},
		{[]string{w.url("/status/502")}, ExitHTTP, "HTTP_BAD_GATEWAY"},
		{[]string{w.url("/status/429")}, ExitDegraded, "HTTP_RATE_LIMITED"},
		{[]string{"--vpn", "wg9", w.url("/ok")}, ExitVPN, "VPN_DOWN"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			code, out, errOut := w.run("", append([]string{"check", "--json"}, tc.args...)...)
			if code != tc.exit {
				t.Fatalf("exit %d want %d (stderr %q)", code, tc.exit, errOut)
			}
			c := decodeCheck(t, out)
			if tc.code == "" && c.Cause != nil || tc.code != "" && (c.Cause == nil || c.Cause.Code != tc.code) {
				t.Fatalf("cause %+v want %s", c.Cause, tc.code)
			}
		})
	}
}

func TestCheckHumanOutput(t *testing.T) {
	w := newWorld(t)
	code, out, _ := w.run("", "check", "--ca-file", w.vars["LAB_CA"], w.tlsURL(lab.TLSExpired, "/ok"))
	if code != ExitTLS {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"[ ok ] dns", "[ ok ] tcp", "[FAIL] tls", "TLS certificate expired 2 days ago",
		"DOWN  TLS_EXPIRED  (side: theirs)", "Hint: Renew the certificate on the server. DNS and TCP are fine."} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = w.run("", "check", w.url("/ok"), "--json")
	if code != ExitOK || !strings.HasPrefix(out, `{"v":1,"type":"check"`) {
		t.Errorf("flags after the target should work: %d %q", code, out)
	}
}

func TestCheckWorksWithoutConfig(t *testing.T) {
	w := newWorld(t)
	if _, err := os.Stat(w.configPath()); err == nil {
		t.Fatal("precondition: no config")
	}
	if code, _, e := w.run("", "check", w.url("/ok")); code != ExitOK {
		t.Fatalf("exit %d: %s", code, e)
	}
	if _, err := os.Stat(w.configPath()); err == nil {
		t.Error("check must not create a config")
	}
}

func TestCheckUsageErrors(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{{"check"}, {"check", "ftp://x.example"}, {"check", "db.example:99999"},
		{"check", "--auth", "bearer", w.url("/auth/bearer")}} {
		if code, _, _ := w.run("", args...); code != ExitUsage {
			t.Errorf("%v: exit %d want %d", args, code, ExitUsage)
		}
	}
}

func TestCheckAuthSecretSources(t *testing.T) {
	w := newWorld(t)
	code, out, _ := w.run(lab.Token+"\n", "check", "--json", "--auth", "bearer", "--secret-stdin", w.url("/auth/bearer"))
	if code != ExitOK {
		t.Fatalf("bearer via stdin: exit %d %s", code, out)
	}
	code, _, _ = w.run("", "check", "--auth", "header", "--header-name", "X-API-Key", "--secret", "env:API_TOKEN", w.url("/auth/header"))
	if code != ExitOK {
		t.Fatalf("header via env ref: exit %d", code)
	}
	code, out, _ = w.run("wrong-token\n", "check", "--json", "--auth", "basic", "--username", lab.BasicUser, "--secret-stdin", w.url("/auth/basic"))
	c := decodeCheck(t, out)
	if code != ExitHTTP || c.Cause.Code != "HTTP_AUTH" || !strings.Contains(c.Cause.Hint, "token may be expired or invalid") ||
		c.Side != "yours" {
		t.Fatalf("wrong basic password: exit %d %+v", code, c)
	}
	w.assertNoLeak()
}

func TestMonitorCRUD(t *testing.T) {
	w := newWorld(t)
	code, out, _ := w.run("", "monitor", "add", "--json", "--name", "Web (staging)", "--target", "nope:", "--interval", "10s")
	var r result
	_ = json.Unmarshal([]byte(out), &r)
	if code != ExitUsage || r.OK || len(r.Errors) == 0 {
		t.Fatalf("invalid add: %d %s", code, out)
	}
	code, out, _ = w.run("", "monitor", "add", "--json", "--name", "Web (staging)", "--target", w.url("/ok"), "--interval", "10s")
	_ = json.Unmarshal([]byte(out), &r)
	if code != ExitUsage || !strings.Contains(strings.Join(r.Errors, ";"), "interval below 30s") {
		t.Fatalf("interval below minimum: %d %s", code, out)
	}
	code, out, _ = w.run("", "monitor", "add", "--name", "Web (staging)", "--target", w.url("/ok"), "--expect", "200,204")
	if code != ExitOK || !strings.Contains(out, "Test check:") || !strings.Contains(out, "Added monitor 'web-staging'") {
		t.Fatalf("add: %d %s", code, out)
	}
	if code, _, _ := w.run("", "monitor", "add", "--name", "DB", "--target", fmt.Sprintf("db.lab.test:%d", w.lab.HTTPPort), "--no-test"); code != 0 {
		t.Fatal("add tcp")
	}
	code, out, _ = w.run("", "monitor", "list", "--json")
	var list []view
	if err := json.Unmarshal([]byte(out), &list); err != nil || code != 0 || len(list) != 2 || list[1].Kind != "tcp" ||
		list[0].ExpectStatus[1] != 204 {
		t.Fatalf("list: %d %s", code, out)
	}
	if code, _, _ := w.run("", "monitor", "edit", "web-staging", "--interval", "2m", "--no-test"); code != 0 {
		t.Fatal("edit")
	}
	if code, _, _ := w.run("", "monitor", "pause", "db"); code != 0 {
		t.Fatal("pause")
	}
	cfg, err := config.Load(w.configPath())
	if err != nil || cfg.Monitors[0].Interval.D().Minutes() != 2 || !cfg.Monitors[1].Paused {
		t.Fatalf("config after edit/pause: %v %+v", err, cfg)
	}
	if code, _, _ := w.run("", "monitor", "resume", "db"); code != 0 {
		t.Fatal("resume")
	}
	if code, _, _ := w.run("", "monitor", "rm", "db"); code != 0 {
		t.Fatal("rm")
	}
	if code, _, _ := w.run("", "monitor", "rm", "db"); code != ExitUsage {
		t.Fatal("rm missing should be a usage error")
	}
	code, out, _ = w.run("", "monitor", "show", "web-staging")
	var v view
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || v.IntervalS != 120 {
		t.Fatalf("show: %d %s", code, out)
	}
	if _, err := os.Stat(w.configPath() + ".bak"); err != nil {
		t.Error("config backup missing")
	}
}

func TestPanelFlowWithHandoffFile(t *testing.T) {
	w := newWorld(t)
	// 1. The panel asks for a handoff file and writes the token into it.
	code, out, _ := w.run("", "secret", "handoff")
	path := strings.TrimSpace(out)
	if code != 0 || !strings.HasPrefix(path, w.vars["XDG_RUNTIME_DIR"]) {
		t.Fatalf("handoff: %d %q", code, out)
	}
	if err := os.WriteFile(path, []byte(lab.Token), 0o644); err != nil {
		t.Fatal(err)
	}
	// 2. Live test before save keeps the file.
	code, out, _ = w.run("", "check", "--json", "--auth", "bearer", "--secret-file", path, "--keep-secret-file", w.url("/auth/bearer"))
	if code != ExitOK {
		t.Fatalf("live test: %d %s", code, out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("--keep-secret-file must keep the handoff file")
	}
	// 3. Save consumes it into the keyring; config holds only the reference.
	code, out, _ = w.run("", "monitor", "add", "--json", "--name", "Admin API", "--target", w.url("/auth/bearer"),
		"--auth", "bearer", "--secret-file", path)
	var r result
	_ = json.Unmarshal([]byte(out), &r)
	if code != 0 || !r.OK || r.Backend != "keyring" {
		t.Fatalf("add with secret: %d %s", code, out)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("handoff file must be deleted after saving")
	}
	data, _ := os.ReadFile(w.configPath())
	if !strings.Contains(string(data), "secret = 'admin-api-token'") {
		t.Fatalf("config should reference the secret by name:\n%s", data)
	}
	if v, _ := w.keyring.Get("sebab", "admin-api-token"); v != lab.Token {
		t.Fatal("secret not in keyring")
	}
	// 4. The engine-side check resolves it.
	if code, out, _ := w.run("", "check", "--monitor", "admin-api", "--json"); code != ExitOK {
		t.Fatalf("configured check: %d %s", code, out)
	}
	code, md, _ := w.run("", "report", "admin-api", "--fresh")
	if code != 0 || !strings.Contains(md, "| Status | UP |") {
		t.Fatalf("report: %d %s", code, md)
	}
	w.assertNoLeak(md)
}

func TestHandoffOutsideAllowedDirRefused(t *testing.T) {
	w := newWorld(t)
	bad := filepath.Join(w.vars["HOME"], "handoff-123")
	_ = os.WriteFile(bad, []byte(lab.Token), 0o600)
	if code, _, _ := w.run("", "check", "--auth", "bearer", "--secret-file", bad, w.url("/auth/bearer")); code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
}

func TestSecretCommandsAndFileFallback(t *testing.T) {
	w := newWorld(t)
	code, out, _ := w.run(lab.Token+"\n", "secret", "set", "api")
	if code != 0 || !strings.Contains(out, "in the keyring") {
		t.Fatalf("set: %d %s", code, out)
	}
	if code, _, _ := w.run("", "secret", "set", "empty"); code != ExitUsage {
		t.Fatal("empty stdin must be refused")
	}
	w.keyring.mu.Lock()
	w.keyring.down = true
	w.keyring.mu.Unlock()
	code, out, errOut := w.run(lab.Token+"\n", "secret", "set", "fallback")
	if code != 0 || !strings.Contains(out, "in the file") || !strings.Contains(errOut, "no Secret Service keyring") {
		t.Fatalf("fallback: %d %s %s", code, out, errOut)
	}
	st, err := os.Stat(filepath.Join(w.vars["XDG_CONFIG_HOME"], "sebab", "secrets.toml"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file %v %v", st, err)
	}
	if code, _, _ := w.run("", "monitor", "add", "--name", "F", "--target", w.url("/auth/bearer"), "--auth", "bearer",
		"--secret", "fallback", "--no-test"); code != 0 {
		t.Fatal("add with stored ref")
	}
	code, out, _ = w.run("", "secret", "list", "--json")
	if code != 0 || !strings.Contains(out, `"name":"fallback","backend":"file","status":"ok"`) {
		t.Fatalf("list: %d %s", code, out)
	}
	if code, _, _ := w.run("", "secret", "rm", "fallback"); code != 0 {
		t.Fatal("rm")
	}
	code, out, _ = w.run("", "check", "--monitor", "f", "--json")
	if code != ExitHTTP || !strings.Contains(out, "HTTP_AUTH") {
		t.Fatalf("missing secret should surface as HTTP_AUTH: %d %s", code, out)
	}
	w.assertNoLeak()
}

func TestHelpAndVersion(t *testing.T) {
	w := newWorld(t)
	code, out, _ := w.run("", "--help")
	if code != 0 || !strings.Contains(out, "check") || !strings.Contains(out, "monitor") {
		t.Fatalf("help: %s", out)
	}
	if code, out, _ := w.run("", "version"); code != 0 || !strings.HasPrefix(out, "sebab ") {
		t.Fatalf("version: %s", out)
	}
	if code, _, _ := w.run("", "nope"); code != ExitUsage {
		t.Fatal("unknown command")
	}
}
