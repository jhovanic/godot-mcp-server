package headless

import (
	"context"
	"errors"
	"testing"

	"github.com/jhovanic/godot-mcp-server/internal/validate"
)

// ReadSceneTree validates ScenePath against the project root before ever
// invoking Godot, so this is testable without a Godot binary: an
// out-of-root path must fail fast with ErrOutsideRoot and never reach
// exec.Command.
func TestReadSceneTree_RejectsOutOfRootPath(t *testing.T) {
	root, err := validate.NewRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}

	c := &Client{
		GodotBin:         "godot-should-never-be-invoked",
		OperationsScript: "/nonexistent/does/not/matter.gd",
		Root:             root,
	}

	_, err = c.ReadSceneTree(context.Background(), ReadSceneTreeParams{ScenePath: "../outside.tscn"})
	if err == nil {
		t.Fatal("ReadSceneTree with a traversal path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("ReadSceneTree error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}
