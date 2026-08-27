package main

import "testing"

func TestParseFlags_RequiresProject(t *testing.T) {
	if _, err := parseFlags([]string{}); err == nil {
		t.Fatal("parseFlags with no -project, want error")
	}
}

func TestParseFlags_Defaults(t *testing.T) {
	cfg, err := parseFlags([]string{"-project", "/tmp/my-godot-project"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.projectRoot != "/tmp/my-godot-project" {
		t.Errorf("projectRoot = %q, want %q", cfg.projectRoot, "/tmp/my-godot-project")
	}
	if cfg.godotBin != "godot" {
		t.Errorf("godotBin = %q, want default %q", cfg.godotBin, "godot")
	}
	if cfg.operationsScript != "" {
		t.Errorf("operationsScript = %q, want empty (resolved at runtime)", cfg.operationsScript)
	}
}

// There is deliberately no runtime-tier flag yet at all: internal/runtime
// is a client with nothing wired into the tool allowlist to configure. This
// test documents that, so a reviewer notices if a host/address-shaped flag
// gets added ahead of (or without) the operation that needs it.
func TestParseFlags_NoRuntimeTierFlagsYet(t *testing.T) {
	for _, unknown := range []string{"-runtime-host", "-runtime-port", "-enable-runtime-tier"} {
		_, err := parseFlags([]string{"-project", "/tmp/x", unknown, "1"})
		if err == nil {
			t.Errorf("parseFlags accepted unknown flag %q, want error (no runtime-tier flags should exist yet)", unknown)
		}
	}
}
