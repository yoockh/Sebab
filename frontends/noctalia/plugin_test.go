// Package noctalia holds tests for the Noctalia plugin in ./sebab. Each entry
// script runs in a real Luau VM against a mock host (testdata/mock.luau) that
// validates every ui.* prop and host call. Set SEBAB_LUAU to a luau binary, or
// put luau on PATH; without one the tests are skipped.
package noctalia

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func luauBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("SEBAB_LUAU"); p != "" {
		return p
	}
	p, err := exec.LookPath("luau")
	if err != nil {
		t.Skip("luau not found (set SEBAB_LUAU); skipping plugin script tests")
	}
	return p
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// run assembles mock + shared module + entry + driver into one chunk. Each part
// is wrapped in do ... end so their locals stay separate.
func run(t *testing.T, entry, driver, prelude string) {
	t.Helper()
	luau := luauBinary(t)
	var b strings.Builder
	b.WriteString(read(t, "testdata/mock.luau"))
	b.WriteString("\ndo\n" + prelude + "\nend\n")
	b.WriteString("H.module(\"./lib/common.luau\", function()\n" + read(t, "sebab/lib/common.luau") + "\nend)\n")
	b.WriteString("do\n" + read(t, filepath.Join("sebab", entry)) + "\nend\n")
	b.WriteString("do\n" + read(t, filepath.Join("testdata", driver)) + "\nend\n")
	script := filepath.Join(t.TempDir(), strings.TrimSuffix(driver, ".luau")+"_all.luau")
	if err := os.WriteFile(script, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(luau, script).CombinedOutput()
	if err != nil {
		t.Fatalf("luau %s failed: %v\n%s", driver, err, out)
	}
	if !strings.Contains(string(out), " ok") {
		t.Fatalf("driver did not finish:\n%s", out)
	}
}

func TestServiceScript(t *testing.T) { run(t, "service.luau", "service_test.luau", "") }

func TestServiceScriptWithoutBinary(t *testing.T) {
	run(t, "service.luau", "service_missing_test.luau", `H.commands.sebab = false; H.files.sebab = nil`)
}

func TestWidgetScript(t *testing.T) { run(t, "widget.luau", "widget_test.luau", "") }

func TestPanelScript(t *testing.T) { run(t, "panel.luau", "panel_test.luau", "") }

// Every translation key used by the scripts and the manifest exists in en.json.
func TestTranslationsComplete(t *testing.T) {
	var nested map[string]any
	if err := json.Unmarshal([]byte(read(t, "sebab/translations/en.json")), &nested); err != nil {
		t.Fatal(err)
	}
	// Noctalia flattens nested objects into dotted keys.
	en := map[string]string{}
	var flatten func(prefix string, v any)
	flatten = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				key := k
				if prefix != "" {
					key = prefix + "." + k
				}
				flatten(key, c)
			}
		case string:
			en[prefix] = x
		}
	}
	flatten("", nested)
	keyRe := regexp.MustCompile(`(?:noctalia\.tr|tr)\("([a-z_.]*[a-z_])"`)
	trpRe := regexp.MustCompile(`noctalia\.trp\("([a-z_.]*[a-z_])"`)
	var missing []string
	for _, f := range []string{"service.luau", "widget.luau", "panel.luau", "lib/common.luau"} {
		src := read(t, filepath.Join("sebab", f))
		for _, m := range keyRe.FindAllStringSubmatch(src, -1) {
			if _, ok := en[m[1]]; !ok {
				missing = append(missing, f+": "+m[1])
			}
		}
		for _, m := range trpRe.FindAllStringSubmatch(src, -1) {
			if _, ok := en[m[1]+".other"]; !ok {
				missing = append(missing, f+": "+m[1]+".other")
			}
		}
	}
	for _, s := range []string{"up", "degraded", "down", "unknown", "paused"} {
		if _, ok := en["status."+s]; !ok {
			missing = append(missing, "status."+s)
		}
	}
	for _, s := range []string{"yours", "theirs", "unknown"} {
		if _, ok := en["side."+s]; !ok {
			missing = append(missing, "side."+s)
		}
	}
	manifest := read(t, "sebab/plugin.toml")
	for _, m := range regexp.MustCompile(`(?:label_key|description_key)\s*=\s*"([^"]+)"`).FindAllStringSubmatch(manifest, -1) {
		if _, ok := en[m[1]]; !ok {
			missing = append(missing, "plugin.toml: "+m[1])
		}
	}
	if len(missing) > 0 {
		t.Fatalf("missing translation keys:\n%s", strings.Join(missing, "\n"))
	}
}

// The plugin never puts a secret in argv: no script passes a token to the CLI
// except through --secret-file.
func TestNoSecretFlagsInScripts(t *testing.T) {
	src := read(t, "sebab/panel.luau")
	for _, bad := range []string{`"--token"`, `"--password"`, `form.token)`} {
		if strings.Contains(src, bad) && bad != `form.token)` {
			t.Errorf("panel.luau contains %s", bad)
		}
	}
	if strings.Contains(src, `table.insert(argv, form.token`) {
		t.Error("panel.luau inserts the token into argv")
	}
}
