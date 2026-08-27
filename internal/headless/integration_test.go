package headless

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
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

// writeMinimalPNG writes a valid 1x1 PNG — a real, importable asset type —
// using only the standard library, so this test doesn't depend on any
// external image tooling being available.
func writeMinimalPNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating png: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encoding png: %v", err)
	}
}

// writeSetNodePropertyFixtureScene writes a small, throwaway .tscn to
// projectDir — deliberately not testdata/fixture_project/main.tscn, since
// that fixture is shared with the read-only scene-tree tests and must stay
// pristine (SetNodeProperty writes the scene file it's given). "Main" (a
// Node2D, which carries a real float property in rotation, a real int
// property in z_index, a real bool property in visible, a real Vector2
// property in position, and a real Color property in modulate, inherited
// from CanvasItem) has one child, "Label" (a Label, which carries a real
// String property in text) — between them, every value type
// SetNodeProperty supports has a genuine target property to exercise.
func writeSetNodePropertyFixtureScene(t *testing.T, projectDir string) {
	t.Helper()
	const scene = `[gd_scene load_steps=1 format=3]

[node name="Main" type="Node2D"]

[node name="Label" type="Label" parent="."]
`
	if err := os.WriteFile(filepath.Join(projectDir, "main.tscn"), []byte(scene), 0o644); err != nil {
		t.Fatalf("writing fixture scene: %v", err)
	}
}

func setNodePropertyFixtureClient(t *testing.T) *Client {
	t.Helper()
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeSetNodePropertyFixtureScene(t, projectDir)

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return &Client{GodotBin: godotBin, OperationsScript: operationsScriptPath(t), Root: root}
}

func TestSetNodeProperty_RealGodot_String(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	strVal := "hello from a test"
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Label",
		PropertyName: "text",
		StringValue:  &strVal,
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	// .tscn is plain text: read the saved scene back directly (not via
	// ReadSceneTree, which doesn't expose properties) to confirm the write
	// actually landed on disk in Godot's own serialization.
	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), `text = "hello from a test"`) {
		t.Errorf("saved scene missing expected text property: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_Int(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	intVal := int64(7)
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "",
		PropertyName: "z_index",
		IntValue:     &intVal,
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "z_index = 7") {
		t.Errorf("saved scene missing expected z_index property: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_Float(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	floatVal := 1.5
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "",
		PropertyName: "rotation",
		FloatValue:   &floatVal,
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "rotation = 1.5") {
		t.Errorf("saved scene missing expected rotation property: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_Bool(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	boolVal := false
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "",
		PropertyName: "visible",
		BoolValue:    &boolVal,
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "visible = false") {
		t.Errorf("saved scene missing expected visible property: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_Vector2(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "",
		PropertyName: "position",
		Vector2Value: &Vector2{X: 1.5, Y: -2.5},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "position = Vector2(1.5, -2.5)") {
		t.Errorf("saved scene missing expected position property: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_Color(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "",
		PropertyName: "modulate",
		ColorValue:   &Color{R: 0.5, G: 0.25, B: 0.75, A: 1},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "modulate = Color(0.5, 0.25, 0.75, 1)") {
		t.Errorf("saved scene missing expected modulate property: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_UnknownProperty(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	strVal := "should not be written"
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "",
		PropertyName: "this_property_does_not_exist",
		StringValue:  &strVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty on an unknown property name, want error")
	}

	// The scene must be left untouched: a mistyped property name is a
	// no-op in Godot's own Object.set(), not an error, so the operation
	// must detect that and refuse to save rather than silently writing
	// nothing useful.
	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading scene: %v", err)
	}
	if strings.Contains(string(data), "this_property_does_not_exist") {
		t.Errorf("scene was modified despite the error: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_MissingNode(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	boolVal := true
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "DoesNotExist",
		PropertyName: "visible",
		BoolValue:    &boolVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty against a missing node, want error")
	}
}

func TestSetNodeProperty_RealGodot_MissingScene(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	boolVal := true
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "does_not_exist.tscn",
		PropertyName: "visible",
		BoolValue:    &boolVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty against a missing scene, want error")
	}
}

func TestReadImportSettings_RealGodot(t *testing.T) {
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeMinimalPNG(t, filepath.Join(projectDir, "icon.png"))

	// ReadImportSettings never invokes Godot itself (it's a plain file
	// read, like ReadProjectSettings) — but the .import sidecar it reads
	// only exists because Godot's own import pipeline generated it, which
	// is what this triggers: a brief headless editor run, exactly what a
	// real project accumulates from having been opened once. This is
	// test-only setup, unrelated to scripts/godot_operations.gd.
	// #nosec G204 -- test-only, fixed argv, all test-controlled.
	cmd := exec.Command(godotBin, "--headless", "--editor", "--quit-after", "20", "--path", projectDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("triggering asset import: %v\n%s", err, out)
	}

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	c := &Client{GodotBin: godotBin, OperationsScript: operationsScriptPath(t), Root: root}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got, err := c.ReadImportSettings(ctx, ReadImportSettingsParams{AssetPath: "icon.png"})
	if err != nil {
		t.Fatalf("ReadImportSettings against a real Godot-generated .import file: %v", err)
	}
	if got.Path != "res://icon.png.import" {
		t.Errorf("Path = %q, want %q", got.Path, "res://icon.png.import")
	}
	if !strings.Contains(got.Source, "[remap]") {
		t.Errorf("Source missing expected [remap] section: %s", got.Source)
	}
	if !strings.Contains(got.Source, `importer="texture"`) {
		t.Errorf("Source missing expected importer field: %s", got.Source)
	}
}
