package headless

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jhovanic/godot-mcp-server/internal/validate"
)

// Regression coverage for the bug where Godot's own engine startup banner
// (written to stdout before any user script code runs) got swept into the
// same buffer as the operations script's single JSON output line, and
// parseResponse (formerly inlined in run()) tried to decode the whole
// buffer as one JSON document. See the bug report: "decoding godot
// response: invalid character 'G'" on every headless call.
func TestParseResponse_IgnoresEngineStartupBanner(t *testing.T) {
	stdout := "Godot Engine v4.7.1.stable.mono.official.a13da4feb - https://godotengine.org\n" +
		`{"ok":true,"result":{"name":"Main","type":"Node"}}` + "\n"

	resp, err := parseResponse([]byte(stdout))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resp.OK = false, want true: %+v", resp)
	}

	var node SceneNode
	if err := json.Unmarshal(resp.Result, &node); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if node.Name != "Main" || node.Type != "Node" {
		t.Fatalf("unexpected node: %+v", node)
	}
}

func TestParseResponse_NoBannerStillWorks(t *testing.T) {
	stdout := `{"ok":true,"result":{"name":"Main","type":"Node"}}` + "\n"

	resp, err := parseResponse([]byte(stdout))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resp.OK = false, want true: %+v", resp)
	}
}

func TestParseResponse_TrailingBlankLinesIgnored(t *testing.T) {
	stdout := "Godot Engine v4.7.1.stable.mono.official.a13da4feb - https://godotengine.org\n" +
		`{"ok":true,"result":{"name":"Main","type":"Node"}}` + "\n\n\n"

	resp, err := parseResponse([]byte(stdout))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resp.OK = false, want true: %+v", resp)
	}
}

func TestParseResponse_NoJSONLine(t *testing.T) {
	stdout := "Godot Engine v4.7.1.stable.mono.official.a13da4feb - https://godotengine.org\n"

	if _, err := parseResponse([]byte(stdout)); err == nil {
		t.Fatal("parseResponse with no JSON line, want error")
	}
}

func TestParseResponse_EmptyStdout(t *testing.T) {
	if _, err := parseResponse(nil); err == nil {
		t.Fatal("parseResponse on empty stdout, want error")
	}
}

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

// ReadScript and ReadProjectSettings never invoke Godot at all — both read
// plain text files directly, so there's no engine capability either
// operation needs. GodotBin and OperationsScript are set to garbage below
// specifically to prove that: if either ever started shelling out, these
// tests would fail loudly instead of silently doing the wrong thing.
func newDirectReadTestClient(t *testing.T, projectRoot string) *Client {
	t.Helper()
	root, err := validate.NewRoot(projectRoot)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return &Client{
		GodotBin:         "godot-should-never-be-invoked-for-a-plain-file-read",
		OperationsScript: "/nonexistent/does/not/matter.gd",
		Root:             root,
	}
}

func TestReadScript_Success(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	const source = "extends Node\n\nfunc _ready() -> void:\n\tprint(\"hello\")\n"
	if err := os.WriteFile(filepath.Join(dir, "scripts", "player.gd"), []byte(source), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c := newDirectReadTestClient(t, dir)

	got, err := c.ReadScript(context.Background(), ReadScriptParams{ScriptPath: "scripts/player.gd"})
	if err != nil {
		t.Fatalf("ReadScript: %v", err)
	}
	if got.Source != source {
		t.Fatalf("Source = %q, want %q", got.Source, source)
	}
	if got.Path != "res://scripts/player.gd" {
		t.Fatalf("Path = %q, want %q", got.Path, "res://scripts/player.gd")
	}
}

func TestReadScript_RejectsOutOfRootPath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	_, err := c.ReadScript(context.Background(), ReadScriptParams{ScriptPath: "../outside.gd"})
	if err == nil {
		t.Fatal("ReadScript with a traversal path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("ReadScript error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestReadScript_RejectsNonGDScriptExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a script"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c := newDirectReadTestClient(t, dir)

	_, err := c.ReadScript(context.Background(), ReadScriptParams{ScriptPath: "notes.txt"})
	if err == nil {
		t.Fatal("ReadScript on a non-.gd file, want error")
	}
}

func TestReadScript_MissingFile(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	_, err := c.ReadScript(context.Background(), ReadScriptParams{ScriptPath: "does_not_exist.gd"})
	if err == nil {
		t.Fatal("ReadScript on a missing file, want error")
	}
}

func TestReadProjectSettings_Success(t *testing.T) {
	dir := t.TempDir()
	const source = "config_version=5\n\n[application]\n\nconfig/name=\"Fixture\"\n"
	if err := os.WriteFile(filepath.Join(dir, "project.godot"), []byte(source), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c := newDirectReadTestClient(t, dir)

	got, err := c.ReadProjectSettings(context.Background(), ReadProjectSettingsParams{})
	if err != nil {
		t.Fatalf("ReadProjectSettings: %v", err)
	}
	if got.Source != source {
		t.Fatalf("Source = %q, want %q", got.Source, source)
	}
	if got.Path != "res://project.godot" {
		t.Fatalf("Path = %q, want %q", got.Path, "res://project.godot")
	}
}

func TestReadProjectSettings_MissingFile(t *testing.T) {
	// A project root with no project.godot at all — e.g. Root pointed at a
	// directory that isn't actually a Godot project.
	c := newDirectReadTestClient(t, t.TempDir())

	_, err := c.ReadProjectSettings(context.Background(), ReadProjectSettingsParams{})
	if err == nil {
		t.Fatal("ReadProjectSettings with no project.godot present, want error")
	}
}
