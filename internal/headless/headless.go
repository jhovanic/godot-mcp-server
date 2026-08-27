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
