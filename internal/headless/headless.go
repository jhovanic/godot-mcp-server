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
