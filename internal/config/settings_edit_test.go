package config

import (
	"bytes"
	"strings"
	"testing"
)

func TestWithVNCCompatibility(t *testing.T) {
	const base = "ssh:\n  user: desktop\n  listen: 127.0.0.1:15922\n  host_key_file: host\n  authorized_keys_file: authorized\n  allow_vnc: true\n"
	for _, tc := range []struct{ section, listener string }{
		{"", "127.0.0.1:15923"},
		{"vnc:\n  compatibility: null\n", "127.0.0.1:15923"},
		{"vnc:\n  compatibility:\n    enabled: false # retain\n    listen: 127.0.0.1:19923\n", "127.0.0.1:19923"},
	} {
		original := []byte(base + tc.section)
		on, err := WithVNCCompatibility(original, true)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := decodeSettings(bytes.NewReader(on))
		if err != nil || !cfg.VNC.Compatibility.Enabled || cfg.VNC.Compatibility.Listen != tc.listener {
			t.Fatalf("unexpected enabled setting: %+v, %v", cfg.VNC.Compatibility, err)
		}
		off, err := WithVNCCompatibility(on, false)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := parseSettings(bytes.NewReader(original))
		after, err := parseSettings(bytes.NewReader(off))
		if err != nil || before != after {
			t.Fatalf("toggling changed other settings: %v", err)
		}
		if strings.Contains(tc.section, "# retain") && !strings.Contains(string(off), "# retain") {
			t.Fatal("toggling dropped the YAML comment")
		}
	}
	conflict := []byte(base + "vnc:\n  compatibility:\n    listen: 127.0.0.1:5900\n")
	if _, err := WithVNCCompatibility(conflict, true); err == nil {
		t.Fatal("enabled a port that is already assigned to the VNC backend")
	}
}
