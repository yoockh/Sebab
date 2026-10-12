package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The example in docs/config.md must parse and validate.
func TestDocsConfigExampleParses(t *testing.T) {
	data, err := os.ReadFile("../../docs/config.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)```toml\n(.*?)```").FindSubmatch(data)
	if m == nil {
		t.Fatal("no toml block in docs/config.md")
	}
	c, err := Parse(m[1])
	if err != nil {
		t.Fatalf("docs example does not parse: %v", err)
	}
	if len(c.Monitors) != 1 || len(c.Notify) != 3 || len(c.Monitors[0].Assert) != 3 ||
		!strings.HasPrefix(c.Monitors[0].URL, "https://") {
		t.Fatalf("unexpected parse: %+v", c)
	}
}
