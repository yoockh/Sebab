package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const prdExample = `
[defaults]
interval = "60s"
fail_threshold = 2
recover_threshold = 2
canary = ["tcp://192.0.2.1:443", "tcp://192.0.2.2:443"]

[[monitor]]
id = "web-staging"
name = "Web (staging)"
url = "https://staging.example.com/health"
expect_status = [200]

[[monitor]]
id = "admin-api"
name = "Admin API"
url = "https://admin.internal.example/api/ping"
auth = { type = "bearer", secret = "admin-api-token" }
requires_vpn = { interface = "wg0" }

[[monitor]]
id = "staging-db"
name = "Staging DB"
target = "tcp://db.staging.example:5432"
`

func TestParsePRDExample(t *testing.T) {
	c, err := Parse([]byte(prdExample))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Monitors) != 3 {
		t.Fatalf("monitors: %d", len(c.Monitors))
	}
	if c.Monitors[1].Auth.Secret != "admin-api-token" || c.Monitors[1].RequiresVPN.Interface != "wg0" {
		t.Errorf("auth/vpn not parsed: %+v", c.Monitors[1])
	}
	if c.Monitors[2].Kind() != "tcp" || c.Monitors[0].Kind() != "http" {
		t.Errorf("kinds wrong")
	}
	s := c.Resolve(c.Monitors[0])
	if s.Interval != 60*time.Second || s.FailThreshold != 2 || s.TLSWarnDays != 14 || !s.FollowRedirects {
		t.Errorf("resolve: %+v", s)
	}
	if e := c.EngineSettings(); len(e.Canary) != 2 || e.Canary[0] != "tcp://192.0.2.1:443" {
		t.Errorf("canary %v", e.Canary)
	}
}

func TestCanaryDefaultsAndDisable(t *testing.T) {
	c, err := Parse([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.EngineSettings().Canary; len(got) != len(DefaultCanary) {
		t.Errorf("default canary %v", got)
	}
	c, err = Parse([]byte("[defaults]\ncanary = []\ndns_canary = []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.EngineSettings(); len(got.Canary) != 0 || len(got.DNSCanary) != 0 {
		t.Errorf("canary should be disabled: %+v", got)
	}
}

func TestValidationMessages(t *testing.T) {
	cases := map[string]struct {
		toml string
		want string
	}{
		"interval":     {`[[monitor]]` + "\nid=\"x\"\nname=\"X\"\nurl=\"https://a.example\"\ninterval=\"10s\"", "monitor 'x': interval below 30s"},
		"bad url":      {"[[monitor]]\nid=\"x\"\nname=\"X\"\nurl=\"ftp://a.example\"", "scheme must be http or https"},
		"missing port": {"[[monitor]]\nid=\"x\"\nname=\"X\"\ntarget=\"db.example\"", "missing port"},
		"cidr":         {"[[monitor]]\nid=\"x\"\nname=\"X\"\ntarget=\"192.0.2.0/24:80\"", "ranges and wildcards"},
		"port range":   {"[[monitor]]\nid=\"x\"\nname=\"X\"\ntarget=\"db.example:1-100\"", "ranges and wildcards"},
		"both":         {"[[monitor]]\nid=\"x\"\nname=\"X\"\ntarget=\"db.example:1\"\nurl=\"https://a.example\"", "not both"},
		"dup":          {"[[monitor]]\nid=\"x\"\nname=\"X\"\ntarget=\"a.example:1\"\n[[monitor]]\nid=\"x\"\nname=\"Y\"\ntarget=\"b.example:1\"", "duplicate id"},
		"auth type":    {"[[monitor]]\nid=\"x\"\nname=\"X\"\nurl=\"https://a.example\"\nauth={type=\"magic\",secret=\"s\"}", "must be bearer, header or basic"},
		"secret value": {"[[monitor]]\nid=\"x\"\nname=\"X\"\nurl=\"https://a.example\"\nauth={type=\"bearer\",secret=\"Bearer abc def\"}", "auth.secret must be"},
		"header auth":  {"[[monitor]]\nid=\"x\"\nname=\"X\"\nurl=\"https://a.example\"\nheaders={Authorization=\"x\"}", "put credentials in auth"},
		"userinfo":     {"[[monitor]]\nid=\"x\"\nname=\"X\"\nurl=\"https://u:p@a.example\"", "not in the URL"},
		"unknown":      {"[[monitor]]\nid=\"x\"\nname=\"X\"\nurl=\"https://a.example\"\nbogus=1", "unknown field"},
		"bad dur":      {"[defaults]\ninterval=\"soon\"", "invalid duration"},
		"vpn":          {"[[monitor]]\nid=\"x\"\nname=\"X\"\nurl=\"https://a.example\"\nrequires_vpn={interface=\"wg 0\"}", "not a valid interface"},
		"tcp auth":     {"[[monitor]]\nid=\"x\"\nname=\"X\"\ntarget=\"a.example:22\"\nexpect_status=[200]", "only apply to url"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.toml))
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want ValidationError, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestSaveAtomicAndBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.toml")
	c := &Config{}
	if err := c.Add(Monitor{ID: "a", Name: "A", URL: "https://a.example/"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(Monitor{ID: "b", Name: "B", Target: "tcp://b.example:22"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", st.Mode(), err)
	}
	loaded, err := Load(path)
	if err != nil || len(loaded.Monitors) != 2 {
		t.Fatalf("reload: %v %+v", err, loaded)
	}
	bak, err := Load(path + ".bak")
	if err != nil || len(bak.Monitors) != 1 {
		t.Fatalf("backup: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSaveRefusesInvalid(t *testing.T) {
	c := &Config{Monitors: []Monitor{{ID: "BAD ID", Name: "x", URL: "https://a.example"}}}
	if err := c.Save(filepath.Join(t.TempDir(), "c.toml")); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestEditOperations(t *testing.T) {
	c := &Config{}
	_ = c.Add(Monitor{ID: "a", Name: "A", URL: "https://a.example/"})
	if err := c.Add(Monitor{ID: "a", Name: "A2", URL: "https://a.example/"}); err == nil {
		t.Error("duplicate add should fail")
	}
	if err := c.Add(Monitor{ID: "z", Name: "Z", URL: "https://a.example/", Interval: durPtr(time.Second)}); err == nil || len(c.Monitors) != 1 {
		t.Error("invalid add should fail and roll back")
	}
	if err := c.Replace("a", Monitor{ID: "a", Name: "A", URL: "nope"}); err == nil || c.Monitors[0].URL != "https://a.example/" {
		t.Error("invalid replace should roll back")
	}
	if got := c.UniqueID("A"); got != "a-2" {
		t.Errorf("unique id %q", got)
	}
	if err := c.Remove("a"); err != nil || len(c.Monitors) != 0 {
		t.Error("remove failed")
	}
	if err := c.Remove("a"); err == nil {
		t.Error("remove missing should fail")
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{"Web (staging)": "web-staging", "  ": "monitor", "API v2!": "api-v2"} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolvePaths(t *testing.T) {
	env := map[string]string{"HOME": "/home/u", "XDG_RUNTIME_DIR": "/run/user/1"}
	p, err := ResolvePaths(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigFile != "/home/u/.config/sebab/config.toml" || p.RuntimeDir != "/run/user/1/sebab" ||
		p.StateDir != "/home/u/.local/state/sebab" || p.SocketPath() != "/run/user/1/sebab/sebab.sock" {
		t.Errorf("paths %+v", p)
	}
	env["SEBAB_CONFIG"] = "/tmp/x.toml"
	p, _ = ResolvePaths(func(k string) string { return env[k] })
	if p.ConfigFile != "/tmp/x.toml" {
		t.Errorf("override ignored: %s", p.ConfigFile)
	}
	if _, err := ResolvePaths(func(string) string { return "" }); !errors.Is(err, ErrNoHome) {
		t.Errorf("want ErrNoHome, got %v", err)
	}
}

func TestTimeoutClampedBelowInterval(t *testing.T) {
	c := &Config{Defaults: Defaults{Timeout: durPtr(2 * time.Minute)}}
	s := c.Resolve(Monitor{ID: "a", Name: "A", URL: "https://a.example"})
	if s.Timeout >= s.Interval {
		t.Errorf("timeout %v not below interval %v", s.Timeout, s.Interval)
	}
}

func durPtr(d time.Duration) *Duration { v := Duration(d); return &v }

func TestAssertionValidation(t *testing.T) {
	ok := `[[monitor]]
id = "a"
name = "A"
url = "https://a.example/"
assert = [
  { type = "body", value = "ok" },
  { type = "json", path = "data.items[0].state", value = "ready" },
  { type = "json", path = "[0].id" },
  { type = "header", name = "Content-Type", value = "json" },
  { type = "body", value = "Maintenance", negate = true },
]
`
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{ type = "body" }`, `{ type = "json", path = "a..b" }`, `{ type = "header", name = "bad name" }`,
		`{ type = "xpath", path = "/x" }`} {
		src := "[[monitor]]\nid=\"a\"\nname=\"A\"\nurl=\"https://a.example/\"\nassert=[" + bad + "]\n"
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
	if _, err := Parse([]byte("[[monitor]]\nid=\"a\"\nname=\"A\"\ntarget=\"a.example:1\"\nassert=[{type=\"body\",value=\"x\"}]\n")); err == nil {
		t.Error("tcp monitors cannot have assertions")
	}
}

func TestQuietHours(t *testing.T) {
	q, err := ParseQuietHours("22:00-07:00")
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 11, h, m, 0, 0, time.UTC) }
	for _, tc := range []struct {
		t    time.Time
		want bool
	}{{at(23, 0), true}, {at(2, 30), true}, {at(6, 59), true}, {at(7, 0), false}, {at(12, 0), false}, {at(22, 0), true}} {
		if got := q.Contains(tc.t); got != tc.want {
			t.Errorf("%v: %v", tc.t, got)
		}
	}
	day, _ := ParseQuietHours("12:00-13:30")
	if !day.Contains(at(13, 29)) || day.Contains(at(13, 30)) {
		t.Error("non-wrapping window")
	}
	for _, bad := range []string{"22-07", "25:00-07:00", "07:00-07:00", "nope"} {
		if _, err := ParseQuietHours(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	if _, err := Parse([]byte("[defaults]\nquiet_hours = \"late\"\n")); err == nil {
		t.Error("invalid quiet_hours must fail validation")
	}
	c, _ := Parse([]byte("[defaults]\nquiet_hours = \"22:00-07:00\"\n"))
	if c.EngineSettings().QuietHours == nil {
		t.Error("quiet hours not resolved")
	}
}

func TestNotifyChannels(t *testing.T) {
	ok := `
[[notify]]
type = "webhook"
url = "https://hooks.example.com/sebab"
[[notify]]
type = "ntfy"
url = "https://ntfy.example.com/topic"
secret = "env:NTFY_TOKEN"
[[notify]]
type = "telegram"
secret = "tg-token"
chat_id = "42"
`
	c, err := Parse([]byte(ok))
	if err != nil || len(c.Notify) != 3 {
		t.Fatalf("%v %+v", err, c)
	}
	for _, bad := range []string{
		"[[notify]]\ntype=\"sms\"\n",
		"[[notify]]\ntype=\"webhook\"\nurl=\"ftp://x.example\"\n",
		"[[notify]]\ntype=\"telegram\"\nchat_id=\"1\"\n",
		"[[notify]]\ntype=\"ntfy\"\nurl=\"https://n.example/t\"\nsecret=\"has spaces\"\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("should fail: %s", bad)
		}
	}
}

func TestValidationErrorsDoNotEchoCredentials(t *testing.T) {
	for _, raw := range []string{"ftp://h.example/?api_key=leak-123", "https://user:leak-123@h.example/", "https://h.example:99999/?t=leak-123"} {
		err := ValidateURL(raw)
		if err == nil {
			t.Fatalf("%s should be invalid", raw)
		}
		if strings.Contains(err.Error(), "leak-123") {
			t.Errorf("error echoes a credential: %v", err)
		}
	}
}
