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
// any coordinate outside the configured frame grid), "IntGrid" (a plain
// Node with the fixture's own custom_props_holder.gd script attached), and
// "Remote" (a RemoteTransform2D, which carries a real NodePath property in
// remote_path), "Spawner" (a MultiplayerSpawner, which carries a real
// PackedStringArray property in _spawnable_scenes — underscore-prefixed,
// but genuinely a public ClassDB property, not a private implementation
// detail), "Split" (a SplitContainer, which carries a real
// PackedInt32Array property in split_offsets), "Poly" (a
// CollisionPolygon2D, which carries a real PackedVector2Array property in
// polygon), and "Ctrl" (a Control, which carries a real typed
// Array[NodePath] property in accessibility_flow_to_nodes — a different
// Variant mechanism from the Packed*Array family, see NodePathArrayValue's
// doc comment) — between them, every value type SetNodeProperty supports
// has a genuine target property to exercise.
//
// Vector3i and Plane are two of the types with no built-in Node target at
// all: no built-in Node class exposes either property type (verified
// against a real build via ClassDB introspection — the only property of
// either type anywhere in the engine is on a Resource, out of this tool's
// reach either way, since it only ever targets Node properties). Testing
// them against custom_props_holder.gd's exported properties (grid_position,
// a Vector3i; boundary_plane, a Plane) instead is also a more realistic
// stand-in for how this tool actually gets used: against a project's own
// custom node scripts, not just built-in engine properties.
//
// The same is true, and was reverified the same way, for every
// Typed*ArrayValue element type (Array[String]/Array[int]/Array[float]/
// Array[Vector2]/Array[Color]/Array[Vector3]): no built-in Node class
// exposes a typed-Array[T] property of any of these element types (only
// Array[NodePath], on Control.accessibility_flow_to_nodes, has a real
// built-in target). custom_props_holder.gd's typed_strings/typed_ints/
// typed_floats/typed_vector2s/typed_colors/typed_vector3s properties are
// the real-Godot test target for all six.
//
// typed_textures (Array[Texture2D]) is the same story for
// TypedResourceArrayValue: no built-in Node class exposes a typed array of
// any Resource subclass either (also reverified via ClassDB). It's also the
// property that established _op_set_node_property's expected-class lookup
// for a Resource-element typed array: a script-exported Array[Texture2D]
// reports hint=PROPERTY_HINT_TYPE_STRING with hint_string="24/17:Texture2D"
// (the compound TYPE/HINT:ClassName encoding, not the plain class name
// Array[NodePath] uses — that plain form only happens for a builtin-typed
// element), so the class name has to be parsed out of the part after the
// ":".
func writeSetNodePropertyFixtureScene(t *testing.T, godotBin, projectDir string) {
	t.Helper()
	const script = `extends Node

@export var grid_position: Vector3i = Vector3i.ZERO
@export var boundary_plane: Plane = Plane(0, 1, 0, 0)
@export var typed_strings: Array[String] = []
@export var typed_ints: Array[int] = []
@export var typed_floats: Array[float] = []
@export var typed_vector2s: Array[Vector2] = []
@export var typed_colors: Array[Color] = []
@export var typed_vector3s: Array[Vector3] = []
@export var typed_textures: Array[Texture2D] = []
`
	if err := os.WriteFile(filepath.Join(projectDir, "custom_props_holder.gd"), []byte(script), 0o644); err != nil {
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
	int_grid.set_script(load("res://custom_props_holder.gd"))
	main.add_child(int_grid)
	int_grid.owner = main

	var remote := RemoteTransform2D.new()
	remote.name = "Remote"
	main.add_child(remote)
	remote.owner = main

	var spawner := MultiplayerSpawner.new()
	spawner.name = "Spawner"
	main.add_child(spawner)
	spawner.owner = main

	var split := SplitContainer.new()
	split.name = "Split"
	main.add_child(split)
	split.owner = main

	var poly := CollisionPolygon2D.new()
	poly.name = "Poly"
	main.add_child(poly)
	poly.owner = main

	var ctrl := Control.new()
	ctrl.name = "Ctrl"
	main.add_child(ctrl)
	ctrl.owner = main

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

// TestSetNodeProperty_RealGodot_Quaternion targets Node3D's "quaternion",
// which — like "position" (see TestSetNodeProperty_RealGodot_Vector3 above)
// — is a synthetic accessor onto the node's stored "transform", this time
// converting the quaternion into a rotation Basis rather than an origin.
// The value used here is a 90-degree rotation around Y
// (x=0, y=sin(45deg), z=0, w=cos(45deg)); the exact saved Basis values were
// captured from a real Godot run rather than hand-derived, the same way
// TestSetNodeProperty_RealGodot_Vector3's Transform3D assertion was.
func TestSetNodeProperty_RealGodot_Quaternion(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:       "main.tscn",
		NodePath:        "Cube",
		PropertyName:    "quaternion",
		QuaternionValue: &Quaternion{X: 0, Y: 0.7071067811865476, Z: 0, W: 0.7071067811865476},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "transform = Transform3D(0, 0, 1, 0, 1, 0, -1, 0, 0, 0, 0, 0)") {
		t.Errorf("saved scene missing expected transform with the rotation basis: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_Basis targets Node3D's "basis", which — like
// "position" and "quaternion" above — is a synthetic accessor onto the
// node's stored "transform": basis supplies its rotation/scale part while
// leaving the origin untouched (zero here, since the fixture's "Cube" is
// otherwise at its default transform).
func TestSetNodeProperty_RealGodot_Basis(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Cube",
		PropertyName: "basis",
		BasisValue: &Basis{
			X: Vector3{X: 2, Y: 0, Z: 0},
			Y: Vector3{X: 0, Y: 3, Z: 0},
			Z: Vector3{X: 0, Y: 0, Z: 4},
		},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "transform = Transform3D(2, 0, 0, 0, 3, 0, 0, 0, 4, 0, 0, 0)") {
		t.Errorf("saved scene missing expected transform with the requested basis: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_Transform3D targets Node3D's "transform"
// directly. Unlike every other Node3D property this package tests
// (position, quaternion, basis — all synthetic accessors onto transform,
// see their own doc comments), transform is the actual stored property:
// the saved .tscn line matches the request directly, the same way Rect2's
// region_rect does.
func TestSetNodeProperty_RealGodot_Transform3D(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Cube",
		PropertyName: "transform",
		Transform3DValue: &Transform3D{
			Basis: Basis{
				X: Vector3{X: 2, Y: 0, Z: 0},
				Y: Vector3{X: 0, Y: 3, Z: 0},
				Z: Vector3{X: 0, Y: 0, Z: 4},
			},
			Origin: Vector3{X: 10, Y: 20, Z: 30},
		},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "transform = Transform3D(2, 0, 0, 0, 3, 0, 0, 0, 4, 10, 20, 30)") {
		t.Errorf("saved scene missing expected transform property: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_Transform2D targets Node2D's "transform" —
// the reverse situation from Node3D's transform: here, transform ITSELF is
// the synthetic accessor. Node2D only stores position/rotation/scale/skew
// individually; setting transform decomposes the request into those, so a
// pure-rotation-plus-origin transform (no scale or skew) round-trips
// through the verify-before-save check correctly but shows up on disk as
// "position = ..." and "rotation = ..." lines, not a
// "transform = Transform2D(...)" line.
func TestSetNodeProperty_RealGodot_Transform2D(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "",
		PropertyName: "transform",
		Transform2DValue: &Transform2D{
			X:      Vector2{X: 0.8660254037844387, Y: 0.49999999999999994},
			Y:      Vector2{X: -0.49999999999999994, Y: 0.8660254037844387},
			Origin: Vector2{X: 10, Y: 20},
		},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "position = Vector2(10, 20)") || !strings.Contains(string(data), "rotation = 0.5235988") {
		t.Errorf("saved scene missing expected decomposed position/rotation properties: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_NodePath(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nodePathVal := "../Target"
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "Remote",
		PropertyName:  "remote_path",
		NodePathValue: &nodePathVal,
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), `remote_path = NodePath("../Target")`) {
		t.Errorf("saved scene missing expected remote_path property: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_StringArray(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:        "main.tscn",
		NodePath:         "Spawner",
		PropertyName:     "_spawnable_scenes",
		StringArrayValue: []string{"res://a.tscn", "res://b.tscn"},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), `_spawnable_scenes = PackedStringArray("res://a.tscn", "res://b.tscn")`) {
		t.Errorf("saved scene missing expected _spawnable_scenes property: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_EmptyStringArray proves an explicitly empty
// StringArrayValue ([]string{}, as opposed to nil) round-trips end to end as
// "clear this array" rather than being silently dropped from the request —
// the exact failure mode SetNodePropertyParams.StringArrayValue's doc
// comment describes for why it uses "omitzero" instead of "omitempty".
// Godot itself omits a property from a saved .tscn once it matches its
// default value (an empty array is PackedStringArray's zero value), so the
// property is set to something non-empty first, then cleared, and the
// assertion is on the property's absence, not on a literal empty-array
// line.
func TestSetNodeProperty_RealGodot_EmptyStringArray(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:        "main.tscn",
		NodePath:         "Spawner",
		PropertyName:     "_spawnable_scenes",
		StringArrayValue: []string{"res://a.tscn"},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (initial non-empty set) against a real Godot binary: %v", err)
	}

	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:        "main.tscn",
		NodePath:         "Spawner",
		PropertyName:     "_spawnable_scenes",
		StringArrayValue: []string{},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (clearing to an empty array) against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if strings.Contains(string(data), "_spawnable_scenes") {
		t.Errorf("saved scene still has _spawnable_scenes after clearing it to an empty array: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_IntArray(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "Split",
		PropertyName:  "split_offsets",
		IntArrayValue: []int64{10, -20, 30},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "split_offsets = PackedInt32Array(10, -20, 30)") {
		t.Errorf("saved scene missing expected split_offsets property: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_EmptyIntArray mirrors
// TestSetNodeProperty_RealGodot_EmptyStringArray above: an explicitly empty
// IntArrayValue must round-trip as "clear this array", not be dropped from
// the request the way "omitempty" (rather than "omitzero") would have
// dropped it — see IntArrayValue's doc comment. Unlike
// MultiplayerSpawner's _spawnable_scenes, though, an empty split_offsets
// isn't omitted from the saved scene as matching some default — Godot still
// writes it explicitly as "PackedInt32Array()", so the assertion here is on
// that explicit empty form, not on the property's absence.
func TestSetNodeProperty_RealGodot_EmptyIntArray(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "Split",
		PropertyName:  "split_offsets",
		IntArrayValue: []int64{10},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (initial non-empty set) against a real Godot binary: %v", err)
	}

	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "Split",
		PropertyName:  "split_offsets",
		IntArrayValue: []int64{},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (clearing to an empty array) against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "split_offsets = PackedInt32Array()") {
		t.Errorf("saved scene missing expected empty split_offsets property: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_FloatArray(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:       "main.tscn",
		NodePath:        "Label",
		PropertyName:    "tab_stops",
		FloatArrayValue: []float64{1.5, 2.5, -3.5},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "tab_stops = PackedFloat32Array(1.5, 2.5, -3.5)") {
		t.Errorf("saved scene missing expected tab_stops property: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_EmptyFloatArray mirrors
// TestSetNodeProperty_RealGodot_EmptyStringArray above (not
// TestSetNodeProperty_RealGodot_EmptyIntArray's explicit-empty-array
// variant): Label.tab_stops, like MultiplayerSpawner's _spawnable_scenes,
// is simply omitted from the saved scene once it's cleared back to empty,
// rather than staying as an explicit "PackedFloat32Array()" the way
// SplitContainer.split_offsets does — verified empirically, not assumed
// from the PackedInt32Array precedent.
func TestSetNodeProperty_RealGodot_EmptyFloatArray(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:       "main.tscn",
		NodePath:        "Label",
		PropertyName:    "tab_stops",
		FloatArrayValue: []float64{1.5},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (initial non-empty set) against a real Godot binary: %v", err)
	}

	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:       "main.tscn",
		NodePath:        "Label",
		PropertyName:    "tab_stops",
		FloatArrayValue: []float64{},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (clearing to an empty array) against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if strings.Contains(string(data), "tab_stops") {
		t.Errorf("saved scene still has tab_stops after clearing it to an empty array: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_Vector2Array(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Poly",
		PropertyName: "polygon",
		Vector2ArrayValue: []Vector2{
			{X: 1.5, Y: 2.5},
			{X: -3, Y: 4},
		},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "polygon = PackedVector2Array(1.5, 2.5, -3, 4)") {
		t.Errorf("saved scene missing expected polygon property: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_EmptyVector2Array mirrors
// TestSetNodeProperty_RealGodot_EmptyStringArray/EmptyFloatArray above:
// CollisionPolygon2D.polygon is omitted from the saved scene once cleared
// back to empty, rather than staying as an explicit
// "PackedVector2Array()" the way SplitContainer.split_offsets does —
// verified empirically.
func TestSetNodeProperty_RealGodot_EmptyVector2Array(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:         "main.tscn",
		NodePath:          "Poly",
		PropertyName:      "polygon",
		Vector2ArrayValue: []Vector2{{X: 1, Y: 2}},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (initial non-empty set) against a real Godot binary: %v", err)
	}

	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:         "main.tscn",
		NodePath:          "Poly",
		PropertyName:      "polygon",
		Vector2ArrayValue: []Vector2{},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (clearing to an empty array) against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if strings.Contains(string(data), "polygon") {
		t.Errorf("saved scene still has polygon after clearing it to an empty array: %s", data)
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

func TestSetNodeProperty_RealGodot_Plane(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "IntGrid",
		PropertyName: "boundary_plane",
		PlaneValue:   &Plane{X: 0, Y: 0, Z: 1, D: 5},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "boundary_plane = Plane(0, 0, 1, 5)") {
		t.Errorf("saved scene missing expected boundary_plane property: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_Rect2 targets Sprite2D's "region_rect",
// which — unlike frame_coords (see TestSetNodeProperty_RealGodot_Vector2i
// above) — is a genuinely stored property, not a synthetic accessor: the
// saved .tscn line matches the request directly.
func TestSetNodeProperty_RealGodot_Rect2(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Sprite",
		PropertyName: "region_rect",
		Rect2Value: &Rect2{
			Position: Vector2{X: 1.5, Y: 2.5},
			Size:     Vector2{X: 10, Y: 20},
		},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "region_rect = Rect2(1.5, 2.5, 10, 20)") {
		t.Errorf("saved scene missing expected region_rect property: %s", data)
	}
}

// rect2iFixtureClient builds a separate, minimal fixture project from
// setNodePropertyFixtureClient's shared one: Rect2i has exactly one
// built-in Node target in the whole engine, Window.nonclient_area (verified
// via the same ClassDB introspection approach used to establish that
// Vector3i has none — see writeSetNodePropertyFixtureScene's doc comment),
// and instantiating a Window node leaks a rendering viewport/RIDs at
// process exit (Godot prints ERROR/WARNING lines about it, though it
// doesn't fail the run). Keeping the Window node confined to its own
// throwaway fixture avoids adding that noise to every other
// SetNodeProperty test, which all share setNodePropertyFixtureClient's
// scene.
func rect2iFixtureClient(t *testing.T) *Client {
	t.Helper()
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	const setupScript = `extends SceneTree

func _init() -> void:
	var main := Node2D.new()
	main.name = "Main"

	var win := Window.new()
	win.name = "Win"
	main.add_child(win)
	win.owner = main

	var packed := PackedScene.new()
	var pack_err := packed.pack(main)
	if pack_err != OK:
		push_error("failed to pack fixture scene: %d" % pack_err)
	var save_err := ResourceSaver.save(packed, "res://main.tscn")
	if save_err != OK:
		push_error("failed to save fixture scene: %d" % save_err)
	quit()
`
	scriptPath := filepath.Join(projectDir, "generate_rect2i_fixture.gd")
	if err := os.WriteFile(scriptPath, []byte(setupScript), 0o644); err != nil {
		t.Fatalf("writing fixture-generation script: %v", err)
	}

	// #nosec G204 -- test-only, fixed argv, godotBin/projectDir/scriptPath
	// are all test-controlled, not external input.
	cmd := exec.Command(godotBin, "--headless", "--path", projectDir, "--script", scriptPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating rect2i fixture scene: %v\n%s", err, out)
	}

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return &Client{GodotBin: godotBin, OperationsScript: operationsScriptPath(t), Root: root}
}

func TestSetNodeProperty_RealGodot_Rect2i(t *testing.T) {
	c := rect2iFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Win",
		PropertyName: "nonclient_area",
		Rect2iValue: &Rect2i{
			Position: Vector2i{X: 1, Y: 2},
			Size:     Vector2i{X: 3, Y: 4},
		},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "nonclient_area = Rect2i(1, 2, 3, 4)") {
		t.Errorf("saved scene missing expected nonclient_area property: %s", data)
	}
}

// aabbFixtureClient builds another separate, minimal fixture project of its
// own, for the same reason rect2iFixtureClient does: the cleanest built-in
// Node target for AABB, VisibleOnScreenNotifier3D.aabb, leaks a rendering
// server RID at process exit (Godot prints an ERROR line about it, though
// it doesn't fail the run) — a plain Node3D like the shared fixture's
// "Cube" doesn't have this problem, but VisibleOnScreenNotifier3D
// specifically does, since it registers real rendering-server instance
// state on construction. Keeping it confined to its own throwaway fixture
// avoids adding that noise to every other SetNodeProperty test.
func aabbFixtureClient(t *testing.T) *Client {
	t.Helper()
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	const setupScript = `extends SceneTree

func _init() -> void:
	var main := Node2D.new()
	main.name = "Main"

	var notifier := VisibleOnScreenNotifier3D.new()
	notifier.name = "Notifier"
	main.add_child(notifier)
	notifier.owner = main

	var packed := PackedScene.new()
	var pack_err := packed.pack(main)
	if pack_err != OK:
		push_error("failed to pack fixture scene: %d" % pack_err)
	var save_err := ResourceSaver.save(packed, "res://main.tscn")
	if save_err != OK:
		push_error("failed to save fixture scene: %d" % save_err)
	quit()
`
	scriptPath := filepath.Join(projectDir, "generate_aabb_fixture.gd")
	if err := os.WriteFile(scriptPath, []byte(setupScript), 0o644); err != nil {
		t.Fatalf("writing fixture-generation script: %v", err)
	}

	// #nosec G204 -- test-only, fixed argv, godotBin/projectDir/scriptPath
	// are all test-controlled, not external input.
	cmd := exec.Command(godotBin, "--headless", "--path", projectDir, "--script", scriptPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating aabb fixture scene: %v\n%s", err, out)
	}

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return &Client{GodotBin: godotBin, OperationsScript: operationsScriptPath(t), Root: root}
}

func TestSetNodeProperty_RealGodot_AABB(t *testing.T) {
	c := aabbFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Notifier",
		PropertyName: "aabb",
		AABBValue: &AABB{
			Position: Vector3{X: 1, Y: 2, Z: 3},
			Size:     Vector3{X: 4, Y: 5, Z: 6},
		},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "aabb = AABB(1, 2, 3, 4, 5, 6)") {
		t.Errorf("saved scene missing expected aabb property: %s", data)
	}
}

// polygon2DFixtureClient builds another separate, minimal fixture project,
// for the same reason rect2iFixtureClient and aabbFixtureClient do:
// Polygon2D — the cleanest built-in Node target for PackedColorArray
// (vertex_colors) — leaks an internal mesh RID at process exit (Godot
// prints an ERROR line about it, though it doesn't fail the run), the same
// category of noise as Window and VisibleOnScreenNotifier3D. Keeping it
// confined to its own throwaway fixture avoids adding that noise to every
// other SetNodeProperty test.
func polygon2DFixtureClient(t *testing.T) *Client {
	t.Helper()
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	const setupScript = `extends SceneTree

func _init() -> void:
	var main := Node2D.new()
	main.name = "Main"

	var poly := Polygon2D.new()
	poly.name = "ColorPoly"
	main.add_child(poly)
	poly.owner = main

	var packed := PackedScene.new()
	var pack_err := packed.pack(main)
	if pack_err != OK:
		push_error("failed to pack fixture scene: %d" % pack_err)
	var save_err := ResourceSaver.save(packed, "res://main.tscn")
	if save_err != OK:
		push_error("failed to save fixture scene: %d" % save_err)
	quit()
`
	scriptPath := filepath.Join(projectDir, "generate_polygon2d_fixture.gd")
	if err := os.WriteFile(scriptPath, []byte(setupScript), 0o644); err != nil {
		t.Fatalf("writing fixture-generation script: %v", err)
	}

	// #nosec G204 -- test-only, fixed argv, godotBin/projectDir/scriptPath
	// are all test-controlled, not external input.
	cmd := exec.Command(godotBin, "--headless", "--path", projectDir, "--script", scriptPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating polygon2d fixture scene: %v\n%s", err, out)
	}

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return &Client{GodotBin: godotBin, OperationsScript: operationsScriptPath(t), Root: root}
}

func TestSetNodeProperty_RealGodot_ColorArray(t *testing.T) {
	c := polygon2DFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "ColorPoly",
		PropertyName: "vertex_colors",
		ColorArrayValue: []Color{
			{R: 1, G: 0, B: 0, A: 1},
			{R: 0, G: 1, B: 0, A: 0.5},
		},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "vertex_colors = PackedColorArray(1, 0, 0, 1, 0, 1, 0, 0.5)") {
		t.Errorf("saved scene missing expected vertex_colors property: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_EmptyColorArray mirrors the other
// TestSetNodeProperty_RealGodot_Empty*Array tests above: Polygon2D's
// vertex_colors is omitted from the saved scene once cleared back to
// empty, following the same omit-when-empty pattern already confirmed for
// _spawnable_scenes, tab_stops, and polygon — verified empirically here
// too, not assumed.
func TestSetNodeProperty_RealGodot_EmptyColorArray(t *testing.T) {
	c := polygon2DFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:       "main.tscn",
		NodePath:        "ColorPoly",
		PropertyName:    "vertex_colors",
		ColorArrayValue: []Color{{R: 1, G: 0, B: 0, A: 1}},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (initial non-empty set) against a real Godot binary: %v", err)
	}

	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:       "main.tscn",
		NodePath:        "ColorPoly",
		PropertyName:    "vertex_colors",
		ColorArrayValue: []Color{},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (clearing to an empty array) against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if strings.Contains(string(data), "vertex_colors") {
		t.Errorf("saved scene still has vertex_colors after clearing it to an empty array: %s", data)
	}
}

// navigationObstacle3DFixtureClient builds another separate, minimal
// fixture project, for the same reason polygon2DFixtureClient and its
// predecessors do: NavigationObstacle3D — the only built-in Node target for
// PackedVector3Array (vertices) that actually persists to the saved scene
// (CPUParticles3D.emission_points and emission_normals were ruled out:
// both have usage=0 in their ClassDB property info, meaning Godot never
// stores them regardless of value — they're runtime-only generated caches,
// not editable scene state) — leaks navigation-server and rendering RIDs
// at process exit (several ERROR lines, heavier than Window/
// VisibleOnScreenNotifier3D/Polygon2D's leaks but the same category).
// Keeping it confined to its own throwaway fixture avoids adding that
// noise to every other SetNodeProperty test.
func navigationObstacle3DFixtureClient(t *testing.T) *Client {
	t.Helper()
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	const setupScript = `extends SceneTree

func _init() -> void:
	var main := Node2D.new()
	main.name = "Main"

	var obs := NavigationObstacle3D.new()
	obs.name = "Obs"
	main.add_child(obs)
	obs.owner = main

	var packed := PackedScene.new()
	var pack_err := packed.pack(main)
	if pack_err != OK:
		push_error("failed to pack fixture scene: %d" % pack_err)
	var save_err := ResourceSaver.save(packed, "res://main.tscn")
	if save_err != OK:
		push_error("failed to save fixture scene: %d" % save_err)
	quit()
`
	scriptPath := filepath.Join(projectDir, "generate_navigation_obstacle_3d_fixture.gd")
	if err := os.WriteFile(scriptPath, []byte(setupScript), 0o644); err != nil {
		t.Fatalf("writing fixture-generation script: %v", err)
	}

	// #nosec G204 -- test-only, fixed argv, godotBin/projectDir/scriptPath
	// are all test-controlled, not external input.
	cmd := exec.Command(godotBin, "--headless", "--path", projectDir, "--script", scriptPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating navigation obstacle 3d fixture scene: %v\n%s", err, out)
	}

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return &Client{GodotBin: godotBin, OperationsScript: operationsScriptPath(t), Root: root}
}

func TestSetNodeProperty_RealGodot_Vector3Array(t *testing.T) {
	c := navigationObstacle3DFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Obs",
		PropertyName: "vertices",
		Vector3ArrayValue: []Vector3{
			{X: 1.5, Y: 2.5, Z: 3.5},
			{X: -1, Y: -2, Z: -3},
		},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), "vertices = PackedVector3Array(1.5, 2.5, 3.5, -1, -2, -3)") {
		t.Errorf("saved scene missing expected vertices property: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_EmptyVector3Array mirrors the other
// TestSetNodeProperty_RealGodot_Empty*Array tests above: verified
// empirically, not assumed, that NavigationObstacle3D.vertices is omitted
// from the saved scene once cleared back to empty.
func TestSetNodeProperty_RealGodot_EmptyVector3Array(t *testing.T) {
	c := navigationObstacle3DFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:         "main.tscn",
		NodePath:          "Obs",
		PropertyName:      "vertices",
		Vector3ArrayValue: []Vector3{{X: 1, Y: 2, Z: 3}},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (initial non-empty set) against a real Godot binary: %v", err)
	}

	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:         "main.tscn",
		NodePath:          "Obs",
		PropertyName:      "vertices",
		Vector3ArrayValue: []Vector3{},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (clearing to an empty array) against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if strings.Contains(string(data), "vertices") {
		t.Errorf("saved scene still has vertices after clearing it to an empty array: %s", data)
	}
}

func TestSetNodeProperty_RealGodot_NodePathArray(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:          "main.tscn",
		NodePath:           "Ctrl",
		PropertyName:       "accessibility_flow_to_nodes",
		NodePathArrayValue: []string{"../A", "../B"},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), `accessibility_flow_to_nodes = Array[NodePath]([NodePath("../A"), NodePath("../B")])`) {
		t.Errorf("saved scene missing expected accessibility_flow_to_nodes property: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_EmptyNodePathArray mirrors the other
// TestSetNodeProperty_RealGodot_Empty*Array tests above: verified
// empirically that Control.accessibility_flow_to_nodes is omitted from the
// saved scene once cleared back to empty.
func TestSetNodeProperty_RealGodot_EmptyNodePathArray(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:          "main.tscn",
		NodePath:           "Ctrl",
		PropertyName:       "accessibility_flow_to_nodes",
		NodePathArrayValue: []string{"../A"},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (initial non-empty set) against a real Godot binary: %v", err)
	}

	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:          "main.tscn",
		NodePath:           "Ctrl",
		PropertyName:       "accessibility_flow_to_nodes",
		NodePathArrayValue: []string{},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (clearing to an empty array) against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if strings.Contains(string(data), "accessibility_flow_to_nodes") {
		t.Errorf("saved scene still has accessibility_flow_to_nodes after clearing it to an empty array: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_ResourceValue exercises the reference-only
// resource path: resource_value names an existing .tres file, which must
// get loaded and assigned to a Texture2D-typed property, producing a real
// ext_resource entry in the saved scene (Godot's own serialization, not
// something this tool constructs by hand).
func TestSetNodeProperty_RealGodot_ResourceValue(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	const placeholderTexture = `[gd_resource type="PlaceholderTexture2D" format=3]

[resource]
size = Vector2(16, 16)
`
	if err := os.WriteFile(filepath.Join(c.Root.String(), "placeholder.tres"), []byte(placeholderTexture), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resVal := "placeholder.tres"
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "Sprite",
		PropertyName:  "texture",
		ResourceValue: &resVal,
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), `path="res://placeholder.tres"`) {
		t.Errorf("saved scene missing ext_resource pointing at placeholder.tres: %s", data)
	}
	if !strings.Contains(string(data), "texture = ExtResource(") {
		t.Errorf("saved scene missing texture = ExtResource(...) assignment: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_ResourceValueTypeMismatch verifies the
// type-check-before-set decided in the resource-reference maintainer
// conversation: Object.set() alone would happily store a Curve reference on
// a Texture2D-typed property (it doesn't enforce the declared type), so
// this must be caught explicitly and refused rather than silently written.
func TestSetNodeProperty_RealGodot_ResourceValueTypeMismatch(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	const mismatchedResource = `[gd_resource type="Curve" format=3]

[resource]
`
	if err := os.WriteFile(filepath.Join(c.Root.String(), "mismatched.tres"), []byte(mismatchedResource), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	before, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading scene before the attempt: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resVal := "mismatched.tres"
	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "Sprite",
		PropertyName:  "texture",
		ResourceValue: &resVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty assigning a Curve to a Texture2D-typed property, want error")
	}

	after, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading scene after the attempt: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("scene was modified despite the type-mismatch rejection:\nbefore: %s\nafter: %s", before, after)
	}
}

func TestSetNodeProperty_RealGodot_ResourceValueMissingFile(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resVal := "does_not_exist.tres"
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "Sprite",
		PropertyName:  "texture",
		ResourceValue: &resVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a resource_value pointing at a nonexistent file, want error")
	}
}

// TestSetNodeProperty_RealGodot_ScriptPropertyBlocked confirms the "script"
// hard block end-to-end, not just via the fast Go-side check exercised in
// headless_test.go: even a real, loadable script resource must never be
// assignable to a node's script property through this tool.
func TestSetNodeProperty_RealGodot_ScriptPropertyBlocked(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	before, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading scene before the attempt: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resVal := "custom_props_holder.gd"
	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "Cube",
		PropertyName:  "script",
		ResourceValue: &resVal,
	})
	if err == nil {
		t.Fatal("SetNodeProperty on property_name \"script\" against a real Godot binary, want error")
	}

	after, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading scene after the attempt: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("scene was modified despite the \"script\" block:\nbefore: %s\nafter: %s", before, after)
	}
}

// TestSetNodeProperty_RealGodot_TypedArrays exercises all six
// Typed*ArrayValue fields against custom_props_holder.gd's matching
// exported properties (see writeSetNodePropertyFixtureScene's doc comment
// for why no built-in Node target exists for any of them), table-driven
// since the shape of the check — set the field, read the saved scene back,
// look for Godot's own typed-array serialization of it — is identical
// across all six element types.
func TestSetNodeProperty_RealGodot_TypedArrays(t *testing.T) {
	tests := []struct {
		name     string
		property string
		set      func(*SetNodePropertyParams)
		want     string
	}{
		{
			name:     "TypedStringArrayValue",
			property: "typed_strings",
			set:      func(p *SetNodePropertyParams) { p.TypedStringArrayValue = []string{"alpha", "beta"} },
			want:     `typed_strings = Array[String](["alpha", "beta"])`,
		},
		{
			name:     "TypedIntArrayValue",
			property: "typed_ints",
			set:      func(p *SetNodePropertyParams) { p.TypedIntArrayValue = []int64{1, 2, 3} },
			want:     `typed_ints = Array[int]([1, 2, 3])`,
		},
		{
			name:     "TypedFloatArrayValue",
			property: "typed_floats",
			set:      func(p *SetNodePropertyParams) { p.TypedFloatArrayValue = []float64{1.5, 2.5} },
			want:     `typed_floats = Array[float]([1.5, 2.5])`,
		},
		{
			name:     "TypedVector2ArrayValue",
			property: "typed_vector2s",
			set:      func(p *SetNodePropertyParams) { p.TypedVector2ArrayValue = []Vector2{{X: 1, Y: 2}} },
			want:     `typed_vector2s = Array[Vector2]([Vector2(1, 2)])`,
		},
		{
			name:     "TypedColorArrayValue",
			property: "typed_colors",
			set:      func(p *SetNodePropertyParams) { p.TypedColorArrayValue = []Color{{R: 1, G: 0, B: 0, A: 1}} },
			want:     `typed_colors = Array[Color]([Color(1, 0, 0, 1)])`,
		},
		{
			name:     "TypedVector3ArrayValue",
			property: "typed_vector3s",
			set:      func(p *SetNodePropertyParams) { p.TypedVector3ArrayValue = []Vector3{{X: 1, Y: 2, Z: 3}} },
			want:     `typed_vector3s = Array[Vector3]([Vector3(1, 2, 3)])`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := setNodePropertyFixtureClient(t)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			params := SetNodePropertyParams{
				ScenePath:    "main.tscn",
				NodePath:     "IntGrid",
				PropertyName: tt.property,
			}
			tt.set(&params)

			if _, err := c.SetNodeProperty(ctx, params); err != nil {
				t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
			}

			data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
			if err != nil {
				t.Fatalf("reading saved scene: %v", err)
			}
			if !strings.Contains(string(data), tt.want) {
				t.Errorf("saved scene missing expected %s property: %s", tt.property, data)
			}
		})
	}
}

const placeholderTextureFixture = `[gd_resource type="PlaceholderTexture2D" format=3]

[resource]
size = Vector2(16, 16)
`

// TestSetNodeProperty_RealGodot_TypedResourceArrayValue exercises the
// multi-element path: each element is loaded independently and appended
// into a dynamically-typed Array[Texture2D] (see
// writeSetNodePropertyFixtureScene's doc comment for why the class name
// has to be parsed out of hint_string), producing real ext_resource
// entries in the saved scene.
func TestSetNodeProperty_RealGodot_TypedResourceArrayValue(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	if err := os.WriteFile(filepath.Join(c.Root.String(), "placeholder_a.tres"), []byte(placeholderTextureFixture), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(c.Root.String(), "placeholder_b.tres"), []byte(placeholderTextureFixture), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:               "main.tscn",
		NodePath:                "IntGrid",
		PropertyName:            "typed_textures",
		TypedResourceArrayValue: []string{"placeholder_a.tres", "placeholder_b.tres"},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), `path="res://placeholder_a.tres"`) || !strings.Contains(string(data), `path="res://placeholder_b.tres"`) {
		t.Errorf("saved scene missing ext_resource entries for both placeholder textures: %s", data)
	}
	if !strings.Contains(string(data), "typed_textures = Array[Texture2D](") {
		t.Errorf("saved scene missing typed_textures = Array[Texture2D](...) assignment: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_TypedResourceArrayValueTypeMismatch relies
// on Godot's own TypedArray container validation (confirmed empirically:
// appending an incompatible element is rejected at the engine level, with
// subclass tolerance handled automatically) rather than a hand-rolled
// ClassDB check like ResourceValue's scalar case needed.
func TestSetNodeProperty_RealGodot_TypedResourceArrayValueTypeMismatch(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	if err := os.WriteFile(filepath.Join(c.Root.String(), "placeholder_a.tres"), []byte(placeholderTextureFixture), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	const mismatchedResource = `[gd_resource type="Curve" format=3]

[resource]
`
	if err := os.WriteFile(filepath.Join(c.Root.String(), "mismatched.tres"), []byte(mismatchedResource), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	before, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading scene before the attempt: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:               "main.tscn",
		NodePath:                "IntGrid",
		PropertyName:            "typed_textures",
		TypedResourceArrayValue: []string{"placeholder_a.tres", "mismatched.tres"},
	})
	if err == nil {
		t.Fatal("SetNodeProperty appending a Curve into an Array[Texture2D]-typed property, want error")
	}

	after, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading scene after the attempt: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("scene was modified despite the type-mismatch rejection:\nbefore: %s\nafter: %s", before, after)
	}
}

func TestSetNodeProperty_RealGodot_TypedResourceArrayValueMissingFile(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:               "main.tscn",
		NodePath:                "IntGrid",
		PropertyName:            "typed_textures",
		TypedResourceArrayValue: []string{"does_not_exist.tres"},
	})
	if err == nil {
		t.Fatal("SetNodeProperty with a typed_resource_array_value element pointing at a nonexistent file, want error")
	}
}

// TestSetNodeProperty_RealGodot_TypedResourceArrayValueEmpty mirrors the
// other TestSetNodeProperty_RealGodot_Empty*Array tests: an empty typed
// resource array is a legitimate value (clearing the property), verified
// empirically to be omitted from the saved scene once cleared.
func TestSetNodeProperty_RealGodot_TypedResourceArrayValueEmpty(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	if err := os.WriteFile(filepath.Join(c.Root.String(), "placeholder_a.tres"), []byte(placeholderTextureFixture), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:               "main.tscn",
		NodePath:                "IntGrid",
		PropertyName:            "typed_textures",
		TypedResourceArrayValue: []string{"placeholder_a.tres"},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (initial non-empty set) against a real Godot binary: %v", err)
	}

	_, err = c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:               "main.tscn",
		NodePath:                "IntGrid",
		PropertyName:            "typed_textures",
		TypedResourceArrayValue: []string{},
	})
	if err != nil {
		t.Fatalf("SetNodeProperty (clearing to an empty array) against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if strings.Contains(string(data), "typed_textures") {
		t.Errorf("saved scene still has typed_textures after clearing it to an empty array: %s", data)
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

// scriptEditFixtureClient builds a fresh, temp-dir-backed project (not the
// shared testdata/fixture_project — same reasoning as
// setNodePropertyFixtureClient: these tests write to the project) with one
// script, player.gd, exercising a top-level @export var, a top-level
// function, and a nested "class Helper:" with a same-named method, so
// top-level-only scoping is proven against real Godot, not just the pure
// splice-function tests.
func scriptEditFixtureClient(t *testing.T) *Client {
	t.Helper()
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "player.gd"), []byte(scriptEditIntegrationFixtureSource), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return &Client{GodotBin: godotBin, OperationsScript: operationsScriptPath(t), Root: root}
}

const scriptEditIntegrationFixtureSource = `extends Node

@export var speed: float = 300.0

func take_damage(amount: int) -> void:
	print(amount)

class Helper:
	func take_damage() -> void:
		pass
`

func TestSetScriptExport_RealGodot_InsertsNewDeclaration(t *testing.T) {
	c := scriptEditFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	i := int64(100)
	result, err := c.SetScriptExport(ctx, SetScriptExportParams{
		ScriptPath: "player.gd",
		Name:       "health",
		IntValue:   &i,
	})
	if err != nil {
		t.Fatalf("SetScriptExport against a real Godot binary: %v", err)
	}
	if result.Action != "inserted" {
		t.Errorf("Action = %q, want %q", result.Action, "inserted")
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading saved script: %v", err)
	}
	if !strings.Contains(string(data), "@export var health: int = 100") {
		t.Errorf("saved script missing the new declaration: %s", data)
	}
	if !strings.Contains(string(data), "@export var speed: float = 300.0") {
		t.Errorf("saved script lost the existing declaration: %s", data)
	}
}

func TestSetScriptExport_RealGodot_ModifiesExistingDeclaration(t *testing.T) {
	c := scriptEditFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	f := 500.0
	result, err := c.SetScriptExport(ctx, SetScriptExportParams{
		ScriptPath: "player.gd",
		Name:       "speed",
		FloatValue: &f,
	})
	if err != nil {
		t.Fatalf("SetScriptExport against a real Godot binary: %v", err)
	}
	if result.Action != "modified" {
		t.Errorf("Action = %q, want %q", result.Action, "modified")
	}
	if result.PreviousDeclaration != "@export var speed: float = 300.0" {
		t.Errorf("PreviousDeclaration = %q", result.PreviousDeclaration)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading saved script: %v", err)
	}
	if !strings.Contains(string(data), "@export var speed: float = 500.0") {
		t.Errorf("saved script missing the modified declaration: %s", data)
	}
}

// TestSetScriptExport_RealGodot_RemainingValueTypesInsertSuccessfully
// exercises the 11 value types deferred when set_script_export first
// shipped (Vector2i/Vector3i/Quaternion/Rect2/Rect2i/Plane/AABB/Basis/
// Transform2D/Transform3D/NodePath) — each confirmed empirically to be a
// valid @export default against a real Godot 4.7.2 binary before this
// test was written. Table-driven since the shape of the check (insert,
// read the file back, look for the rendered literal) is identical across
// all eleven.
func TestSetScriptExport_RealGodot_RemainingValueTypesInsertSuccessfully(t *testing.T) {
	basis := Basis{X: Vector3{X: 1}, Y: Vector3{Y: 1}, Z: Vector3{Z: 1}}
	nodePath := "../Target"

	tests := []struct {
		name string
		set  func(*SetScriptExportParams)
		want string
	}{
		{"Vector2iValue", func(p *SetScriptExportParams) { p.Vector2iValue = &Vector2i{X: 1, Y: 2} }, "Vector2i(1, 2)"},
		{"Vector3iValue", func(p *SetScriptExportParams) { p.Vector3iValue = &Vector3i{X: 1, Y: 2, Z: 3} }, "Vector3i(1, 2, 3)"},
		{"QuaternionValue", func(p *SetScriptExportParams) { p.QuaternionValue = &Quaternion{W: 1} }, "Quaternion(0.0, 0.0, 0.0, 1.0)"},
		{"Rect2Value", func(p *SetScriptExportParams) {
			p.Rect2Value = &Rect2{Position: Vector2{X: 1, Y: 2}, Size: Vector2{X: 3, Y: 4}}
		}, "Rect2(1.0, 2.0, 3.0, 4.0)"},
		{"Rect2iValue", func(p *SetScriptExportParams) {
			p.Rect2iValue = &Rect2i{Position: Vector2i{X: 1, Y: 2}, Size: Vector2i{X: 3, Y: 4}}
		}, "Rect2i(1, 2, 3, 4)"},
		{"PlaneValue", func(p *SetScriptExportParams) { p.PlaneValue = &Plane{Y: 1, D: 5} }, "Plane(0.0, 1.0, 0.0, 5.0)"},
		{"AABBValue", func(p *SetScriptExportParams) {
			p.AABBValue = &AABB{Size: Vector3{X: 1, Y: 1, Z: 1}}
		}, "AABB(Vector3(0.0, 0.0, 0.0), Vector3(1.0, 1.0, 1.0))"},
		{"BasisValue", func(p *SetScriptExportParams) { p.BasisValue = &basis }, "Basis(Vector3(1.0, 0.0, 0.0), Vector3(0.0, 1.0, 0.0), Vector3(0.0, 0.0, 1.0))"},
		{"Transform2DValue", func(p *SetScriptExportParams) {
			p.Transform2DValue = &Transform2D{X: Vector2{X: 1}, Y: Vector2{Y: 1}}
		}, "Transform2D(Vector2(1.0, 0.0), Vector2(0.0, 1.0), Vector2(0.0, 0.0))"},
		{"Transform3DValue", func(p *SetScriptExportParams) {
			p.Transform3DValue = &Transform3D{Basis: basis}
		}, "Transform3D(Basis(Vector3(1.0, 0.0, 0.0), Vector3(0.0, 1.0, 0.0), Vector3(0.0, 0.0, 1.0)), Vector3(0.0, 0.0, 0.0))"},
		{"NodePathValue", func(p *SetScriptExportParams) { p.NodePathValue = &nodePath }, `NodePath("../Target")`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := scriptEditFixtureClient(t)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			params := SetScriptExportParams{ScriptPath: "player.gd", Name: "test_val"}
			tt.set(&params)

			result, err := c.SetScriptExport(ctx, params)
			if err != nil {
				t.Fatalf("SetScriptExport against a real Godot binary: %v", err)
			}
			if result.Action != "inserted" {
				t.Errorf("Action = %q, want %q", result.Action, "inserted")
			}

			data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
			if err != nil {
				t.Fatalf("reading saved script: %v", err)
			}
			if !strings.Contains(string(data), tt.want) {
				t.Errorf("saved script missing expected declaration %q: %s", tt.want, data)
			}
		})
	}
}

// TestSetScriptExport_RealGodot_RollsBackOnInvalidResult uses a name that
// passes Go's identifier regex but is a GDScript reserved word, so only
// the real parser (via checkScriptParses) catches it.
func TestSetScriptExport_RealGodot_RollsBackOnInvalidResult(t *testing.T) {
	c := scriptEditFixtureClient(t)

	before, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading script before the attempt: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	i := int64(1)
	_, err = c.SetScriptExport(ctx, SetScriptExportParams{
		ScriptPath: "player.gd",
		Name:       "var",
		IntValue:   &i,
	})
	if err == nil {
		t.Fatal("SetScriptExport with a reserved-word name against a real Godot binary, want error")
	}

	after, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading script after the attempt: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("script was modified despite the parse-failure rollback:\nbefore: %s\nafter: %s", before, after)
	}
}

func TestSetFunctionBody_RealGodot_ReplacesExistingFunctionBody(t *testing.T) {
	c := scriptEditFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.SetFunctionBody(ctx, SetFunctionBodyParams{
		ScriptPath:   "player.gd",
		FunctionName: "take_damage",
		Body:         "print(\"took %d damage\" % amount)",
	})
	if err != nil {
		t.Fatalf("SetFunctionBody against a real Godot binary: %v", err)
	}
	if result.Action != "modified" {
		t.Errorf("Action = %q, want %q", result.Action, "modified")
	}
	if result.PreviousSignature != "func take_damage(amount: int) -> void:" {
		t.Errorf("PreviousSignature = %q", result.PreviousSignature)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading saved script: %v", err)
	}
	if !strings.Contains(string(data), "func take_damage(amount: int) -> void:\n\tprint(\"took %d damage\" % amount)") {
		t.Errorf("saved script missing the replaced body: %s", data)
	}
}

// TestSetFunctionBody_RealGodot_SignatureVariations exercises replacing
// parameters only, return type only, and both together against a real
// Godot parse — table-driven since the shape of the check is identical.
func TestSetFunctionBody_RealGodot_SignatureVariations(t *testing.T) {
	tests := []struct {
		name       string
		parameters *string
		returnType *string
		wantSig    string
	}{
		{"parameters only", strPtr("amount: int, source: Node"), nil, "func take_damage(amount: int, source: Node) -> void:"},
		{"return type only", nil, strPtr("bool"), "func take_damage(amount: int) -> bool:"},
		{"both", strPtr("amount: int, source: Node"), strPtr("bool"), "func take_damage(amount: int, source: Node) -> bool:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := scriptEditFixtureClient(t)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			body := "print(amount)"
			if tt.returnType != nil && *tt.returnType == "bool" {
				body = "print(amount)\nreturn true"
			}
			_, err := c.SetFunctionBody(ctx, SetFunctionBodyParams{
				ScriptPath:   "player.gd",
				FunctionName: "take_damage",
				Parameters:   tt.parameters,
				ReturnType:   tt.returnType,
				Body:         body,
			})
			if err != nil {
				t.Fatalf("SetFunctionBody against a real Godot binary: %v", err)
			}

			data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
			if err != nil {
				t.Fatalf("reading saved script: %v", err)
			}
			if !strings.Contains(string(data), tt.wantSig) {
				t.Errorf("saved script missing expected signature %q: %s", tt.wantSig, data)
			}
		})
	}
}

func TestSetFunctionBody_RealGodot_InsertsNewFunction(t *testing.T) {
	c := scriptEditFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.SetFunctionBody(ctx, SetFunctionBodyParams{
		ScriptPath:   "player.gd",
		FunctionName: "heal",
		Parameters:   strPtr("amount: int"),
		ReturnType:   strPtr("void"),
		Body:         "print(amount)",
	})
	if err != nil {
		t.Fatalf("SetFunctionBody against a real Godot binary: %v", err)
	}
	if result.Action != "inserted" {
		t.Errorf("Action = %q, want %q", result.Action, "inserted")
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading saved script: %v", err)
	}
	if !strings.Contains(string(data), "func heal(amount: int) -> void:\n\tprint(amount)") {
		t.Errorf("saved script missing the inserted function: %s", data)
	}
	if !strings.Contains(string(data), "func take_damage(amount: int) -> void:") {
		t.Errorf("saved script lost the existing function: %s", data)
	}
}

func TestSetFunctionBody_RealGodot_InsertsNewFunction_NoParametersNoReturnType(t *testing.T) {
	c := scriptEditFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetFunctionBody(ctx, SetFunctionBodyParams{
		ScriptPath:   "player.gd",
		FunctionName: "reset",
		Body:         "pass",
	})
	if err != nil {
		t.Fatalf("SetFunctionBody against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading saved script: %v", err)
	}
	if !strings.Contains(string(data), "func reset():\n\tpass") {
		t.Errorf("saved script missing the bare inserted function: %s", data)
	}
}

func TestSetFunctionBody_RealGodot_TopLevelOnly_DoesNotTouchNestedClassFunction(t *testing.T) {
	c := scriptEditFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.SetFunctionBody(ctx, SetFunctionBodyParams{
		ScriptPath:   "player.gd",
		FunctionName: "take_damage",
		Body:         "print(\"top-level replacement\")",
	})
	if err != nil {
		t.Fatalf("SetFunctionBody against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading saved script: %v", err)
	}
	if !strings.Contains(string(data), "\tfunc take_damage() -> void:\n\t\tpass") {
		t.Errorf("saved script lost Helper's own nested take_damage(): %s", data)
	}
	if !strings.Contains(string(data), "print(\"top-level replacement\")") {
		t.Errorf("saved script missing the top-level replacement: %s", data)
	}
}

// TestSetFunctionBody_RealGodot_RollsBackOnInvalidResult supplies a body
// with genuinely invalid GDScript syntax that only the real parser can
// catch — Go has no business trying to validate arbitrary statement text.
func TestSetFunctionBody_RealGodot_RollsBackOnInvalidResult(t *testing.T) {
	c := scriptEditFixtureClient(t)

	before, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading script before the attempt: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = c.SetFunctionBody(ctx, SetFunctionBodyParams{
		ScriptPath:   "player.gd",
		FunctionName: "take_damage",
		Body:         "this is not ) valid gdscript !!!",
	})
	if err == nil {
		t.Fatal("SetFunctionBody with invalid GDScript body against a real Godot binary, want error")
	}

	after, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading script after the attempt: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("script was modified despite the parse-failure rollback:\nbefore: %s\nafter: %s", before, after)
	}
}

func TestSetScriptExport_RealGodot_OnreadyInsertsNewDeclaration(t *testing.T) {
	c := scriptEditFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	i := int64(5)
	result, err := c.SetScriptExport(ctx, SetScriptExportParams{
		ScriptPath: "player.gd",
		Name:       "target_count",
		IntValue:   &i,
		Onready:    true,
	})
	if err != nil {
		t.Fatalf("SetScriptExport with Onready set against a real Godot binary: %v", err)
	}
	if result.Action != "inserted" {
		t.Errorf("Action = %q, want %q", result.Action, "inserted")
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading saved script: %v", err)
	}
	if !strings.Contains(string(data), "@onready var target_count: int = 5") {
		t.Errorf("saved script missing the new @onready declaration: %s", data)
	}
	if !strings.Contains(string(data), "@export var speed: float = 300.0") {
		t.Errorf("saved script lost the existing @export declaration: %s", data)
	}
}

func TestSetScriptSignal_RealGodot_InsertsAndModifiesSignal(t *testing.T) {
	c := scriptEditFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.SetScriptSignal(ctx, SetScriptSignalParams{
		ScriptPath: "player.gd",
		Name:       "died",
	})
	if err != nil {
		t.Fatalf("SetScriptSignal against a real Godot binary: %v", err)
	}
	if result.Action != "inserted" {
		t.Errorf("Action = %q, want %q", result.Action, "inserted")
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading saved script: %v", err)
	}
	if !strings.Contains(string(data), "signal died\n") {
		t.Errorf("saved script missing the new bare signal: %s", data)
	}

	result, err = c.SetScriptSignal(ctx, SetScriptSignalParams{
		ScriptPath: "player.gd",
		Name:       "died",
		Parameters: strPtr("cause: String"),
	})
	if err != nil {
		t.Fatalf("SetScriptSignal (modify) against a real Godot binary: %v", err)
	}
	if result.Action != "modified" {
		t.Errorf("Action = %q, want %q", result.Action, "modified")
	}
	if result.PreviousDeclaration != "signal died" {
		t.Errorf("PreviousDeclaration = %q", result.PreviousDeclaration)
	}

	data, err = os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading saved script: %v", err)
	}
	if !strings.Contains(string(data), "signal died(cause: String)") {
		t.Errorf("saved script missing the modified signal: %s", data)
	}
}

func TestSetScriptIdentity_RealGodot_SetsClassNameAndExtends(t *testing.T) {
	c := scriptEditFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.SetScriptIdentity(ctx, SetScriptIdentityParams{
		ScriptPath: "player.gd",
		ClassName:  strPtr("Player"),
		Extends:    strPtr("Node2D"),
	})
	if err != nil {
		t.Fatalf("SetScriptIdentity against a real Godot binary: %v", err)
	}
	if result.ClassNameAction != "inserted" || result.ExtendsAction != "modified" {
		t.Errorf("ClassNameAction = %q, ExtendsAction = %q, want %q and %q", result.ClassNameAction, result.ExtendsAction, "inserted", "modified")
	}
	if result.PreviousExtends != "extends Node" {
		t.Errorf("PreviousExtends = %q", result.PreviousExtends)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading saved script: %v", err)
	}
	if !strings.Contains(string(data), "class_name Player\nextends Node2D\n") {
		t.Errorf("saved script missing the new class_name/extends header: %s", data)
	}
}

// TestSetScriptIdentity_RealGodot_RollsBackOnInvalidExtendsTarget uses an
// extends target that is syntactically a valid identifier but names no
// real class, so only the real parser (via checkScriptParses) catches it —
// exactly the kind of error this tool's own Go-side validation cannot
// detect ahead of time (see SetScriptIdentityParams's doc comment).
func TestSetScriptIdentity_RealGodot_RollsBackOnInvalidExtendsTarget(t *testing.T) {
	c := scriptEditFixtureClient(t)

	before, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading script before the attempt: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = c.SetScriptIdentity(ctx, SetScriptIdentityParams{
		ScriptPath: "player.gd",
		Extends:    strPtr("TotallyNonexistentBaseClass12345"),
	})
	if err == nil {
		t.Fatal("SetScriptIdentity with a nonexistent extends target against a real Godot binary, want error")
	}

	after, err := os.ReadFile(filepath.Join(c.Root.String(), "player.gd"))
	if err != nil {
		t.Fatalf("reading script after the attempt: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("script was modified despite the parse-failure rollback:\nbefore: %s\nafter: %s", before, after)
	}
}

// sceneUIDFixtureClient builds a fresh, temp-dir-backed project with a
// scene shaped like the bug report that motivated preserveSceneUIDs
// (scene_uid.go): a [gd_scene] header and one [ext_resource] line each
// carrying a hand-set uid="..." attribute, matching what a real
// Godot-4.4+-saved scene looks like. The uid values here are just
// syntactically valid uid://-shaped strings, not registered in any
// project-wide UID cache — confirmed during investigation that this
// doesn't matter: the fix operates at the text level, independent of
// whether Godot's own loader considers the uid resolvable.
func sceneUIDFixtureClient(t *testing.T) *Client {
	t.Helper()
	godotBin := godotBinForTest(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "alpha.gd"), []byte("extends ColorRect\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	const scene = `[gd_scene load_steps=2 format=3 uid="uid://dp18m7gwcwhl0"]

[ext_resource type="Script" uid="uid://b0i8615afj62o" path="res://alpha.gd" id="1_8wgy4"]

[node name="Main" type="ColorRect"]
script = ExtResource("1_8wgy4")
color = Color(0.4589408, 0.83825284, 0.31885445, 1)

[node name="Sprite" type="Sprite2D" parent="."]
`
	if err := os.WriteFile(filepath.Join(projectDir, "main.tscn"), []byte(scene), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	root, err := validate.NewRoot(projectDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return &Client{GodotBin: godotBin, OperationsScript: operationsScriptPath(t), Root: root}
}

func TestSetNodeProperty_RealGodot_PreservesSceneUID(t *testing.T) {
	c := sceneUIDFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	red := Color{R: 1, G: 0, B: 0, A: 1}
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "",
		PropertyName: "color",
		ColorValue:   &red,
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), `uid="uid://dp18m7gwcwhl0"`) {
		t.Errorf("saved scene lost the gd_scene-level uid: %s", data)
	}
	if !strings.Contains(string(data), `uid="uid://b0i8615afj62o"`) {
		t.Errorf("saved scene lost the ext_resource uid: %s", data)
	}
	if !strings.Contains(string(data), "color = Color(1, 0, 0, 1)") {
		t.Errorf("saved scene missing the requested color change: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_NoSpuriousUIDOnSceneWithoutOne proves the
// fix doesn't invent uid attributes on a scene that never had any — the
// common case, and the shape of every other set_node_property fixture in
// this test file.
func TestSetNodeProperty_RealGodot_NoSpuriousUIDOnSceneWithoutOne(t *testing.T) {
	c := setNodePropertyFixtureClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	strVal := "hello"
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:    "main.tscn",
		NodePath:     "Label",
		PropertyName: "text",
		StringValue:  &strVal,
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if strings.Contains(string(data), "uid=") {
		t.Errorf("saved scene has a uid= attribute that was never there before: %s", data)
	}
}

// TestSetNodeProperty_RealGodot_PreservesUIDAlongsideNewResourceReference
// proves the fix composes correctly with resource_value: the pre-existing
// ext_resource's uid survives even when the same call introduces a
// brand-new ext_resource (which, having no original counterpart, gets no
// restored uid of its own — expected, not a regression).
func TestSetNodeProperty_RealGodot_PreservesUIDAlongsideNewResourceReference(t *testing.T) {
	c := sceneUIDFixtureClient(t)

	const placeholderTexture = `[gd_resource type="PlaceholderTexture2D" format=3]

[resource]
size = Vector2(16, 16)
`
	if err := os.WriteFile(filepath.Join(c.Root.String(), "placeholder.tres"), []byte(placeholderTexture), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resVal := "placeholder.tres"
	_, err := c.SetNodeProperty(ctx, SetNodePropertyParams{
		ScenePath:     "main.tscn",
		NodePath:      "Sprite",
		PropertyName:  "texture",
		ResourceValue: &resVal,
	})
	if err != nil {
		t.Fatalf("SetNodeProperty against a real Godot binary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.Root.String(), "main.tscn"))
	if err != nil {
		t.Fatalf("reading saved scene: %v", err)
	}
	if !strings.Contains(string(data), `uid="uid://dp18m7gwcwhl0"`) {
		t.Errorf("saved scene lost the gd_scene-level uid: %s", data)
	}
	if !strings.Contains(string(data), `uid="uid://b0i8615afj62o"`) {
		t.Errorf("saved scene lost the pre-existing script's ext_resource uid: %s", data)
	}
	if !strings.Contains(string(data), `path="res://placeholder.tres"`) {
		t.Errorf("saved scene missing the new ext_resource for placeholder.tres: %s", data)
	}
}
