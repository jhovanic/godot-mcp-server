package headless

import (
	"context"
	"encoding/json"
	"errors"
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
