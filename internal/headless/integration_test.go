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

// writeSetNodePropertyFixtureScene generates a small, throwaway main.tscn in
// projectDir via a real Godot invocation — deliberately not
// testdata/fixture_project/main.tscn, since that fixture is shared with the
// read-only scene-tree tests and must stay pristine (SetNodeProperty writes
// the scene file it's given). It's generated rather than hand-authored the
// way the other fixtures in this file are: attaching a script to a node via
// hand-written `.tscn` text (a `script=ExtResource("1")` node attribute) is
// easy to get subtly wrong — an earlier version of this fixture parsed
// without any error but silently produced a node with no script attached at
// all (target.get_script() came back null), which only surfaced once
// Vector3i tried to use the property that script was supposed to export.
// Building the tree with real Node objects and Node.set_script(), then
// PackedScene.pack() + ResourceSaver.save() — the same escape hatch already
// used for the .res fixture in generateResFixture — sidesteps that whole
// class of mistake by going through Godot's own node construction and
// script-attachment API instead of hand-written scene-file syntax.
//
// "Main" (a Node2D, which carries a real float property in rotation, a real
// int property in z_index, a real bool property in visible, a real Vector2
// property in position, and a real Color property in modulate, inherited
// from CanvasItem) has four children: "Label" (a Label, which carries a
// real String property in text), "Cube" (a Node3D, nested here purely to
// have a real Vector3 property in position to exercise — Godot allows a
// Node3D under a Node2D structurally even though the transforms are
// unrelated), "Sprite" (a Sprite2D with hframes/vframes both set to 4,
// giving frame_coords — a synthetic Vector2i accessor over the single
// stored "frame" int, see TestSetNodeProperty_RealGodot_Vector2i's doc
// comment — headroom for a non-trivial value; Sprite2D's own setter rejects
// any coordinate outside the configured frame grid), and "IntGrid" (a plain
// Node with the
// fixture's own vector3i_holder.gd script attached, exporting
// grid_position) — between them, every value type SetNodeProperty supports
// has a genuine target property to exercise.
//
// Vector3i is the one type with no built-in Node target at all: no built-in
// Node class exposes a Vector3i property (verified against a real build via
// ClassDB introspection — the only Vector3i property anywhere in the engine
// is a Resource property, PlaceholderTexture3D.size, which this tool can't
// address either way, since it only ever targets Node properties). Testing
// it against "IntGrid"'s custom-script property instead is also a more
// realistic stand-in for how this tool actually gets used: against a
// project's own custom node scripts, not just built-in engine properties.
func writeSetNodePropertyFixtureScene(t *testing.T, godotBin, projectDir string) {
	t.Helper()
	const script = `extends Node

@export var grid_position: Vector3i = Vector3i.ZERO
`
	if err := os.WriteFile(filepath.Join(projectDir, "vector3i_holder.gd"), []byte(script), 0o644); err != nil {
		t.Fatalf("writing fixture script: %v", err)
	}

	const setupScript = `extends SceneTree

func _init() -> void:
	var main := Node2D.new()
	main.name = "Main"

	var label := Label.new()
	label.name = "Label"
	main.add_child(label)
	label.owner = main

	var cube := Node3D.new()
	cube.name = "Cube"
	main.add_child(cube)
	cube.owner = main

	var sprite := Sprite2D.new()
	sprite.name = "Sprite"
	sprite.hframes = 4
	sprite.vframes = 4
	main.add_child(sprite)
	sprite.owner = main

	var int_grid := Node.new()
	int_grid.name = "IntGrid"
	int_grid.set_script(load("res://vector3i_holder.gd"))
	main.add_child(int_grid)
	int_grid.owner = main

	var packed := PackedScene.new()
	var pack_err := packed.pack(main)
	if pack_err != OK:
		push_error("failed to pack fixture scene: %d" % pack_err)
	var save_err := ResourceSaver.save(packed, "res://main.tscn")
	if save_err != OK:
		push_error("failed to save fixture scene: %d" % save_err)
	quit()
`
	scriptPath := filepath.Join(projectDir, "generate_set_node_property_fixture.gd")
	if err := os.WriteFile(scriptPath, []byte(setupScript), 0o644); err != nil {
		t.Fatalf("writing fixture-generation script: %v", err)
	}

	// #nosec G204 -- test-only, fixed argv, godotBin/projectDir/scriptPath
	// are all test-controlled, not external input.
	cmd := exec.Command(godotBin, "--headless", "--path", projectDir, "--script", scriptPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating set_node_property fixture scene: %v\n%s", err, out)
	}
}

func setNodePropertyFixtureClient(t *testing.T) *Client {
	t.Helper()
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeSetNodePropertyFixtureScene(t, godotBin, projectDir)

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

// TestSetNodeProperty_RealGodot_Vector3 targets Node3D's "position", which
// unlike Node2D's "position" is not itself a stored property: Node3D only
// exports "transform" (a Basis plus an origin), and "position" is a
// synthetic accessor that reads/writes transform's origin. Godot's own
// verify-before-save read-back (target.get("position") after the set)
// confirms the write took effect the same way it would for any other
// property, but the .tscn line that actually changes on disk is
// "transform = Transform3D(...)", with the requested Vector3 as the last
// three (origin) components — not a "position = Vector3(...)" line.
func TestSetNodeProperty_RealGodot_Vector3(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Cube",
		PropertyName: "position",
		Vector3Value: &Vector3{X: 1.5, Y: -2.5, Z: 3.5},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "transform = Transform3D(1, 0, 0, 0, 1, 0, 0, 0, 1, 1.5, -2.5, 3.5)") {
		t.Errorf("saved scene missing expected transform with position as its origin: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_Vector2i targets Sprite2D's "frame_coords",
// which — like Node3D's "position" (see TestSetNodeProperty_RealGodot_Vector3
// above) — is not itself a stored property: Sprite2D only exports "frame"
// (a single flat int index into the hframes x vframes grid), and
// frame_coords is a synthetic accessor computed from it
// (frame = y*hframes + x). The verify-before-save read-back still confirms
// the write took effect, but the .tscn line that actually changes on disk
// is "frame = 14" (3*4 + 2, for the fixture's 4x4 grid), not
// "frame_coords = Vector2i(2, 3)".
func TestSetNodeProperty_RealGodot_Vector2i(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "Sprite",
		PropertyName:  "frame_coords",
		Vector2iValue: &Vector2i{X: 2, Y: 3},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "frame = 14") {
		t.Errorf("saved scene missing expected frame (frame_coords' backing property): %s", data)
	}
}

func TestSetNodeProperty_RealGodot_Vector3i(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "IntGrid",
		PropertyName:  "grid_position",
		Vector3iValue: &Vector3i{X: 2, Y: 3, Z: 4},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "grid_position = Vector3i(2, 3, 4)") {
		t.Errorf("saved scene missing expected grid_position property: %s", data)
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

// TestSetNodeProperty_RealGodot_UnknownProperty_NonRootTarget is a
// regression test: the operations script used to read target.get_class()
// for the error message *after* calling root.free(), and freeing root also
// frees every descendant. When node_path == "" (as in
// TestSetNodeProperty_RealGodot_UnknownProperty above), target is root
// itself, and that always happened to survive long enough for the format
// call — the bug only showed up once a real Vector2i test ended up hitting
// this same mismatch path against a non-root target (Sprite2D.frame_coords
// rejected by the engine's own hframes/vframes bounds check), where
// target.get_class() crashed with "Cannot call method 'get_class' on a
// previously freed instance." Targeting "Label" here (a child of the scene
// root) with an unknown property name exercises that non-root path
// directly.
func TestSetNodeProperty_RealGodot_UnknownProperty_NonRootTarget(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	strVal := "should not be written"
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Label",
		PropertyName: "this_property_does_not_exist",
		StringValue:  &strVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty on an unknown property name against a non-root target, want error")
	}
	if !strings.Contains(err.Error(), "Label") {
		t.Errorf("error message should name the target node's class (Label), got: %v", err)
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
