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

// These tests exercise (c *Client) SetScriptExport/SetFunctionBody's
// Go-side validation using newDirectReadTestClient's garbage GodotBin: any
// case that must be rejected before ever invoking Godot is testable here
// without a real binary. Every "alone is valid" case below also reaches
// writeScriptChecked's rollback path for free, since the garbage binary
// makes checkScriptParses fail — see TestSetScriptExport_RollsBack* /
// TestSetFunctionBody_RollsBack* for the same assertion made explicit.

const scriptEditFixtureSource = "extends Node\n\n@export var health: int = 100\n\nfunc take_damage(amount: int) -> void:\n\thealth -= amount\n"

func writeScriptEditFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "player.gd"), []byte(scriptEditFixtureSource), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func TestSetScriptExport_RejectsOutOfRootPath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	i := int64(1)
	_, err := c.SetScriptExport(context.Background(), SetScriptExportParams{
		ScriptPath: "../outside.gd",
		Name:       "health",
		IntValue:   &i,
	})
	if err == nil {
		t.Fatal("SetScriptExport with a traversal path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("SetScriptExport error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestSetScriptExport_RejectsNonGDScriptExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a script"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	i := int64(1)
	_, err := c.SetScriptExport(context.Background(), SetScriptExportParams{
		ScriptPath: "notes.txt",
		Name:       "health",
		IntValue:   &i,
	})
	if err == nil {
		t.Fatal("SetScriptExport on a non-.gd file, want error")
	}
}

func TestSetScriptExport_RejectsMissingFile(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	i := int64(1)
	_, err := c.SetScriptExport(context.Background(), SetScriptExportParams{
		ScriptPath: "does_not_exist.gd",
		Name:       "health",
		IntValue:   &i,
	})
	if err == nil {
		t.Fatal("SetScriptExport on a nonexistent script, want error")
	}
}

func TestSetScriptExport_RejectsEmptyName(t *testing.T) {
	dir := t.TempDir()
	writeScriptEditFixture(t, dir)
	c := newDirectReadTestClient(t, dir)

	i := int64(1)
	_, err := c.SetScriptExport(context.Background(), SetScriptExportParams{
		ScriptPath: "player.gd",
		Name:       "",
		IntValue:   &i,
	})
	if err == nil {
		t.Fatal("SetScriptExport with an empty name, want error")
	}
}

func TestSetScriptExport_RejectsInvalidIdentifierName(t *testing.T) {
	dir := t.TempDir()
	writeScriptEditFixture(t, dir)
	c := newDirectReadTestClient(t, dir)

	for _, name := range []string{"123abc", "has space", "has-dash", "has.dot"} {
		t.Run(name, func(t *testing.T) {
			i := int64(1)
			_, err := c.SetScriptExport(context.Background(), SetScriptExportParams{
				ScriptPath: "player.gd",
				Name:       name,
				IntValue:   &i,
			})
			if err == nil {
				t.Fatalf("SetScriptExport with invalid identifier %q, want error", name)
			}
		})
	}
}

func TestSetScriptExport_RejectsZeroValuesSet(t *testing.T) {
	dir := t.TempDir()
	writeScriptEditFixture(t, dir)
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetScriptExport(context.Background(), SetScriptExportParams{
		ScriptPath: "player.gd",
		Name:       "health",
	})
	if err == nil {
		t.Fatal("SetScriptExport with zero values set, want error")
	}
	if !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetScriptExport error = %v, want an \"exactly one of\" message", err)
	}
}

func TestSetScriptExport_RejectsMultipleValuesSet(t *testing.T) {
	dir := t.TempDir()
	writeScriptEditFixture(t, dir)
	c := newDirectReadTestClient(t, dir)

	i := int64(1)
	s := "hello"
	_, err := c.SetScriptExport(context.Background(), SetScriptExportParams{
		ScriptPath:  "player.gd",
		Name:        "health",
		IntValue:    &i,
		StringValue: &s,
	})
	if err == nil {
		t.Fatal("SetScriptExport with both int_value and string_value set, want error")
	}
	if !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetScriptExport error = %v, want an \"exactly one of\" message", err)
	}
}

// TestSetScriptExport_ValueTypesAloneAreValidAndRollBack proves each of the
// 7 value fields alone passes Go-side validation (reaches the
// checkScriptParses step, which the garbage GodotBin then fails), and that
// writeScriptChecked's rollback leaves the file byte-identical to before
// the call.
func TestSetScriptExport_ValueTypesAloneAreValidAndRollBack(t *testing.T) {
	str := "hello"
	i := int64(5)
	f := 2.5
	b := true
	v2 := Vector2{X: 1, Y: 2}
	v3 := Vector3{X: 1, Y: 2, Z: 3}
	col := Color{R: 1, G: 0, B: 0, A: 1}

	tests := []struct {
		name   string
		params SetScriptExportParams
	}{
		{"StringValue", SetScriptExportParams{StringValue: &str}},
		{"IntValue", SetScriptExportParams{IntValue: &i}},
		{"FloatValue", SetScriptExportParams{FloatValue: &f}},
		{"BoolValue", SetScriptExportParams{BoolValue: &b}},
		{"Vector2Value", SetScriptExportParams{Vector2Value: &v2}},
		{"Vector3Value", SetScriptExportParams{Vector3Value: &v3}},
		{"ColorValue", SetScriptExportParams{ColorValue: &col}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeScriptEditFixture(t, dir)
			c := newDirectReadTestClient(t, dir)

			params := tt.params
			params.ScriptPath = "player.gd"
			params.Name = "health"
			_, err := c.SetScriptExport(context.Background(), params)
			if err == nil {
				t.Fatalf("SetScriptExport with a lone %s and a garbage GodotBin, want an error from the check-only step", tt.name)
			}
			if strings.Contains(err.Error(), "exactly one of") {
				t.Fatalf("SetScriptExport rejected a lone %s as if zero/multiple values were set: %v", tt.name, err)
			}

			data, readErr := os.ReadFile(filepath.Join(dir, "player.gd"))
			if readErr != nil {
				t.Fatalf("reading fixture after rollback: %v", readErr)
			}
			if string(data) != scriptEditFixtureSource {
				t.Errorf("file was not rolled back to its original content: %q", data)
			}
		})
	}
}

func TestSetScriptExport_RollsBackFileOnVerificationInfraFailure(t *testing.T) {
	dir := t.TempDir()
	writeScriptEditFixture(t, dir)
	c := newDirectReadTestClient(t, dir)

	i := int64(200)
	_, err := c.SetScriptExport(context.Background(), SetScriptExportParams{
		ScriptPath: "player.gd",
		Name:       "health",
		IntValue:   &i,
	})
	if err == nil {
		t.Fatal("SetScriptExport against a garbage GodotBin, want error")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("SetScriptExport error = %v, want it to mention the rollback", err)
	}

	data, readErr := os.ReadFile(filepath.Join(dir, "player.gd"))
	if readErr != nil {
		t.Fatalf("reading fixture after rollback: %v", readErr)
	}
	if string(data) != scriptEditFixtureSource {
		t.Errorf("file was not rolled back to its original content: %q", data)
	}
}

func TestSetFunctionBody_RejectsOutOfRootPath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	_, err := c.SetFunctionBody(context.Background(), SetFunctionBodyParams{
		ScriptPath:   "../outside.gd",
		FunctionName: "take_damage",
		Body:         "pass",
	})
	if err == nil {
		t.Fatal("SetFunctionBody with a traversal path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("SetFunctionBody error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestSetFunctionBody_RejectsNonGDScriptExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a script"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetFunctionBody(context.Background(), SetFunctionBodyParams{
		ScriptPath:   "notes.txt",
		FunctionName: "take_damage",
		Body:         "pass",
	})
	if err == nil {
		t.Fatal("SetFunctionBody on a non-.gd file, want error")
	}
}

func TestSetFunctionBody_RejectsMissingFile(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	_, err := c.SetFunctionBody(context.Background(), SetFunctionBodyParams{
		ScriptPath:   "does_not_exist.gd",
		FunctionName: "take_damage",
		Body:         "pass",
	})
	if err == nil {
		t.Fatal("SetFunctionBody on a nonexistent script, want error")
	}
}

func TestSetFunctionBody_RejectsEmptyFunctionName(t *testing.T) {
	dir := t.TempDir()
	writeScriptEditFixture(t, dir)
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetFunctionBody(context.Background(), SetFunctionBodyParams{
		ScriptPath:   "player.gd",
		FunctionName: "",
		Body:         "pass",
	})
	if err == nil {
		t.Fatal("SetFunctionBody with an empty function_name, want error")
	}
}

func TestSetFunctionBody_RejectsInvalidIdentifierName(t *testing.T) {
	dir := t.TempDir()
	writeScriptEditFixture(t, dir)
	c := newDirectReadTestClient(t, dir)

	for _, name := range []string{"123abc", "has space", "has-dash"} {
		t.Run(name, func(t *testing.T) {
			_, err := c.SetFunctionBody(context.Background(), SetFunctionBodyParams{
				ScriptPath:   "player.gd",
				FunctionName: name,
				Body:         "pass",
			})
			if err == nil {
				t.Fatalf("SetFunctionBody with invalid identifier %q, want error", name)
			}
		})
	}
}

func TestSetFunctionBody_RejectsEmptyBody(t *testing.T) {
	dir := t.TempDir()
	writeScriptEditFixture(t, dir)
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetFunctionBody(context.Background(), SetFunctionBodyParams{
		ScriptPath:   "player.gd",
		FunctionName: "take_damage",
		Body:         "",
	})
	if err == nil {
		t.Fatal("SetFunctionBody with an empty body, want error")
	}
}

func TestSetFunctionBody_RejectsMultiLineSignatureBeforeInvokingGodot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "player.gd"), []byte("extends Node\n\nfunc take_damage(\n\tamount: int,\n) -> void:\n\tpass\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetFunctionBody(context.Background(), SetFunctionBodyParams{
		ScriptPath:   "player.gd",
		FunctionName: "take_damage",
		Body:         "pass",
	})
	if err == nil {
		t.Fatal("SetFunctionBody against a multi-line signature, want error")
	}
	if strings.Contains(err.Error(), "running godot") {
		t.Fatalf("SetFunctionBody reached exec instead of being rejected by the pure splice function first: %v", err)
	}
}

func TestSetFunctionBody_RollsBackFileOnVerificationInfraFailure(t *testing.T) {
	dir := t.TempDir()
	writeScriptEditFixture(t, dir)
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetFunctionBody(context.Background(), SetFunctionBodyParams{
		ScriptPath:   "player.gd",
		FunctionName: "take_damage",
		Body:         "health -= amount * 2",
	})
	if err == nil {
		t.Fatal("SetFunctionBody against a garbage GodotBin, want error")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("SetFunctionBody error = %v, want it to mention the rollback", err)
	}

	data, readErr := os.ReadFile(filepath.Join(dir, "player.gd"))
	if readErr != nil {
		t.Fatalf("reading fixture after rollback: %v", readErr)
	}
	if string(data) != scriptEditFixtureSource {
		t.Errorf("file was not rolled back to its original content: %q", data)
	}
}
