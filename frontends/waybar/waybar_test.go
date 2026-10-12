// Package waybar checks the example Waybar configuration shipped for Sebab.
package waybar

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestExampleConfigIsValid(t *testing.T) {
	data, err := os.ReadFile("config.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	// Strip // comments (none of the values contain "//").
	plain := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAll(data, nil)
	var cfg map[string]map[string]any
	if err := json.Unmarshal(plain, &cfg); err != nil {
		t.Fatalf("config.jsonc is not valid JSON(C): %v", err)
	}
	m := cfg["custom/sebab"]
	if m["exec"] != "sebab waybar" || m["return-type"] != "json" || m["exec-on-event"] != false {
		t.Fatalf("module %+v", m)
	}
	known := map[string]bool{"exec": true, "exec-if": true, "hide-empty-text": true, "exec-on-event": true,
		"return-type": true, "interval": true, "restart-interval": true, "signal": true, "format": true,
		"format-icons": true, "rotate": true, "max-length": true, "min-length": true, "align": true, "justify": true,
		"on-click": true, "on-click-middle": true, "on-click-right": true, "on-update": true, "on-scroll-up": true,
		"on-scroll-down": true, "smooth-scrolling-threshold": true, "tooltip": true, "tooltip-format": true,
		"escape": true, "menu": true}
	for k := range m {
		if !known[k] {
			t.Errorf("unknown custom module option %q (waybar-custom(5), Waybar 0.15)", k)
		}
	}
	icons := m["format-icons"].(map[string]any)
	for _, alt := range []string{"up", "degraded", "down", "offline", "stalled", "starting", "empty"} {
		if _, ok := icons[alt]; !ok {
			t.Errorf("format-icons lacks %q", alt)
		}
	}
}
