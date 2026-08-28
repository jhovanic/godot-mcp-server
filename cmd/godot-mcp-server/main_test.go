package main

import (
	"testing"
	"time"
)

func TestSessionID_Format(t *testing.T) {
	now := time.Date(2026, 8, 27, 3, 4, 5, 0, time.UTC)
	got := sessionID(now, 12345)
	want := "20260827T030405Z-12345"
	if got != want {
		t.Errorf("sessionID(%v, 12345) = %q, want %q", now, got, want)
	}
}

func TestSessionID_DifferentPIDsDiffer(t *testing.T) {
	now := time.Now()
	a := sessionID(now, 1)
	b := sessionID(now, 2)
	if a == b {
		t.Errorf("sessionID with different pids produced the same ID %q, want distinct IDs", a)
	}
}

func TestLogFilePath(t *testing.T) {
	got := logFilePath("/opt/godot-mcp-server", "20260827T030405Z-12345")
	want := "/opt/godot-mcp-server/logs/20260827T030405Z-12345.txt"
	if got != want {
		t.Errorf("logFilePath = %q, want %q", got, want)
	}
}

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
	if cfg.mode != "read-only" {
		t.Errorf("mode = %q, want default %q", cfg.mode, "read-only")
	}
}

func TestParseFlags_ExplicitReadWriteMode(t *testing.T) {
	cfg, err := parseFlags([]string{"-project", "/tmp/my-godot-project", "-mode", "read-write"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.mode != "read-write" {
		t.Errorf("mode = %q, want %q", cfg.mode, "read-write")
	}
}

func TestParseFlags_ExplicitAdvancedMode(t *testing.T) {
	cfg, err := parseFlags([]string{"-project", "/tmp/my-godot-project", "-mode", "advanced"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.mode != "advanced" {
		t.Errorf("mode = %q, want %q", cfg.mode, "advanced")
	}
}

func TestParseFlags_InvalidMode(t *testing.T) {
	_, err := parseFlags([]string{"-project", "/tmp/my-godot-project", "-mode", "read-write-please"})
	if err == nil {
		t.Fatal("parseFlags with invalid -mode, want error")
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
