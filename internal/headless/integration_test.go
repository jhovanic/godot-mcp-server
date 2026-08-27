package headless

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jhovanic/godot-mcp-server/internal/validate"
)

// This file exercises internal/headless (and scripts/godot_operations.gd)
// against a real Godot binary. It's what should have caught the
// stdout-startup-banner bug (see TestParseResponse_IgnoresEngineStartupBanner
// in headless_test.go for the regression unit test): that bug only showed
// up once a real engine was talking to us, and every unit test in this
// package stubs that away.
//
// Skips itself when no Godot binary is available, so `go test ./...` still
// passes in a plain dev environment. CI installs a pinned, checksum-verified
// Godot and sets GODOT_BIN so this actually runs there — see ci.yml.

// godotBinForTest locates a real Godot binary to test against, or skips the
// calling test if none is configured.
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

// operationsScriptPath resolves the real scripts/godot_operations.gd this
// repo ships, shared by every test in this file that constructs its own
// *Client.
func operationsScriptPath(t *testing.T) string {
	t.Helper()
	opsScript, err := filepath.Abs(filepath.Join("..", "..", "scripts", "godot_operations.gd"))
	if err != nil {
		t.Fatalf("resolving operations script path: %v", err)
	}
	if _, err := os.Stat(opsScript); err != nil {
		t.Fatalf("operations script not found at %s: %v", opsScript, err)
	}
	return opsScript
}

func fixtureClient(t *testing.T) *Client {
	t.Helper()

	root, err := validate.NewRoot("testdata/fixture_project")
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}

	return &Client{
		GodotBin:         godotBinForTest(t),
		OperationsScript: operationsScriptPath(t),
		Root:             root,
	}
}

func TestReadSceneTree_RealGodot(t *testing.T) {
	c := fixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	node, err := c.ReadSceneTree(ctx, ReadSceneTreeParams{ScenePath: "main.tscn"})
	if err != nil {
		t.Fatalf("ReadSceneTree against a real Godot binary: %v", err)
	}

	want := &SceneNode{
		Name: "Main",
		Type: "Node",
		Children: []SceneNode{
			{
				Name: "World",
				Type: "Node2D",
				Children: []SceneNode{
					{Name: "Player", Type: "CharacterBody2D"},
				},
			},
		},
	}

	gotJSON, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshaling got: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshaling want: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("scene tree mismatch:\n got:  %s\n want: %s", gotJSON, wantJSON)
	}
}

func TestReadSceneTree_RealGodot_MissingScene(t *testing.T) {
	c := fixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// In-root but nonexistent: passes Go-side path validation, so this
	// exercises the operations script's own _err("no scene resource...")
	// path and the round trip of a real ok:false response.
	_, err := c.ReadSceneTree(ctx, ReadSceneTreeParams{ScenePath: "does_not_exist.tscn"})
	if err == nil {
		t.Fatal("ReadSceneTree for a missing scene, want error")
	}
}

// generateResFixture creates a real .res binary resource file via a one-off
// Godot invocation, so TestReadBinaryResource_RealGodot can exercise an
// actual binary resource without a committed binary blob in testdata/ (a
// .res file isn't hand-authorable text the way .tscn/.tres fixtures are).
// This is test-only setup, not part of this server's fixed operation set —
// it doesn't go through scripts/godot_operations.gd or internal/headless
// at all.
func generateResFixture(t *testing.T, godotBin, projectDir string) {
	t.Helper()

	const setupScript = `extends SceneTree

func _init() -> void:
	var mat := StandardMaterial3D.new()
	mat.albedo_color = Color(1, 0, 0, 1)
	mat.roughness = 0.4
	var err := ResourceSaver.save(mat, "res://red.res")
	if err != OK:
		push_error("failed to save fixture: %d" % err)
	quit()
`
	scriptPath := filepath.Join(projectDir, "generate_res_fixture.gd")
	if err := os.WriteFile(scriptPath, []byte(setupScript), 0o644); err != nil {
		t.Fatalf("writing fixture-generation script: %v", err)
	}

	// #nosec G204 -- test-only, fixed argv, godotBin/projectDir/scriptPath
	// are all test-controlled, not external input.
	cmd := exec.Command(godotBin, "--headless", "--path", projectDir, "--script", scriptPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating .res fixture: %v\n%s", err, out)
	}
}

func TestReadBinaryResource_RealGodot(t *testing.T) {
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	generateResFixture(t, godotBin, projectDir)

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	c := &Client{GodotBin: godotBin, OperationsScript: operationsScriptPath(t), Root: root}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got, err := c.ReadBinaryResource(ctx, ReadBinaryResourceParams{ResourcePath: "red.res"})
	if err != nil {
		t.Fatalf("ReadBinaryResource against a real Godot binary: %v", err)
	}

	if got.Path != "res://red.res" {
		t.Errorf("Path = %q, want %q", got.Path, "res://red.res")
	}
	if !strings.Contains(got.Source, `type="StandardMaterial3D"`) {
		t.Errorf("Source missing expected resource type header: %s", got.Source)
	}
	if !strings.Contains(got.Source, "albedo_color = Color(1, 0, 0, 1)") {
		t.Errorf("Source missing expected property: %s", got.Source)
	}
	if strings.Contains(got.Source, "ao_enabled") {
		t.Errorf("Source includes untouched engine defaults, want only properties actually set: %s", got.Source)
	}
}

func TestReadBinaryResource_RealGodot_MissingResource(t *testing.T) {
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	c := &Client{GodotBin: godotBin, OperationsScript: operationsScriptPath(t), Root: root}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// In-root but nonexistent: passes Go-side path validation, so this
	// exercises the operations script's own _err(...) path for
	// read_binary_resource specifically.
	_, err = c.ReadBinaryResource(ctx, ReadBinaryResourceParams{ResourcePath: "does_not_exist.res"})
	if err == nil {
		t.Fatal("ReadBinaryResource for a missing resource, want error")
	}
}
