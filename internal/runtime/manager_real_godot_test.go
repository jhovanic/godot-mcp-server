package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jhovanic/godot-mcp-server/internal/audit"
	"github.com/jhovanic/godot-mcp-server/internal/validate"
)

// This file exercises internal/runtime against a real Godot binary,
// including — for the first time — the shipped autoload template
// (scripts/mcp_runtime_autoload.gd) actually running inside a live Godot
// process. Skips itself when no Godot binary is available, matching every
// other real-Godot test in this repo (see internal/headless/integration_test.go's
// own doc comment).

func godotBinForTest(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("GODOT_BIN"); bin != "" {
		return bin
	}
	if bin, err := exec.LookPath("godot"); err == nil {
		return bin
	}
	t.Skip("no Godot binary available: set GODOT_BIN or put `godot` on PATH to run this test")
	return ""
}

// outputFixtureManager builds a fresh temp-dir project with a script that
// prints two known lines on _ready() and then idles forever (no quit()
// call) — exactly what's needed to test reading output from a still-running
// process and then actually stopping it.
func outputFixtureManager(t *testing.T) *Manager {
	t.Helper()
	godotBin := godotBinForTest(t)
	projectDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	const mainScript = `extends Node

func _ready() -> void:
	print("hello from fixture")
	print("second line")
`
	if err := os.WriteFile(filepath.Join(projectDir, "main.gd"), []byte(mainScript), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	const mainScene = `[gd_scene load_steps=2 format=3]

[ext_resource type="Script" path="res://main.gd" id="1_main"]

[node name="Main" type="Node"]
script = ExtResource("1_main")
`
	if err := os.WriteFile(filepath.Join(projectDir, "main.tscn"), []byte(mainScene), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	var logBuf discardWriter
	return NewManager(godotBin, root, audit.New(logBuf), 4, 2000)
}

func TestManager_RealGodot_LaunchReadStop(t *testing.T) {
	m := outputFixtureManager(t)

	scenePath := "main.tscn"
	launchResult, err := m.LaunchProject(context.Background(), LaunchProjectParams{ScenePath: &scenePath})
	if err != nil {
		t.Fatalf("LaunchProject against a real Godot binary: %v", err)
	}

	var readResult *ReadRuntimeOutputResult
	var sawFirst, sawSecond bool
	waitForTimeout(t, 20*time.Second, func() bool {
		readResult, err = m.ReadRuntimeOutput(context.Background(), ReadRuntimeOutputParams{RunID: launchResult.RunID})
		if err != nil {
			t.Fatalf("ReadRuntimeOutput: %v", err)
		}
		for _, l := range readResult.Lines {
			if l.Text == "hello from fixture" {
				sawFirst = true
			}
			if l.Text == "second line" {
				sawSecond = true
			}
		}
		return sawFirst && sawSecond
	})

	if readResult.ProcessExited {
		t.Error("ProcessExited = true while the fixture should still be idling")
	}

	stopResult, err := m.StopRuntime(context.Background(), StopRuntimeParams{RunID: launchResult.RunID})
	if err != nil {
		t.Fatalf("StopRuntime: %v", err)
	}
	if stopResult.AlreadyExited {
		t.Error("StopRuntime reported AlreadyExited=true for a process that was still running")
	}

	waitFor(t, func() bool {
		readResult, err = m.ReadRuntimeOutput(context.Background(), ReadRuntimeOutputParams{RunID: launchResult.RunID})
		if err != nil {
			t.Fatalf("ReadRuntimeOutput: %v", err)
		}
		return readResult.ProcessExited
	})
}

// runtimeFixtureProject builds a fresh temp-dir project with the real,
// shipped scripts/mcp_runtime_autoload.gd copied in and registered as an
// autoload, plus a scene with a script-attached node exposing an @export
// property — the fixture for discover_runtime_instances/
// read_runtime_scene_tree/read_runtime_node_property. portRange overrides
// the template's own PORT_RANGE_START/END constants, so tests can prove the
// "operator changes the range on both sides, matching by convention" story
// actually works against a real engine, not just the shipped default.
func runtimeFixtureProject(t *testing.T, portRange PortRange) (projectDir string) {
	t.Helper()
	projectDir = t.TempDir()

	autoloadSrc, err := filepath.Abs(filepath.Join("..", "..", "scripts", "mcp_runtime_autoload.gd"))
	if err != nil {
		t.Fatalf("resolving autoload template path: %v", err)
	}
	autoloadBytes, err := os.ReadFile(autoloadSrc)
	if err != nil {
		t.Fatalf("reading autoload template: %v", err)
	}
	autoloadText := string(autoloadBytes)
	if !strings.Contains(autoloadText, "const PORT_RANGE_START := 9080") || !strings.Contains(autoloadText, "const PORT_RANGE_END := 9089") {
		t.Fatal("autoload template's PORT_RANGE_START/END constants have changed shape — update this test's string replacement to match")
	}
	autoloadText = strings.Replace(autoloadText, "const PORT_RANGE_START := 9080", fmt.Sprintf("const PORT_RANGE_START := %d", portRange.Start), 1)
	autoloadText = strings.Replace(autoloadText, "const PORT_RANGE_END := 9089", fmt.Sprintf("const PORT_RANGE_END := %d", portRange.End), 1)
	if err := os.WriteFile(filepath.Join(projectDir, "mcp_runtime_autoload.gd"), []byte(autoloadText), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	const projectGodot = `config_version=5

[autoload]

McpRuntime="*res://mcp_runtime_autoload.gd"
`
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte(projectGodot), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	const playerScript = `extends CharacterBody2D

@export var health: int = 100
`
	if err := os.WriteFile(filepath.Join(projectDir, "player.gd"), []byte(playerScript), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	const mainScene = `[gd_scene load_steps=2 format=3]

[ext_resource type="Script" path="res://player.gd" id="1_player"]

[node name="Main" type="Node2D"]

[node name="Player" type="CharacterBody2D" parent="."]
script = ExtResource("1_player")
`
	if err := os.WriteFile(filepath.Join(projectDir, "main.tscn"), []byte(mainScene), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	return projectDir
}

// TestRuntimeTier_RealGodot_AutoloadDiscoveryAndLiveReads is the
// load-bearing test for the whole capability-B design: the shipped
// autoload template has never run against a real engine before this.
func TestRuntimeTier_RealGodot_AutoloadDiscoveryAndLiveReads(t *testing.T) {
	godotBin := godotBinForTest(t)
	projectDir := runtimeFixtureProject(t, DefaultPortRange)

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	var discard discardWriter
	m := NewManager(godotBin, root, audit.New(discard), 4, 2000)

	scenePath := "main.tscn"
	launchResult, err := m.LaunchProject(context.Background(), LaunchProjectParams{ScenePath: &scenePath})
	if err != nil {
		t.Fatalf("LaunchProject against a real Godot binary: %v", err)
	}
	t.Cleanup(func() {
		_, _ = m.StopRuntime(context.Background(), StopRuntimeParams{RunID: launchResult.RunID})
	})

	var instances []Instance
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitForTimeout(t, 20*time.Second, func() bool {
		instances, err = DiscoverInstances(ctx, DefaultPortRange, audit.New(discard))
		if err != nil {
			t.Fatalf("DiscoverInstances: %v", err)
		}
		return len(instances) > 0
	})
	if len(instances) != 1 {
		t.Fatalf("DiscoverInstances found %d instances, want 1: %+v", len(instances), instances)
	}

	client := &Client{Port: instances[0].Port, Logger: audit.New(discard)}

	tree, err := client.ReadRuntimeSceneTree(ctx)
	if err != nil {
		t.Fatalf("ReadRuntimeSceneTree against a real Godot binary: %v", err)
	}
	if tree.Name != "Main" || len(tree.Children) != 1 || tree.Children[0].Name != "Player" {
		t.Fatalf("unexpected live scene tree: %+v", tree)
	}
	if tree.Children[0].Path != "Player" {
		t.Errorf("Player's live path = %q, want %q", tree.Children[0].Path, "Player")
	}

	prop, err := client.ReadRuntimeNodeProperty(ctx, "Player", "health")
	if err != nil {
		t.Fatalf("ReadRuntimeNodeProperty against a real Godot binary: %v", err)
	}
	if prop.Value != "100" || prop.Type != "int" {
		t.Fatalf("unexpected live property result: %+v", prop)
	}

	if _, err := client.ReadRuntimeNodeProperty(ctx, "Player", "does_not_exist"); err == nil {
		t.Fatal("ReadRuntimeNodeProperty against an unknown property, want error")
	}
}

// TestRuntimeTier_RealGodot_CustomPortRangeMustMatchOnBothSides proves the
// "operator changes the range on both sides, matching by convention" story
// documented in README.md actually works against a real engine — and,
// just as importantly, that scanning the *wrong* range genuinely finds
// nothing, so a mismatched configuration fails loudly (an empty result)
// rather than accidentally succeeding.
func TestRuntimeTier_RealGodot_CustomPortRangeMustMatchOnBothSides(t *testing.T) {
	godotBin := godotBinForTest(t)
	customRange := PortRange{Start: 9500, End: 9505}
	projectDir := runtimeFixtureProject(t, customRange)

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	var discard discardWriter
	m := NewManager(godotBin, root, audit.New(discard), 4, 2000)

	scenePath := "main.tscn"
	launchResult, err := m.LaunchProject(context.Background(), LaunchProjectParams{ScenePath: &scenePath})
	if err != nil {
		t.Fatalf("LaunchProject against a real Godot binary: %v", err)
	}
	t.Cleanup(func() {
		_, _ = m.StopRuntime(context.Background(), StopRuntimeParams{RunID: launchResult.RunID})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Scanning the shipped default range must find nothing: the autoload
	// in this fixture was configured to bind customRange instead, so a
	// mismatched configuration must fail loudly (empty), not accidentally
	// still work.
	defaultScanInstances, err := DiscoverInstances(ctx, DefaultPortRange, audit.New(discard))
	if err != nil {
		t.Fatalf("DiscoverInstances (default range): %v", err)
	}
	if len(defaultScanInstances) != 0 {
		t.Fatalf("DiscoverInstances against the default range found %d instances, want 0 (autoload uses a custom range in this fixture): %+v", len(defaultScanInstances), defaultScanInstances)
	}

	var instances []Instance
	waitForTimeout(t, 20*time.Second, func() bool {
		instances, err = DiscoverInstances(ctx, customRange, audit.New(discard))
		if err != nil {
			t.Fatalf("DiscoverInstances (custom range): %v", err)
		}
		return len(instances) > 0
	})
	if len(instances) != 1 {
		t.Fatalf("DiscoverInstances against the matching custom range found %d instances, want 1: %+v", len(instances), instances)
	}
	if instances[0].Port < customRange.Start || instances[0].Port > customRange.End {
		t.Errorf("discovered port %d is outside the configured custom range %+v", instances[0].Port, customRange)
	}
}
