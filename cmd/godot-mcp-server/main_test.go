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

// TestParseFlags_RuntimeTierDefaults confirms the TCP runtime tier's flags
// (added alongside launch_project/discover_runtime_instances/etc., the
// first runtime-tier operations in the tool allowlist) default sensibly
// when omitted. This supersedes the old TestParseFlags_NoRuntimeTierFlagsYet,
// whose own comment anticipated exactly this moment.
func TestParseFlags_RuntimeTierDefaults(t *testing.T) {
	cfg, err := parseFlags([]string{"-project", "/tmp/my-godot-project"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.runtimePortRange != "9080-9089" {
		t.Errorf("runtimePortRange = %q, want %q", cfg.runtimePortRange, "9080-9089")
	}
	if cfg.runtimeMaxInstances != 4 {
		t.Errorf("runtimeMaxInstances = %d, want 4", cfg.runtimeMaxInstances)
	}
	if cfg.runtimeOutputBufferLines != 2000 {
		t.Errorf("runtimeOutputBufferLines = %d, want 2000", cfg.runtimeOutputBufferLines)
	}
}

func TestParseFlags_RuntimePortRangeOverride(t *testing.T) {
	cfg, err := parseFlags([]string{"-project", "/tmp/my-godot-project", "-runtime-port-range", "7000-7005"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.runtimePortRange != "7000-7005" {
		t.Errorf("runtimePortRange = %q, want %q", cfg.runtimePortRange, "7000-7005")
	}
}

func TestParseFlags_RejectsMalformedRuntimePortRange(t *testing.T) {
	for _, bad := range []string{"not-a-range", "9080", "9089-9080", "0-10", "abc-def"} {
		t.Run(bad, func(t *testing.T) {
			_, err := parseFlags([]string{"-project", "/tmp/my-godot-project", "-runtime-port-range", bad})
			if err == nil {
				t.Fatalf("parseFlags with -runtime-port-range %q, want error", bad)
			}
		})
	}
}

func TestParsePortRange(t *testing.T) {
	r, err := parsePortRange("9080-9089")
	if err != nil {
		t.Fatalf("parsePortRange: %v", err)
	}
	if r.Start != 9080 || r.End != 9089 {
		t.Fatalf("parsePortRange result = %+v, want {9080 9089}", r)
	}
}
