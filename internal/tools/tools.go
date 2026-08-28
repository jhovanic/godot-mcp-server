// Package tools is the single, reviewable location where every MCP tool
// this server exposes is registered. Per CLAUDE.md, tool registration must
// not be scattered across the codebase — this file (and this file alone) is
// the allowlist.
//
// Every tool registered here must be: parameterized and scoped (a typed Go
// struct, not a free-form map), logged via internal/audit on every
// invocation, and read-only until a write tool is deliberately added under
// the same constraints. See CONTRIBUTING.md's "Adding a new tool" section.
package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jhovanic/godot-mcp-server/internal/audit"
	"github.com/jhovanic/godot-mcp-server/internal/headless"
)

// SceneTreeReader is the narrow interface the read_scene_tree tool depends
// on. *headless.Client satisfies it; tests substitute a fake so tool wiring
// and audit logging can be verified without a real Godot binary.
type SceneTreeReader interface {
	ReadSceneTree(ctx context.Context, params headless.ReadSceneTreeParams) (*headless.SceneNode, error)
}

// ScriptReader is the narrow interface the read_script tool depends on.
// *headless.Client satisfies it too, even though ReadScript never actually
// invokes Godot (see its doc comment) — from this package's point of view
// it's still just "the thing that reads project files."
type ScriptReader interface {
	ReadScript(ctx context.Context, params headless.ReadScriptParams) (*headless.ScriptContents, error)
}

// ProjectSettingsReader is the narrow interface the read_project_settings
// tool depends on. Like ScriptReader, *headless.Client satisfies this
// without ever invoking Godot.
type ProjectSettingsReader interface {
	ReadProjectSettings(ctx context.Context, params headless.ReadProjectSettingsParams) (*headless.ProjectSettings, error)
}

// TextResourceReader is the narrow interface the read_text_resource tool
// depends on. Like ScriptReader, *headless.Client satisfies this without
// ever invoking Godot — it's scoped to .tres text resources only (see
// ReadTextResource's doc comment for why .res is out of scope, and for why
// that means a bare "Resource" name would be misleading here).
type TextResourceReader interface {
	ReadTextResource(ctx context.Context, params headless.ReadTextResourceParams) (*headless.TextResourceContents, error)
}

// BinaryResourceReader is the narrow interface the read_binary_resource
// tool depends on. Unlike TextResourceReader, *headless.Client genuinely
// invokes Godot to satisfy this (see ReadBinaryResource's doc comment).
type BinaryResourceReader interface {
	ReadBinaryResource(ctx context.Context, params headless.ReadBinaryResourceParams) (*headless.BinaryResourceContents, error)
}

// ImportSettingsReader is the narrow interface the read_import_settings
// tool depends on. Like ScriptReader, *headless.Client satisfies this
// without ever invoking Godot.
type ImportSettingsReader interface {
	ReadImportSettings(ctx context.Context, params headless.ReadImportSettingsParams) (*headless.ImportSettings, error)
}

// Mode selects which tools RegisterAll exposes. ModeReadOnly (including the
// zero value, so an unset Mode fails safe rather than fails open) is the
// default: nothing but the fixed read tools is ever registered unless a
// caller explicitly opts into ModeReadWrite.
type Mode string

const (
	// ModeReadOnly registers only read tools. This is what the zero value
	// of Mode behaves as, so a Deps built without setting Mode explicitly
	// is still safe.
	ModeReadOnly Mode = "read-only"
	// ModeReadWrite additionally registers write tools. cmd/ is expected to
	// require an explicit, validated opt-in (e.g. a -mode flag) before ever
	// constructing a Deps with this value — see main.go's parseFlags.
	ModeReadWrite Mode = "read-write"
)

// NodePropertySetter is the narrow interface the set_node_property tool
// depends on. *headless.Client satisfies it by genuinely invoking Godot
// (see SetNodeProperty's doc comment) — unlike every read tool's interface
// above, this is a write tool and is only ever registered under
// ModeReadWrite.
type NodePropertySetter interface {
	SetNodeProperty(ctx context.Context, params headless.SetNodePropertyParams) (*headless.SetNodePropertyResult, error)
}

// Deps holds every dependency the tool allowlist needs. Adding a new tool
// tier's dependency here (and threading it through from cmd/) keeps
// construction explicit and centralized, matching the allowlist itself.
type Deps struct {
	SceneTree       SceneTreeReader
	Script          ScriptReader
	ProjectSettings ProjectSettingsReader
	TextResource    TextResourceReader
	BinaryResource  BinaryResourceReader
	ImportSettings  ImportSettingsReader
	NodeProperty    NodePropertySetter
	Mode            Mode
	Logger          *audit.Logger
}

// RegisterAll registers every tool this server exposes against server. This
// is the only function in the codebase that should call mcp.AddTool.
//
// Read tools are always registered. Write tools are gated behind
// deps.Mode == ModeReadWrite: in ModeReadOnly (the default, including the
// zero value of Mode), a write tool is never advertised to the MCP client
// at all — not merely rejected if called. An AI client that can't see a
// write tool can't be prompted or tricked into calling it, which is a
// stronger boundary than a runtime check inside the tool handler.
func RegisterAll(server *mcp.Server, deps Deps) {
	registerReadSceneTree(server, deps)
	registerReadScript(server, deps)
	registerReadProjectSettings(server, deps)
	registerReadTextResource(server, deps)
	registerReadBinaryResource(server, deps)
	registerReadImportSettings(server, deps)

	if deps.Mode == ModeReadWrite {
		registerSetNodeProperty(server, deps)
	}
}

func registerReadSceneTree(server *mcp.Server, deps Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_scene_tree",
		Description: "Read-only. Parses a .tscn file under the configured project root and " +
			"returns its node tree (name, type, children). Does not modify the scene file or " +
			"any other project state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args headless.ReadSceneTreeParams) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		node, err := deps.SceneTree.ReadSceneTree(ctx, args)
		deps.Logger.LogResult("headless", "read_scene_tree", args, node, err, start)
		if err != nil {
			// Returning a plain error here is enough: AddTool's handler
			// wrapper turns it into a CallToolResult with IsError=true,
			// per the go-sdk's documented behavior.
			return nil, nil, err
		}

		text, err := json.MarshalIndent(node, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, node, nil
	})
}

func registerReadScript(server *mcp.Server, deps Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_script",
		Description: "Read-only. Returns the raw source text of a .gd script file under the " +
			"configured project root. Does not modify the script file or any other project " +
			"state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args headless.ReadScriptParams) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		contents, err := deps.Script.ReadScript(ctx, args)
		deps.Logger.LogResult("headless", "read_script", args, contents, err, start)
		if err != nil {
			return nil, nil, err
		}

		text, err := json.MarshalIndent(contents, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, contents, nil
	})
}

func registerReadProjectSettings(server *mcp.Server, deps Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_project_settings",
		Description: "Read-only. Returns the raw text of the project's project.godot file. " +
			"Takes no parameters — every Godot project has exactly one. Does not modify " +
			"project.godot or any other project state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args headless.ReadProjectSettingsParams) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		settings, err := deps.ProjectSettings.ReadProjectSettings(ctx, args)
		deps.Logger.LogResult("headless", "read_project_settings", args, settings, err, start)
		if err != nil {
			return nil, nil, err
		}

		text, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, settings, nil
	})
}

func registerReadTextResource(server *mcp.Server, deps Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_text_resource",
		Description: "Read-only. Returns the raw text of a .tres text resource file under the " +
			"configured project root (materials, themes, and similar Godot resources saved in " +
			"text format). Binary .res resources are not supported. Does not modify the " +
			"resource file or any other project state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args headless.ReadTextResourceParams) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		contents, err := deps.TextResource.ReadTextResource(ctx, args)
		deps.Logger.LogResult("headless", "read_text_resource", args, contents, err, start)
		if err != nil {
			return nil, nil, err
		}

		text, err := json.MarshalIndent(contents, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, contents, nil
	})
}

func registerReadImportSettings(server *mcp.Server, deps Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_import_settings",
		Description: "Read-only. Returns the raw text of an imported asset's <path>.import " +
			"sidecar file (import settings such as compression, mipmaps, or filters) under the " +
			"configured project root. Takes the asset's own path, not the .import file's path. " +
			"Fails if the asset hasn't been imported (project never opened in the editor, or " +
			"not an importable type). Does not modify the .import file, the asset, or any " +
			"other project state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args headless.ReadImportSettingsParams) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		settings, err := deps.ImportSettings.ReadImportSettings(ctx, args)
		deps.Logger.LogResult("headless", "read_import_settings", args, settings, err, start)
		if err != nil {
			return nil, nil, err
		}

		text, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, settings, nil
	})
}

func registerSetNodeProperty(server *mcp.Server, deps Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "set_node_property",
		Description: "Write. Sets a single property (string, integer, floating-point, boolean, " +
			"Vector2, Vector3, Color, Vector2i, Vector3i, Quaternion, Rect2, Rect2i, Plane, " +
			"AABB, Basis, Transform2D, Transform3D, NodePath, a PackedStringArray, a " +
			"PackedInt32Array, a PackedFloat32Array, a PackedVector2Array, a PackedColorArray, " +
			"a PackedVector3Array, or an Array[NodePath] — exactly one of string_value/" +
			"int_value/float_value/bool_value/vector2_value/vector3_value/color_value/" +
			"vector2i_value/vector3i_value/quaternion_value/rect2_value/rect2i_value/" +
			"plane_value/aabb_value/basis_value/transform2d_value/transform3d_value/" +
			"node_path_value/string_array_value/int_array_value/float_array_value/" +
			"vector2_array_value/color_array_value/vector3_array_value/node_path_array_value " +
			"must be given) on one node in a .tscn scene under the configured project root, " +
			"then saves the scene. Fails, without modifying the scene, if the scene, node, or " +
			"property doesn't exist. Only ever available when the server was started with " +
			"-mode read-write.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args headless.SetNodePropertyParams) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		result, err := deps.NodeProperty.SetNodeProperty(ctx, args)
		deps.Logger.LogResult("headless", "set_node_property", args, result, err, start)
		if err != nil {
			return nil, nil, err
		}

		text, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, result, nil
	})
}

func registerReadBinaryResource(server *mcp.Server, deps Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_binary_resource",
		Description: "Read-only. Decodes a .res binary resource file under the configured " +
			"project root by loading it in Godot and re-serializing it through Godot's own " +
			".tres text format, then returns that text. Does not modify the .res file or any " +
			"other project state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args headless.ReadBinaryResourceParams) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		contents, err := deps.BinaryResource.ReadBinaryResource(ctx, args)
		deps.Logger.LogResult("headless", "read_binary_resource", args, contents, err, start)
		if err != nil {
			return nil, nil, err
		}

		text, err := json.MarshalIndent(contents, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, contents, nil
	})
}
