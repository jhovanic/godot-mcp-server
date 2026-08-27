// Package headless implements the headless CLI tier: scoped scene/script/
// resource operations carried out by invoking `godot --headless --script`
// against a single, fixed operations script.
//
// Every call here goes through validate.Root first, and every request sent
// to Godot is a structured JSON payload for one of a fixed set of named
// operations — never a generated or free-form script. See CLAUDE.md and
// SECURITY.md: there is no eval path through this package, and there must
// never be one.
package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jhovanic/godot-mcp-server/internal/validate"
)

// Client invokes the fixed Godot operations script in headless mode.
type Client struct {
	// GodotBin is the path to (or name of, if on PATH) the Godot executable.
	GodotBin string
	// OperationsScript is the absolute path to the fixed operations
	// GDScript entry point (scripts/godot_operations.gd). This path is
	// server configuration, not user/AI input.
	OperationsScript string
	// Root is the validated project root every operation is scoped to.
	Root *validate.Root
}

// request is the fixed envelope sent to the operations script. Operation
// must be one of the script's known, hard-coded cases — there is no
// interpretation of it as code, only as a dispatch key.
type request struct {
	Operation string `json:"operation"`
	Params    any    `json:"params"`
}

// response is the fixed envelope the operations script prints as its sole
// line of *its own* stdout output. It is not necessarily the only line in
// the Godot process's stdout as a whole — the Godot binary itself writes an
// engine startup banner to stdout before any script code runs (and future
// versions may add more) — see parseResponse.
type response struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// ReadSceneTreeParams are the parameters for the read_scene_tree operation.
type ReadSceneTreeParams struct {
	// ScenePath is the .tscn path, relative to the project root, e.g.
	// "scenes/main.tscn". It is validated against Root before use.
	ScenePath string `json:"scene_path" jsonschema:"path to a .tscn file, relative to the project root"`
}

// SceneNode is one node in a scene tree, as reported by Godot.
type SceneNode struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	Children []SceneNode `json:"children,omitempty"`
}

// ReadSceneTree loads a .tscn file headlessly and returns its node tree.
// This is a read-only operation: it does not modify the scene file or any
// project state.
func (c *Client) ReadSceneTree(ctx context.Context, params ReadSceneTreeParams) (*SceneNode, error) {
	absScenePath, err := c.Root.Resolve(params.ScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_scene_tree: %w", err)
	}

	relScenePath, err := filepath.Rel(c.Root.String(), absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_scene_tree: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relScenePath)

	var node SceneNode
	if err := c.run(ctx, "read_scene_tree", struct {
		Path string `json:"path"`
	}{Path: resPath}, &node); err != nil {
		return nil, fmt.Errorf("headless: read_scene_tree: %w", err)
	}
	return &node, nil
}

// ReadScriptParams are the parameters for the read_script operation.
type ReadScriptParams struct {
	// ScriptPath is the .gd path, relative to the project root, e.g.
	// "scripts/player.gd". It is validated against Root before use.
	ScriptPath string `json:"script_path" jsonschema:"path to a .gd script file, relative to the project root"`
}

// ScriptContents is a script's raw source text, as read from disk.
type ScriptContents struct {
	// Path is the script's res://-style path, echoed back for consistency
	// with how the headless tier addresses project files elsewhere.
	Path   string `json:"path"`
	Source string `json:"source"`
}

// ReadScript reads a .gd script file's raw source text.
//
// Unlike ReadSceneTree, this never invokes Godot: GDScript files are plain
// UTF-8 text, and Godot's own implementation of "read this file" would just
// be FileAccess.open(path).get_as_text() — there is no engine capability
// this operation actually needs. Skipping the engine round trip makes this
// a plain validated file read: faster (no ~100-300ms process spawn per
// call), a much smaller failure surface, and unit-testable without a Godot
// binary at all. This is a read-only operation: it does not modify the
// script file or any other project state.
func (c *Client) ReadScript(_ context.Context, params ReadScriptParams) (*ScriptContents, error) {
	absPath, err := c.Root.Resolve(params.ScriptPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_script: %w", err)
	}
	if filepath.Ext(absPath) != ".gd" {
		return nil, fmt.Errorf("headless: read_script: not a .gd file: %s", params.ScriptPath)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_script: %w", err)
	}

	relPath, err := filepath.Rel(c.Root.String(), absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_script: computing project-relative path: %w", err)
	}

	return &ScriptContents{
		Path:   "res://" + filepath.ToSlash(relPath),
		Source: string(data),
	}, nil
}

// ReadProjectSettingsParams are the parameters for the
// read_project_settings operation. There are none: every Godot project has
// exactly one project.godot at its root, so there is nothing to
// parameterize (and, unlike ReadScript/ReadSceneTree, no path input at all
// means no path-traversal surface for this operation).
type ReadProjectSettingsParams struct{}

// ProjectSettings is the raw contents of a project's project.godot file.
type ProjectSettings struct {
	// Path is always "res://project.godot" — present for consistency with
	// ScriptContents' shape, not because it varies.
	Path   string `json:"path"`
	Source string `json:"source"`
}

// ReadProjectSettings reads the project's project.godot file's raw text.
//
// Like ReadScript, this never invokes Godot: project.godot is Godot's own
// plain-text config format, and there's no engine capability needed to
// return it verbatim. This is a read-only operation: it does not modify
// project.godot or any other project state.
func (c *Client) ReadProjectSettings(_ context.Context, _ ReadProjectSettingsParams) (*ProjectSettings, error) {
	absPath, err := c.Root.Resolve("project.godot")
	if err != nil {
		return nil, fmt.Errorf("headless: read_project_settings: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_project_settings: %w", err)
	}

	return &ProjectSettings{
		Path:   "res://project.godot",
		Source: string(data),
	}, nil
}

// ReadTextResourceParams are the parameters for the read_text_resource
// operation.
type ReadTextResourceParams struct {
	// ResourcePath is the .tres path, relative to the project root, e.g.
	// "materials/red.tres". It is validated against Root before use.
	ResourcePath string `json:"resource_path" jsonschema:"path to a .tres text resource file, relative to the project root"`
}

// TextResourceContents is a text resource's raw source, as read from disk.
type TextResourceContents struct {
	// Path is the resource's res://-style path, echoed back for consistency
	// with how the headless tier addresses project files elsewhere.
	Path   string `json:"path"`
	Source string `json:"source"`
}

// ReadTextResource reads a .tres text resource file's raw source text.
//
// Like ReadScript, this never invokes Godot: .tres is Godot's own
// human-readable text serialization for resources (the same style of
// format .tscn uses for scenes), so there's no engine capability needed to
// return it verbatim.
//
// This is deliberately scoped to .tres only — hence "TextResource" rather
// than a bare "Resource" name, which would misleadingly suggest it also
// covers Godot's other resource format, .res. .res is binary-packed, and
// reading it meaningfully would need a real Godot round trip to decode, the
// same way ReadSceneTree needs Godot to instantiate a .tscn's node graph:
// a genuinely different implementation strategy, not a variant of this one.
// That's tracked as a separate, still-open FEATURES.md item and, when
// built, should be its own tool rather than a branch bolted onto this one.
// This is a read-only operation: it does not modify the resource file or
// any other project state.
func (c *Client) ReadTextResource(_ context.Context, params ReadTextResourceParams) (*TextResourceContents, error) {
	absPath, err := c.Root.Resolve(params.ResourcePath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_text_resource: %w", err)
	}

	switch filepath.Ext(absPath) {
	case ".tres":
		// supported
	case ".res":
		return nil, fmt.Errorf("headless: read_text_resource: %s is a binary .res resource — not supported by this operation, since decoding it meaningfully would need a real Godot round trip; only .tres text resources are supported", params.ResourcePath)
	default:
		return nil, fmt.Errorf("headless: read_text_resource: not a .tres file: %s", params.ResourcePath)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_text_resource: %w", err)
	}

	relPath, err := filepath.Rel(c.Root.String(), absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_text_resource: computing project-relative path: %w", err)
	}

	return &TextResourceContents{
		Path:   "res://" + filepath.ToSlash(relPath),
		Source: string(data),
	}, nil
}

// ReadBinaryResourceParams are the parameters for the read_binary_resource
// operation.
type ReadBinaryResourceParams struct {
	// ResourcePath is the .res path, relative to the project root, e.g.
	// "materials/red.res". It is validated against Root before use.
	ResourcePath string `json:"resource_path" jsonschema:"path to a .res binary resource file, relative to the project root"`
}

// BinaryResourceContents is a binary resource's contents, decoded via Godot
// into its own .tres text serialization.
type BinaryResourceContents struct {
	// Path is the resource's original res://-style path (the .res file
	// that was read) — not the path of the temporary .tres it was decoded
	// through, which never outlives this call.
	Path   string `json:"path"`
	Source string `json:"source"`
}

// ReadBinaryResource decodes a .res binary resource file by loading it in
// Godot and re-serializing it through Godot's own .tres text format, then
// returning that text.
//
// Unlike ReadTextResource, this genuinely needs Godot: .res is
// binary-packed, and there is no way to interpret its bytes meaningfully
// without the engine's own resource loader — the same reason ReadSceneTree
// needs Godot to instantiate a .tscn's node graph.
//
// GDScript has no in-memory "serialize this Resource to a string" API,
// only the file-based ResourceSaver.save(), so this necessarily writes a
// temporary .tres file — but always at a Go-generated path in the OS temp
// directory, outside the configured project root, removed before this
// function returns. The AI client never sees or influences that path; it
// only ever sees the res:// path of the .res file it asked to read, and
// the decoded text back. This is a read-only operation with respect to the
// project: it does not modify the .res file, or anything else inside the
// configured project root.
func (c *Client) ReadBinaryResource(ctx context.Context, params ReadBinaryResourceParams) (*BinaryResourceContents, error) {
	absPath, err := c.Root.Resolve(params.ResourcePath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_binary_resource: %w", err)
	}

	switch filepath.Ext(absPath) {
	case ".res":
		// supported
	case ".tres":
		return nil, fmt.Errorf("headless: read_binary_resource: %s is a .tres text resource, not a binary .res resource — use read_text_resource instead", params.ResourcePath)
	default:
		return nil, fmt.Errorf("headless: read_binary_resource: not a .res file: %s", params.ResourcePath)
	}

	relPath, err := filepath.Rel(c.Root.String(), absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_binary_resource: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relPath)

	tmp, err := os.CreateTemp("", "godot-mcp-server-decoded-*.tres")
	if err != nil {
		return nil, fmt.Errorf("headless: read_binary_resource: creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := c.run(ctx, "read_binary_resource", struct {
		Path    string `json:"path"`
		OutPath string `json:"out_path"`
	}{Path: resPath, OutPath: tmpPath}, nil); err != nil {
		return nil, fmt.Errorf("headless: read_binary_resource: %w", err)
	}

	decoded, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_binary_resource: reading decoded output: %w", err)
	}

	return &BinaryResourceContents{
		Path:   resPath,
		Source: string(decoded),
	}, nil
}

// ReadImportSettingsParams are the parameters for the read_import_settings
// operation.
type ReadImportSettingsParams struct {
	// AssetPath is the imported asset's own path, relative to the project
	// root, e.g. "textures/icon.png" — not the .import file's path. It is
	// validated against Root before use.
	AssetPath string `json:"asset_path" jsonschema:"path to the imported asset, relative to the project root (not the .import file itself)"`
}

// ImportSettings is the raw contents of an asset's <path>.import file.
type ImportSettings struct {
	// Path is the .import file's own res://-style path (AssetPath with
	// ".import" appended) — what Source's bytes actually come from.
	Path   string `json:"path"`
	Source string `json:"source"`
}

// ReadImportSettings reads the raw text of an imported asset's <path>.import
// sidecar file.
//
// Like ReadProjectSettings, this never invokes Godot: .import files are
// Godot's own plain-text ConfigFile-style format (the same style
// project.godot uses), generated next to an asset once a project has been
// opened/imported at least once. There's no engine capability needed to
// return it verbatim. This is a read-only operation: it does not modify the
// .import file, the asset it describes, or any other project state.
func (c *Client) ReadImportSettings(_ context.Context, params ReadImportSettingsParams) (*ImportSettings, error) {
	absAssetPath, err := c.Root.Resolve(params.AssetPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_import_settings: %w", err)
	}
	absImportPath := absAssetPath + ".import"

	data, err := os.ReadFile(absImportPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_import_settings: %w (no .import sidecar — the asset may not be an importable type, or the project hasn't been opened/imported yet)", err)
	}

	relImportPath, err := filepath.Rel(c.Root.String(), absImportPath)
	if err != nil {
		return nil, fmt.Errorf("headless: read_import_settings: computing project-relative path: %w", err)
	}

	return &ImportSettings{
		Path:   "res://" + filepath.ToSlash(relImportPath),
		Source: string(data),
	}, nil
}

// Vector2 is a two-component float vector, matching Godot's own Vector2
// type. Used for properties like position and scale.
type Vector2 struct {
	X float64 `json:"x" jsonschema:"the vector's x component"`
	Y float64 `json:"y" jsonschema:"the vector's y component"`
}

// Vector3 is a three-component float vector, matching Godot's own Vector3
// type. Used for properties like position and scale on Node3D-derived
// nodes, the 3D counterpart to Vector2.
type Vector3 struct {
	X float64 `json:"x" jsonschema:"the vector's x component"`
	Y float64 `json:"y" jsonschema:"the vector's y component"`
	Z float64 `json:"z" jsonschema:"the vector's z component"`
}

// Vector2i is a two-component integer vector, matching Godot's own Vector2i
// type. Used for properties like frame_coords or viewport sizes, where
// Godot itself distinguishes the integer type from Vector2 rather than
// coercing.
type Vector2i struct {
	X int64 `json:"x" jsonschema:"the vector's x component"`
	Y int64 `json:"y" jsonschema:"the vector's y component"`
}

// Vector3i is a three-component integer vector, matching Godot's own
// Vector3i type. No built-in Node class exposes a Vector3i property (the
// only one in the engine, PlaceholderTexture3D.size, is a Resource
// property), so this is primarily useful for a project's own custom node
// scripts with an exported Vector3i property — e.g. grid or voxel
// coordinates.
type Vector3i struct {
	X int64 `json:"x" jsonschema:"the vector's x component"`
	Y int64 `json:"y" jsonschema:"the vector's y component"`
	Z int64 `json:"z" jsonschema:"the vector's z component"`
}

// Color is an RGBA color, matching Godot's own Color type. Components are
// typically in [0, 1], the same range Godot's own Color constructor and
// editor use, though Godot itself doesn't clamp them (values outside that
// range are valid for HDR-style effects).
type Color struct {
	R float64 `json:"r" jsonschema:"the color's red component, typically 0-1"`
	G float64 `json:"g" jsonschema:"the color's green component, typically 0-1"`
	B float64 `json:"b" jsonschema:"the color's blue component, typically 0-1"`
	A float64 `json:"a" jsonschema:"the color's alpha (opacity) component, typically 0-1"`
}

// SetNodePropertyParams are the parameters for the set_node_property
// operation. Exactly one of StringValue, IntValue, FloatValue, BoolValue,
// Vector2Value, Vector3Value, ColorValue, Vector2iValue, Vector3iValue must
// be set — which one determines the GDScript-side type the property is set
// to (see scripts/godot_operations.gd's _op_set_node_property), since JSON
// itself doesn't distinguish int from float the way Go and GDScript both
// do.
//
// This is deliberately scoped to primitives plus Vector2, Vector3, Color,
// Vector2i, and Vector3i for now. Godot node properties also include other
// compound types (resource references, NodePath, arrays, ...); supporting
// those is a separate, larger design (how does an AI client express a
// sub-resource reference as tool arguments?) tracked as a future
// FEATURES.md item, not a variant of this one.
type SetNodePropertyParams struct {
	// ScenePath is the .tscn path, relative to the project root. Validated
	// against Root before use.
	ScenePath string `json:"scene_path" jsonschema:"path to a .tscn file, relative to the project root"`
	// NodePath addresses the target node relative to the scene root, using
	// Godot's own NodePath syntax (e.g. "World/Player"). Empty string means
	// the scene root itself.
	NodePath string `json:"node_path" jsonschema:"path to the target node, relative to the scene root, e.g. \"World/Player\"; empty string means the scene root itself"`
	// PropertyName is the node property to set, e.g. "visible" or "z_index".
	PropertyName string `json:"property_name" jsonschema:"the node property to set, e.g. \"visible\" or \"z_index\""`

	StringValue   *string   `json:"string_value,omitempty" jsonschema:"set property_name to this string value; exactly one of the *_value fields must be set"`
	IntValue      *int64    `json:"int_value,omitempty" jsonschema:"set property_name to this integer value; exactly one of the *_value fields must be set"`
	FloatValue    *float64  `json:"float_value,omitempty" jsonschema:"set property_name to this floating-point value; exactly one of the *_value fields must be set"`
	BoolValue     *bool     `json:"bool_value,omitempty" jsonschema:"set property_name to this boolean value; exactly one of the *_value fields must be set"`
	Vector2Value  *Vector2  `json:"vector2_value,omitempty" jsonschema:"set property_name to this Vector2 value (e.g. 2D position, scale); exactly one of the *_value fields must be set"`
	Vector3Value  *Vector3  `json:"vector3_value,omitempty" jsonschema:"set property_name to this Vector3 value (e.g. 3D position, scale); exactly one of the *_value fields must be set"`
	ColorValue    *Color    `json:"color_value,omitempty" jsonschema:"set property_name to this Color value (e.g. modulate, self_modulate); exactly one of the *_value fields must be set"`
	Vector2iValue *Vector2i `json:"vector2i_value,omitempty" jsonschema:"set property_name to this Vector2i value (e.g. frame_coords, viewport size); exactly one of the *_value fields must be set"`
	Vector3iValue *Vector3i `json:"vector3i_value,omitempty" jsonschema:"set property_name to this Vector3i value (e.g. a custom script's grid/voxel coordinates); exactly one of the *_value fields must be set"`
}

// SetNodePropertyResult confirms a completed property write.
type SetNodePropertyResult struct {
	// Path is the scene's res://-style path.
	Path string `json:"path"`
	// NodePath is echoed back from the request for confirmation.
	NodePath string `json:"node_path"`
	// PropertyName is echoed back from the request for confirmation.
	PropertyName string `json:"property_name"`
	// PreviousValue is Godot's own string representation (str()) of the
	// property's value immediately before this write, for audit purposes.
	// It is not a typed round-trippable value — just a human-readable
	// record of what was overwritten.
	PreviousValue string `json:"previous_value"`
}

// SetNodeProperty loads a .tscn file headlessly, sets a single primitive
// property on one node, and saves the scene back to disk.
//
// Unlike the read-only operations in this package, this genuinely needs
// Godot for more than just decoding: producing a correct .tscn byte stream
// (headers, load_steps, resource references, format version) is Godot's own
// serializer's job, done by instantiating the scene, mutating the live node
// tree, then re-packing and saving it — the same round trip
// scripts/godot_operations.gd already uses for read_scene_tree, just with a
// save at the end. Godot's Object.set() silently no-ops on an unknown
// property name rather than erroring, so the operations script reads the
// property back after setting it and refuses to save if the value didn't
// actually change to the requested one — a mistyped property_name is
// reported as an error here, never written as a no-op.
//
// Because the whole scene is re-packed and re-saved (not patched in place),
// the resulting file is Godot's own current serialization of the entire
// scene, not a minimal diff against what was there before: unrelated
// cosmetic details (e.g. an omitted default load_steps=1, or unique_id
// attributes Godot's own re-pack adds) can change alongside the property
// that was actually requested. This mirrors what happens if a human opened
// the scene in the editor and saved it — it's Godot's own round trip, not a
// bug in this operation — but it does mean a version-control diff after a
// write can be noisier than just the one property line.
func (c *Client) SetNodeProperty(ctx context.Context, params SetNodePropertyParams) (*SetNodePropertyResult, error) {
	absScenePath, err := c.Root.Resolve(params.ScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_node_property: %w", err)
	}
	if filepath.Ext(absScenePath) != ".tscn" {
		return nil, fmt.Errorf("headless: set_node_property: not a .tscn file: %s", params.ScenePath)
	}
	if params.PropertyName == "" {
		return nil, errors.New("headless: set_node_property: property_name is required")
	}

	valuesSet := 0
	for _, set := range []bool{
		params.StringValue != nil,
		params.IntValue != nil,
		params.FloatValue != nil,
		params.BoolValue != nil,
		params.Vector2Value != nil,
		params.Vector3Value != nil,
		params.ColorValue != nil,
		params.Vector2iValue != nil,
		params.Vector3iValue != nil,
	} {
		if set {
			valuesSet++
		}
	}
	if valuesSet != 1 {
		return nil, fmt.Errorf("headless: set_node_property: exactly one of string_value, int_value, float_value, bool_value, vector2_value, vector3_value, color_value, vector2i_value, vector3i_value must be set, got %d", valuesSet)
	}

	relScenePath, err := filepath.Rel(c.Root.String(), absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_node_property: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relScenePath)

	var result struct {
		PreviousValue string `json:"previous_value"`
	}
	if err := c.run(ctx, "set_node_property", struct {
		Path          string    `json:"path"`
		NodePath      string    `json:"node_path"`
		PropertyName  string    `json:"property_name"`
		StringValue   *string   `json:"string_value,omitempty"`
		IntValue      *int64    `json:"int_value,omitempty"`
		FloatValue    *float64  `json:"float_value,omitempty"`
		BoolValue     *bool     `json:"bool_value,omitempty"`
		Vector2Value  *Vector2  `json:"vector2_value,omitempty"`
		Vector3Value  *Vector3  `json:"vector3_value,omitempty"`
		ColorValue    *Color    `json:"color_value,omitempty"`
		Vector2iValue *Vector2i `json:"vector2i_value,omitempty"`
		Vector3iValue *Vector3i `json:"vector3i_value,omitempty"`
	}{
		Path:          resPath,
		NodePath:      params.NodePath,
		PropertyName:  params.PropertyName,
		StringValue:   params.StringValue,
		IntValue:      params.IntValue,
		FloatValue:    params.FloatValue,
		BoolValue:     params.BoolValue,
		Vector2Value:  params.Vector2Value,
		Vector3Value:  params.Vector3Value,
		ColorValue:    params.ColorValue,
		Vector2iValue: params.Vector2iValue,
		Vector3iValue: params.Vector3iValue,
	}, &result); err != nil {
		return nil, fmt.Errorf("headless: set_node_property: %w", err)
	}

	return &SetNodePropertyResult{
		Path:          resPath,
		NodePath:      params.NodePath,
		PropertyName:  params.PropertyName,
		PreviousValue: result.PreviousValue,
	}, nil
}

// run invokes the operations script with a single fixed operation name and
// a structured params payload, and decodes the structured result.
//
// The request is passed to Godot as a single argv element (JSON-encoded),
// never interpolated into a shell string, so there is no command-injection
// surface here regardless of what the params contain.
func (c *Client) run(ctx context.Context, operation string, params, out any) error {
	req := request{Operation: operation, Params: params}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	// #nosec G204 -- argv is fixed (godot --headless --path <root> --script
	// <fixed script> -- --json <payload>); no shell is invoked and no
	// element is built from unsanitized string concatenation.
	cmd := exec.CommandContext(ctx, c.GodotBin,
		"--headless",
		"--path", c.Root.String(),
		"--script", c.OperationsScript,
		"--",
		"--json", string(payload),
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running godot: %w (stderr: %s)", err, stderr.String())
	}

	resp, err := parseResponse(stdout.Bytes())
	if err != nil {
		return fmt.Errorf("decoding godot response: %w (stdout: %s, stderr: %s)", err, stdout.String(), stderr.String())
	}
	if !resp.OK {
		return fmt.Errorf("godot operation failed: %s", resp.Error)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("decoding result: %w", err)
		}
	}
	return nil
}

// parseResponse extracts and decodes the operations script's response from
// a Godot process's captured stdout.
//
// stdout as a whole is not guaranteed to be a single JSON document: the
// Godot engine writes its own startup banner (e.g. "Godot Engine
// v4.7.1.stable... - https://godotengine.org") before any script code runs,
// and future engine versions may add further startup lines. The operations
// script's own contract (scripts/godot_operations.gd) is to print exactly
// one line — the JSON response — immediately before quit(), so the last
// non-blank line of stdout is always that response, regardless of whatever
// Godot itself wrote first.
func parseResponse(stdout []byte) (response, error) {
	line, err := lastNonEmptyLine(stdout)
	if err != nil {
		return response{}, err
	}

	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return response{}, err
	}
	return resp, nil
}

// lastNonEmptyLine returns the last non-blank line of b, trimmed of
// surrounding whitespace.
func lastNonEmptyLine(b []byte) ([]byte, error) {
	lines := bytes.Split(b, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) > 0 {
			return line, nil
		}
	}
	return nil, errors.New("no output line found")
}
