package headless

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

func fixtureClient(t *testing.T) *Client {
	t.Helper()

	root, err := validate.NewRoot("testdata/fixture_project")
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}

	opsScript, err := filepath.Abs(filepath.Join("..", "..", "scripts", "godot_operations.gd"))
	if err != nil {
		t.Fatalf("resolving operations script path: %v", err)
	}
	if _, err := os.Stat(opsScript); err != nil {
		t.Fatalf("operations script not found at %s: %v", opsScript, err)
	}

	return &Client{
		GodotBin:         godotBinForTest(t),
		OperationsScript: opsScript,
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
