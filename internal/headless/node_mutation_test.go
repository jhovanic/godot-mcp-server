package headless

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jhovanic/godot-mcp-server/internal/validate"
)

// AddNode/RemoveNode validate ScenePath, the scene extension, and their
// other required fields before ever invoking Godot, so these rejection
// cases are testable without a Godot binary: newDirectReadTestClient's
// garbage GodotBin would fail loudly if any of them reached exec.Command.
// The success path genuinely needs Godot — see TestAddNode_RealGodot*/
// TestRemoveNode_RealGodot* in integration_test.go.

func writeNodeMutationFixtureScene(t *testing.T, dir string) {
	t.Helper()
	const scene = "[gd_scene format=3]\n\n[node name=\"Main\" type=\"Node\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte(scene), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func TestAddNode_RejectsOutOfRootScenePath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	typeName := "Node2D"
	_, err := c.AddNode(context.Background(), AddNodeParams{
		ScenePath: "../outside.tscn",
		Name:      "Enemy",
		TypeName:  &typeName,
	})
	if err == nil {
		t.Fatal("AddNode with a traversal scene_path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("AddNode error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestAddNode_RejectsOutOfRootInstanceScenePath(t *testing.T) {
	dir := t.TempDir()
	writeNodeMutationFixtureScene(t, dir)
	c := newDirectReadTestClient(t, dir)

	instancePath := "../outside.tscn"
	_, err := c.AddNode(context.Background(), AddNodeParams{
		ScenePath:         "main.tscn",
		Name:              "Enemy",
		InstanceScenePath: &instancePath,
	})
	if err == nil {
		t.Fatal("AddNode with a traversal instance_scene_path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("AddNode error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestAddNode_RejectsNonTscnExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a scene"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	typeName := "Node2D"
	_, err := c.AddNode(context.Background(), AddNodeParams{
		ScenePath: "notes.txt",
		Name:      "Enemy",
		TypeName:  &typeName,
	})
	if err == nil {
		t.Fatal("AddNode on a non-.tscn file, want error")
	}
}

func TestAddNode_RejectsEmptyName(t *testing.T) {
	dir := t.TempDir()
	writeNodeMutationFixtureScene(t, dir)
	c := newDirectReadTestClient(t, dir)

	typeName := "Node2D"
	_, err := c.AddNode(context.Background(), AddNodeParams{
		ScenePath: "main.tscn",
		Name:      "",
		TypeName:  &typeName,
	})
	if err == nil {
		t.Fatal("AddNode with an empty name, want error")
	}
}

func TestAddNode_RejectsZeroValuesSet(t *testing.T) {
	dir := t.TempDir()
	writeNodeMutationFixtureScene(t, dir)
	c := newDirectReadTestClient(t, dir)

	_, err := c.AddNode(context.Background(), AddNodeParams{
		ScenePath: "main.tscn",
		Name:      "Enemy",
	})
	if err == nil {
		t.Fatal("AddNode with neither type_name nor instance_scene_path set, want error")
	}
	if !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("AddNode error = %v, want an \"exactly one of\" message", err)
	}
}

func TestAddNode_RejectsBothValuesSet(t *testing.T) {
	dir := t.TempDir()
	writeNodeMutationFixtureScene(t, dir)
	c := newDirectReadTestClient(t, dir)

	typeName := "Node2D"
	instancePath := "main.tscn"
	_, err := c.AddNode(context.Background(), AddNodeParams{
		ScenePath:         "main.tscn",
		Name:              "Enemy",
		TypeName:          &typeName,
		InstanceScenePath: &instancePath,
	})
	if err == nil {
		t.Fatal("AddNode with both type_name and instance_scene_path set, want error")
	}
	if !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("AddNode error = %v, want an \"exactly one of\" message", err)
	}
}

func TestAddNode_TypeNameAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	writeNodeMutationFixtureScene(t, dir)
	c := newDirectReadTestClient(t, dir)

	typeName := "Node2D"
	_, err := c.AddNode(context.Background(), AddNodeParams{
		ScenePath: "main.tscn",
		Name:      "Enemy",
		TypeName:  &typeName,
	})
	if err == nil {
		t.Fatal("AddNode with a lone type_name and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("AddNode rejected a lone type_name as if zero/multiple values were set: %v", err)
	}
}

func TestAddNode_InstanceScenePathAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	writeNodeMutationFixtureScene(t, dir)
	c := newDirectReadTestClient(t, dir)

	instancePath := "main.tscn"
	_, err := c.AddNode(context.Background(), AddNodeParams{
		ScenePath:         "main.tscn",
		Name:              "Enemy",
		InstanceScenePath: &instancePath,
	})
	if err == nil {
		t.Fatal("AddNode with a lone instance_scene_path and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("AddNode rejected a lone instance_scene_path as if zero/multiple values were set: %v", err)
	}
}

func TestRemoveNode_RejectsOutOfRootPath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	_, err := c.RemoveNode(context.Background(), RemoveNodeParams{
		ScenePath: "../outside.tscn",
		NodePath:  "World",
	})
	if err == nil {
		t.Fatal("RemoveNode with a traversal path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("RemoveNode error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestRemoveNode_RejectsNonTscnExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a scene"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.RemoveNode(context.Background(), RemoveNodeParams{
		ScenePath: "notes.txt",
		NodePath:  "World",
	})
	if err == nil {
		t.Fatal("RemoveNode on a non-.tscn file, want error")
	}
}

func TestRemoveNode_RejectsEmptyNodePath(t *testing.T) {
	dir := t.TempDir()
	writeNodeMutationFixtureScene(t, dir)
	c := newDirectReadTestClient(t, dir)

	_, err := c.RemoveNode(context.Background(), RemoveNodeParams{
		ScenePath: "main.tscn",
		NodePath:  "",
	})
	if err == nil {
		t.Fatal("RemoveNode with an empty node_path, want error")
	}
}

func TestRemoveNode_NonEmptyNodePathIsValid(t *testing.T) {
	dir := t.TempDir()
	writeNodeMutationFixtureScene(t, dir)
	c := newDirectReadTestClient(t, dir)

	_, err := c.RemoveNode(context.Background(), RemoveNodeParams{
		ScenePath: "main.tscn",
		NodePath:  "World",
	})
	if err == nil {
		t.Fatal("RemoveNode with a non-empty node_path and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "node_path is required") {
		t.Fatalf("RemoveNode rejected a non-empty node_path as if it were empty: %v", err)
	}
}
