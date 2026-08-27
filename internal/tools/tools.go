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

// Deps holds every dependency the tool allowlist needs. Adding a new tool
// tier's dependency here (and threading it through from cmd/) keeps
// construction explicit and centralized, matching the allowlist itself.
type Deps struct {
	SceneTree       SceneTreeReader
	Script          ScriptReader
	ProjectSettings ProjectSettingsReader
	TextResource    TextResourceReader
	Logger          *audit.Logger
}

// RegisterAll registers every tool this server exposes against server. This
// is the only function in the codebase that should call mcp.AddTool.
func RegisterAll(server *mcp.Server, deps Deps) {
	registerReadSceneTree(server, deps)
	registerReadScript(server, deps)
	registerReadProjectSettings(server, deps)
	registerReadTextResource(server, deps)
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
