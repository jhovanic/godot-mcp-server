package headless

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// ReadScript, ReadProjectSettings, and ReadTextResource never invoke Godot
// at all — each reads a plain text file directly, so there's no engine
// capability any of them needs. GodotBin and OperationsScript are set to
// garbage below specifically to prove that: if any of them ever started
// shelling out, these tests would fail loudly instead of silently doing the
// wrong thing.
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

func TestReadTextResource_Success(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "materials"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	const source = "[gd_resource type=\"StandardMaterial3D\" format=3]\n\n[resource]\nalbedo_color = Color(1, 0, 0, 1)\n"
	if err := os.WriteFile(filepath.Join(dir, "materials", "red.tres"), []byte(source), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c := newDirectReadTestClient(t, dir)

	got, err := c.ReadTextResource(context.Background(), ReadTextResourceParams{ResourcePath: "materials/red.tres"})
	if err != nil {
		t.Fatalf("ReadTextResource: %v", err)
	}
	if got.Source != source {
		t.Fatalf("Source = %q, want %q", got.Source, source)
	}
	if got.Path != "res://materials/red.tres" {
		t.Fatalf("Path = %q, want %q", got.Path, "res://materials/red.tres")
	}
}

func TestReadTextResource_RejectsOutOfRootPath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	_, err := c.ReadTextResource(context.Background(), ReadTextResourceParams{ResourcePath: "../outside.tres"})
	if err == nil {
		t.Fatal("ReadTextResource with a traversal path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("ReadTextResource error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestReadTextResource_RejectsBinaryRes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "packed.res"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c := newDirectReadTestClient(t, dir)

	_, err := c.ReadTextResource(context.Background(), ReadTextResourceParams{ResourcePath: "packed.res"})
	if err == nil {
		t.Fatal("ReadTextResource on a .res file, want error")
	}
}

func TestReadTextResource_RejectsOtherExtensions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "icon.png"), []byte("not a resource"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c := newDirectReadTestClient(t, dir)

	_, err := c.ReadTextResource(context.Background(), ReadTextResourceParams{ResourcePath: "icon.png"})
	if err == nil {
		t.Fatal("ReadTextResource on a non-.tres file, want error")
	}
}

func TestReadTextResource_MissingFile(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	_, err := c.ReadTextResource(context.Background(), ReadTextResourceParams{ResourcePath: "does_not_exist.tres"})
	if err == nil {
		t.Fatal("ReadTextResource on a missing file, want error")
	}
}

// Unlike ReadScript/ReadProjectSettings/ReadTextResource, ReadBinaryResource
// does invoke Godot for a valid .res path — but path and extension
// rejection both happen before that, so these specific cases are still
// testable without a Godot binary: newDirectReadTestClient's garbage
// GodotBin would fail loudly if these ever reached exec.Command.

func TestReadBinaryResource_RejectsOutOfRootPath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	_, err := c.ReadBinaryResource(context.Background(), ReadBinaryResourceParams{ResourcePath: "../outside.res"})
	if err == nil {
		t.Fatal("ReadBinaryResource with a traversal path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("ReadBinaryResource error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestReadBinaryResource_RejectsTextResource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "red.tres"), []byte("[gd_resource type=\"StandardMaterial3D\" format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c := newDirectReadTestClient(t, dir)

	_, err := c.ReadBinaryResource(context.Background(), ReadBinaryResourceParams{ResourcePath: "red.tres"})
	if err == nil {
		t.Fatal("ReadBinaryResource on a .tres file, want error")
	}
}

func TestReadBinaryResource_RejectsOtherExtensions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "icon.png"), []byte("not a resource"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c := newDirectReadTestClient(t, dir)

	_, err := c.ReadBinaryResource(context.Background(), ReadBinaryResourceParams{ResourcePath: "icon.png"})
	if err == nil {
		t.Fatal("ReadBinaryResource on a non-.res file, want error")
	}
}

// ReadImportSettings never invokes Godot: .import files are plain
// ConfigFile-style text, the same as project.godot, so there's no engine
// capability this operation needs.

func TestReadImportSettings_Success(t *testing.T) {
	dir := t.TempDir()
	const source = "[remap]\n\nimporter=\"texture\"\ntype=\"CompressedTexture2D\"\n"
	if err := os.WriteFile(filepath.Join(dir, "icon.png"), []byte("not a real png, just needs to exist"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "icon.png.import"), []byte(source), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c := newDirectReadTestClient(t, dir)

	got, err := c.ReadImportSettings(context.Background(), ReadImportSettingsParams{AssetPath: "icon.png"})
	if err != nil {
		t.Fatalf("ReadImportSettings: %v", err)
	}
	if got.Source != source {
		t.Fatalf("Source = %q, want %q", got.Source, source)
	}
	if got.Path != "res://icon.png.import" {
		t.Fatalf("Path = %q, want %q", got.Path, "res://icon.png.import")
	}
}

func TestReadImportSettings_RejectsOutOfRootPath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	_, err := c.ReadImportSettings(context.Background(), ReadImportSettingsParams{AssetPath: "../outside.png"})
	if err == nil {
		t.Fatal("ReadImportSettings with a traversal path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("ReadImportSettings error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

// SetNodeProperty validates ScenePath, the scene extension, and the
// exactly-one-value-set constraint before ever invoking Godot, so these
// rejection cases are testable without a Godot binary: newDirectReadTestClient's
// garbage GodotBin would fail loudly if any of them reached exec.Command.
// The success path genuinely needs Godot — see TestSetNodeProperty_RealGodot*
// in integration_test.go.

func TestSetNodeProperty_RejectsOutOfRootPath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	strVal := "hello"
	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "../outside.tscn",
		PropertyName: "text",
		StringValue:  &strVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a traversal path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("SetNodeProperty error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestSetNodeProperty_RejectsNonTscnExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a scene"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	boolVal := true
	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "notes.txt",
		PropertyName: "visible",
		BoolValue:    &boolVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty on a non-.tscn file, want error")
	}
}

func TestSetNodeProperty_RejectsZeroValuesSet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "visible",
	})
	if err == nil {
		t.Fatal("SetNodeProperty with no value field set, want error")
	}
}

func TestSetNodeProperty_RejectsMultipleValuesSet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	intVal := int64(1)
	boolVal := true
	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "visible",
		IntValue:     &intVal,
		BoolValue:    &boolVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty with two value fields set, want error")
	}
}

func TestSetNodeProperty_RejectsEmptyPropertyName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	boolVal := true
	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath: "main.tscn",
		BoolValue: &boolVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty with empty property_name, want error")
	}
}

// Vector2Value participates in the same "exactly one *_value field" count as
// the primitive fields, and is otherwise a valid lone value — proven here
// without a real Godot binary the same way the primitive rejection tests
// are: newDirectReadTestClient's garbage GodotBin means any error returned
// once validation passes comes from failing to exec Godot, not from the
// values-set check.

func TestSetNodeProperty_Vector2AloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "position",
		Vector2Value: &Vector2{X: 1, Y: 2},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone vector2_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone vector2_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsVector2PlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	strVal := "hello"
	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "position",
		StringValue:  &strVal,
		Vector2Value: &Vector2{X: 1, Y: 2},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with string_value and vector2_value both set, want error")
	}
}

// ColorValue participates in the same "exactly one *_value field" count as
// the other value fields, proven the same way as Vector2AloneIsValid above.

func TestSetNodeProperty_ColorAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "modulate",
		ColorValue:   &Color{R: 1, G: 0.5, B: 0.25, A: 1},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone color_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone color_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsColorPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "modulate",
		ColorValue:   &Color{R: 1, G: 0.5, B: 0.25, A: 1},
		Vector2Value: &Vector2{X: 1, Y: 2},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with color_value and vector2_value both set, want error")
	}
}

// Vector3Value participates in the same "exactly one *_value field" count as
// the other value fields, proven the same way as Vector2AloneIsValid above.

func TestSetNodeProperty_Vector3AloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "position",
		Vector3Value: &Vector3{X: 1, Y: 2, Z: 3},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone vector3_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone vector3_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsVector3PlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "position",
		Vector3Value: &Vector3{X: 1, Y: 2, Z: 3},
		Vector2Value: &Vector2{X: 1, Y: 2},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with vector3_value and vector2_value both set, want error")
	}
}

// Vector2iValue and Vector3iValue participate in the same "exactly one
// *_value field" count as the other value fields, proven the same way as
// Vector2AloneIsValid above.

func TestSetNodeProperty_Vector2iAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:     "main.tscn",
		PropertyName:  "frame_coords",
		Vector2iValue: &Vector2i{X: 1, Y: 2},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone vector2i_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone vector2i_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsVector2iPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:     "main.tscn",
		PropertyName:  "frame_coords",
		Vector2iValue: &Vector2i{X: 1, Y: 2},
		Vector2Value:  &Vector2{X: 1, Y: 2},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with vector2i_value and vector2_value both set, want error")
	}
}

func TestSetNodeProperty_Vector3iAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:     "main.tscn",
		PropertyName:  "grid_position",
		Vector3iValue: &Vector3i{X: 1, Y: 2, Z: 3},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone vector3i_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone vector3i_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsVector3iPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:     "main.tscn",
		PropertyName:  "grid_position",
		Vector3iValue: &Vector3i{X: 1, Y: 2, Z: 3},
		Vector3Value:  &Vector3{X: 1, Y: 2, Z: 3},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with vector3i_value and vector3_value both set, want error")
	}
}

// QuaternionValue participates in the same "exactly one *_value field"
// count as the other value fields, proven the same way as
// Vector2AloneIsValid above.

func TestSetNodeProperty_QuaternionAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:       "main.tscn",
		PropertyName:    "quaternion",
		QuaternionValue: &Quaternion{X: 0, Y: 0, Z: 0, W: 1},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone quaternion_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone quaternion_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsQuaternionPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:       "main.tscn",
		PropertyName:    "quaternion",
		QuaternionValue: &Quaternion{X: 0, Y: 0, Z: 0, W: 1},
		Vector3Value:    &Vector3{X: 1, Y: 2, Z: 3},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with quaternion_value and vector3_value both set, want error")
	}
}

// Rect2Value and Rect2iValue participate in the same "exactly one *_value
// field" count as the other value fields, proven the same way as
// Vector2AloneIsValid above.

func TestSetNodeProperty_Rect2AloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "region_rect",
		Rect2Value:   &Rect2{Position: Vector2{X: 1, Y: 2}, Size: Vector2{X: 3, Y: 4}},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone rect2_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone rect2_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsRect2PlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "region_rect",
		Rect2Value:   &Rect2{Position: Vector2{X: 1, Y: 2}, Size: Vector2{X: 3, Y: 4}},
		Vector2Value: &Vector2{X: 1, Y: 2},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with rect2_value and vector2_value both set, want error")
	}
}

func TestSetNodeProperty_Rect2iAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "nonclient_area",
		Rect2iValue:  &Rect2i{Position: Vector2i{X: 1, Y: 2}, Size: Vector2i{X: 3, Y: 4}},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone rect2i_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone rect2i_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsRect2iPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:     "main.tscn",
		PropertyName:  "nonclient_area",
		Rect2iValue:   &Rect2i{Position: Vector2i{X: 1, Y: 2}, Size: Vector2i{X: 3, Y: 4}},
		Vector2iValue: &Vector2i{X: 1, Y: 2},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with rect2i_value and vector2i_value both set, want error")
	}
}

// PlaneValue participates in the same "exactly one *_value field" count as
// the other value fields, proven the same way as Vector2AloneIsValid above.

func TestSetNodeProperty_PlaneAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "boundary_plane",
		PlaneValue:   &Plane{X: 0, Y: 1, Z: 0, D: 0},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone plane_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone plane_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsPlanePlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "boundary_plane",
		PlaneValue:   &Plane{X: 0, Y: 1, Z: 0, D: 0},
		Vector3Value: &Vector3{X: 1, Y: 2, Z: 3},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with plane_value and vector3_value both set, want error")
	}
}

// AABBValue participates in the same "exactly one *_value field" count as
// the other value fields, proven the same way as Vector2AloneIsValid above.

func TestSetNodeProperty_AABBAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "aabb",
		AABBValue:    &AABB{Position: Vector3{X: 1, Y: 2, Z: 3}, Size: Vector3{X: 4, Y: 5, Z: 6}},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone aabb_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone aabb_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsAABBPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "aabb",
		AABBValue:    &AABB{Position: Vector3{X: 1, Y: 2, Z: 3}, Size: Vector3{X: 4, Y: 5, Z: 6}},
		Vector3Value: &Vector3{X: 1, Y: 2, Z: 3},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with aabb_value and vector3_value both set, want error")
	}
}

// BasisValue participates in the same "exactly one *_value field" count as
// the other value fields, proven the same way as Vector2AloneIsValid above.

func TestSetNodeProperty_BasisAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "basis",
		BasisValue: &Basis{
			X: Vector3{X: 1, Y: 0, Z: 0},
			Y: Vector3{X: 0, Y: 1, Z: 0},
			Z: Vector3{X: 0, Y: 0, Z: 1},
		},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone basis_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone basis_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsBasisPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "basis",
		BasisValue: &Basis{
			X: Vector3{X: 1, Y: 0, Z: 0},
			Y: Vector3{X: 0, Y: 1, Z: 0},
			Z: Vector3{X: 0, Y: 0, Z: 1},
		},
		Vector3Value: &Vector3{X: 1, Y: 2, Z: 3},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with basis_value and vector3_value both set, want error")
	}
}

// Transform2DValue and Transform3DValue participate in the same "exactly
// one *_value field" count as the other value fields, proven the same way
// as Vector2AloneIsValid above.

func TestSetNodeProperty_Transform2DAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "transform",
		Transform2DValue: &Transform2D{
			X:      Vector2{X: 1, Y: 0},
			Y:      Vector2{X: 0, Y: 1},
			Origin: Vector2{X: 10, Y: 20},
		},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone transform2d_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone transform2d_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsTransform2DPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "transform",
		Transform2DValue: &Transform2D{
			X:      Vector2{X: 1, Y: 0},
			Y:      Vector2{X: 0, Y: 1},
			Origin: Vector2{X: 10, Y: 20},
		},
		Vector2Value: &Vector2{X: 1, Y: 2},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with transform2d_value and vector2_value both set, want error")
	}
}

func TestSetNodeProperty_Transform3DAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "transform",
		Transform3DValue: &Transform3D{
			Basis: Basis{
				X: Vector3{X: 1, Y: 0, Z: 0},
				Y: Vector3{X: 0, Y: 1, Z: 0},
				Z: Vector3{X: 0, Y: 0, Z: 1},
			},
			Origin: Vector3{X: 10, Y: 20, Z: 30},
		},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone transform3d_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone transform3d_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsTransform3DPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:    "main.tscn",
		PropertyName: "transform",
		Transform3DValue: &Transform3D{
			Basis: Basis{
				X: Vector3{X: 1, Y: 0, Z: 0},
				Y: Vector3{X: 0, Y: 1, Z: 0},
				Z: Vector3{X: 0, Y: 0, Z: 1},
			},
			Origin: Vector3{X: 10, Y: 20, Z: 30},
		},
		Vector3Value: &Vector3{X: 1, Y: 2, Z: 3},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with transform3d_value and vector3_value both set, want error")
	}
}

// NodePathValue participates in the same "exactly one *_value field" count
// as the other value fields, proven the same way as Vector2AloneIsValid
// above.

func TestSetNodeProperty_NodePathAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	nodePathVal := "../Target"
	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:     "main.tscn",
		PropertyName:  "remote_path",
		NodePathValue: &nodePathVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone node_path_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone node_path_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_RejectsNodePathPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	nodePathVal := "../Target"
	strVal := "hello"
	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:     "main.tscn",
		PropertyName:  "remote_path",
		NodePathValue: &nodePathVal,
		StringValue:   &strVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty with node_path_value and string_value both set, want error")
	}
}

// StringArrayValue participates in the same "exactly one *_value field"
// count as the other value fields, proven the same way as
// Vector2AloneIsValid above. It also needs its own nil-vs-empty-slice case:
// unlike a pointer field, a slice field's "was this provided at all" signal
// is nil vs non-nil, not zero-value vs non-zero-value — an explicitly empty
// array ([]string{}, decoded from a JSON "[]") is a legitimate value (an
// empty PackedStringArray), not "not set".

func TestSetNodeProperty_StringArrayAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:        "main.tscn",
		PropertyName:     "_spawnable_scenes",
		StringArrayValue: []string{"res://a.tscn", "res://b.tscn"},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone string_array_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone string_array_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_EmptyStringArrayIsStillValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:        "main.tscn",
		PropertyName:     "_spawnable_scenes",
		StringArrayValue: []string{},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with an explicitly empty string_array_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected an explicitly empty string_array_value as if it were unset: %v", err)
	}
}

func TestSetNodeProperty_RejectsStringArrayPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	strVal := "hello"
	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:        "main.tscn",
		PropertyName:     "_spawnable_scenes",
		StringArrayValue: []string{"res://a.tscn"},
		StringValue:      &strVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty with string_array_value and string_value both set, want error")
	}
}

// IntArrayValue has the same nil-vs-empty-slice nuance as StringArrayValue
// above (see SetNodePropertyParams.IntArrayValue's doc comment), and
// participates in the same "exactly one *_value field" count as every
// other value field.

func TestSetNodeProperty_IntArrayAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:     "main.tscn",
		PropertyName:  "split_offsets",
		IntArrayValue: []int64{10, -20, 30},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone int_array_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone int_array_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_EmptyIntArrayIsStillValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:     "main.tscn",
		PropertyName:  "split_offsets",
		IntArrayValue: []int64{},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with an explicitly empty int_array_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected an explicitly empty int_array_value as if it were unset: %v", err)
	}
}

func TestSetNodeProperty_RejectsIntArrayPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	intVal := int64(1)
	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:     "main.tscn",
		PropertyName:  "split_offsets",
		IntArrayValue: []int64{10},
		IntValue:      &intVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty with int_array_value and int_value both set, want error")
	}
}

// FloatArrayValue has the same nil-vs-empty-slice nuance as
// StringArrayValue/IntArrayValue above, and participates in the same
// "exactly one *_value field" count as every other value field.

func TestSetNodeProperty_FloatArrayAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:       "main.tscn",
		PropertyName:    "tab_stops",
		FloatArrayValue: []float64{1.5, 2.5, -3.5},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone float_array_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone float_array_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_EmptyFloatArrayIsStillValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:       "main.tscn",
		PropertyName:    "tab_stops",
		FloatArrayValue: []float64{},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with an explicitly empty float_array_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected an explicitly empty float_array_value as if it were unset: %v", err)
	}
}

func TestSetNodeProperty_RejectsFloatArrayPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	floatVal := 1.5
	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:       "main.tscn",
		PropertyName:    "tab_stops",
		FloatArrayValue: []float64{1.5},
		FloatValue:      &floatVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty with float_array_value and float_value both set, want error")
	}
}

// Vector2ArrayValue has the same nil-vs-empty-slice nuance as the other
// packed array fields above, and participates in the same "exactly one
// *_value field" count as every other value field.

func TestSetNodeProperty_Vector2ArrayAloneIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:         "main.tscn",
		PropertyName:      "polygon",
		Vector2ArrayValue: []Vector2{{X: 1.5, Y: 2.5}, {X: -3, Y: 4}},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a lone vector2_array_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected a lone vector2_array_value as if zero/multiple values were set: %v", err)
	}
}

func TestSetNodeProperty_EmptyVector2ArrayIsStillValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:         "main.tscn",
		PropertyName:      "polygon",
		Vector2ArrayValue: []Vector2{},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with an explicitly empty vector2_array_value and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("SetNodeProperty rejected an explicitly empty vector2_array_value as if it were unset: %v", err)
	}
}

func TestSetNodeProperty_RejectsVector2ArrayPlusOtherValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tscn"), []byte("[gd_scene format=3]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	_, err := c.SetNodeProperty(context.Background(), SetNodePropertyParams{
		ScenePath:         "main.tscn",
		PropertyName:      "polygon",
		Vector2ArrayValue: []Vector2{{X: 1, Y: 2}},
		Vector2Value:      &Vector2{X: 1, Y: 2},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with vector2_array_value and vector2_value both set, want error")
	}
}

func TestReadImportSettings_MissingImportFile(t *testing.T) {
	dir := t.TempDir()
	// The asset exists but was never imported (or isn't an importable
	// type), so there's no <path>.import sidecar.
	if err := os.WriteFile(filepath.Join(dir, "icon.png"), []byte("not imported"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c := newDirectReadTestClient(t, dir)

	_, err := c.ReadImportSettings(context.Background(), ReadImportSettingsParams{AssetPath: "icon.png"})
	if err == nil {
		t.Fatal("ReadImportSettings with no .import sidecar, want error")
	}
}
