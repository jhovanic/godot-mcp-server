package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jhovanic/godot-mcp-server/internal/audit"
	"github.com/jhovanic/godot-mcp-server/internal/headless"
	"github.com/jhovanic/godot-mcp-server/internal/tools"
)

// fakeReader is a test double for tools.SceneTreeReader, so tool-wiring and
// audit-logging behavior can be verified without a real Godot binary.
type fakeReader struct {
	gotParams headless.ReadSceneTreeParams
	node      *headless.SceneNode
	err       error
}

func (f *fakeReader) ReadSceneTree(_ context.Context, params headless.ReadSceneTreeParams) (*headless.SceneNode, error) {
	f.gotParams = params
	return f.node, f.err
}

// fakeScriptReader is a test double for tools.ScriptReader.
type fakeScriptReader struct {
	gotParams headless.ReadScriptParams
	contents  *headless.ScriptContents
	err       error
}

func (f *fakeScriptReader) ReadScript(_ context.Context, params headless.ReadScriptParams) (*headless.ScriptContents, error) {
	f.gotParams = params
	return f.contents, f.err
}

// fakeProjectSettingsReader is a test double for tools.ProjectSettingsReader.
type fakeProjectSettingsReader struct {
	settings *headless.ProjectSettings
	err      error
}

func (f *fakeProjectSettingsReader) ReadProjectSettings(_ context.Context, _ headless.ReadProjectSettingsParams) (*headless.ProjectSettings, error) {
	return f.settings, f.err
}

// fakeTextResourceReader is a test double for tools.TextResourceReader.
type fakeTextResourceReader struct {
	gotParams headless.ReadTextResourceParams
	contents  *headless.TextResourceContents
	err       error
}

func (f *fakeTextResourceReader) ReadTextResource(_ context.Context, params headless.ReadTextResourceParams) (*headless.TextResourceContents, error) {
	f.gotParams = params
	return f.contents, f.err
}

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	cs, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestReadSceneTree_Success(t *testing.T) {
	reader := &fakeReader{
		node: &headless.SceneNode{
			Name: "Main",
			Type: "Node2D",
			Children: []headless.SceneNode{
				{Name: "Player", Type: "CharacterBody2D"},
			},
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{SceneTree: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_scene_tree",
		Arguments: map[string]any{"scene_path": "scenes/main.tscn"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if reader.gotParams.ScenePath != "scenes/main.tscn" {
		t.Fatalf("handler did not pass through params, got %+v", reader.gotParams)
	}

	if len(res.Content) != 1 {
		t.Fatalf("want 1 content block, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	var gotNode headless.SceneNode
	if err := json.Unmarshal([]byte(text.Text), &gotNode); err != nil {
		t.Fatalf("result content is not valid JSON: %v (%s)", err, text.Text)
	}
	if gotNode.Name != "Main" || len(gotNode.Children) != 1 {
		t.Fatalf("unexpected scene tree in result: %+v", gotNode)
	}

	// Audit log: exactly one entry, for the read_scene_tree operation, ok.
	logLine := strings.TrimSpace(logBuf.String())
	if logLine == "" {
		t.Fatal("expected an audit log entry, got none")
	}
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Operation != "read_scene_tree" {
		t.Errorf("audit entry operation = %q, want %q", entry.Operation, "read_scene_tree")
	}
	if entry.Outcome != audit.OutcomeOK {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeOK)
	}
	if entry.Tier != "headless" {
		t.Errorf("audit entry tier = %q, want %q", entry.Tier, "headless")
	}
}

func TestReadSceneTree_Error(t *testing.T) {
	wantErr := errors.New("boom: scene not found")
	reader := &fakeReader{err: wantErr}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{SceneTree: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_scene_tree",
		Arguments: map[string]any{"scene_path": "scenes/missing.tscn"},
	})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("want IsError=true for a failed operation, got false: %+v", res.Content)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Outcome != audit.OutcomeError {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeError)
	}
	if entry.Error == "" {
		t.Error("audit entry missing error message")
	}
}

func TestReadScript_Success(t *testing.T) {
	reader := &fakeScriptReader{
		contents: &headless.ScriptContents{
			Path:   "res://scripts/player.gd",
			Source: "extends Node\n",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{Script: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_script",
		Arguments: map[string]any{"script_path": "scripts/player.gd"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if reader.gotParams.ScriptPath != "scripts/player.gd" {
		t.Fatalf("handler did not pass through params, got %+v", reader.gotParams)
	}

	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	var gotContents headless.ScriptContents
	if err := json.Unmarshal([]byte(text.Text), &gotContents); err != nil {
		t.Fatalf("result content is not valid JSON: %v (%s)", err, text.Text)
	}
	if gotContents.Source != "extends Node\n" {
		t.Fatalf("unexpected script contents in result: %+v", gotContents)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Operation != "read_script" {
		t.Errorf("audit entry operation = %q, want %q", entry.Operation, "read_script")
	}
	if entry.Outcome != audit.OutcomeOK {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeOK)
	}
	if entry.Tier != "headless" {
		t.Errorf("audit entry tier = %q, want %q", entry.Tier, "headless")
	}
}

func TestReadScript_Error(t *testing.T) {
	wantErr := errors.New("boom: not a .gd file")
	reader := &fakeScriptReader{err: wantErr}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{Script: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_script",
		Arguments: map[string]any{"script_path": "notes.txt"},
	})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("want IsError=true for a failed operation, got false: %+v", res.Content)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Outcome != audit.OutcomeError {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeError)
	}
}

func TestReadProjectSettings_Success(t *testing.T) {
	reader := &fakeProjectSettingsReader{
		settings: &headless.ProjectSettings{
			Path:   "res://project.godot",
			Source: "config_version=5\n",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{ProjectSettings: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_project_settings"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}

	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	var gotSettings headless.ProjectSettings
	if err := json.Unmarshal([]byte(text.Text), &gotSettings); err != nil {
		t.Fatalf("result content is not valid JSON: %v (%s)", err, text.Text)
	}
	if gotSettings.Source != "config_version=5\n" {
		t.Fatalf("unexpected project settings in result: %+v", gotSettings)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Operation != "read_project_settings" {
		t.Errorf("audit entry operation = %q, want %q", entry.Operation, "read_project_settings")
	}
	if entry.Outcome != audit.OutcomeOK {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeOK)
	}
	if entry.Tier != "headless" {
		t.Errorf("audit entry tier = %q, want %q", entry.Tier, "headless")
	}
}

func TestReadProjectSettings_Error(t *testing.T) {
	wantErr := errors.New("boom: no project.godot")
	reader := &fakeProjectSettingsReader{err: wantErr}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{ProjectSettings: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_project_settings"})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("want IsError=true for a failed operation, got false: %+v", res.Content)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Outcome != audit.OutcomeError {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeError)
	}
}

func TestReadTextResource_Success(t *testing.T) {
	reader := &fakeTextResourceReader{
		contents: &headless.TextResourceContents{
			Path:   "res://materials/red.tres",
			Source: "[gd_resource type=\"StandardMaterial3D\" format=3]\n",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{TextResource: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_text_resource",
		Arguments: map[string]any{"resource_path": "materials/red.tres"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if reader.gotParams.ResourcePath != "materials/red.tres" {
		t.Fatalf("handler did not pass through params, got %+v", reader.gotParams)
	}

	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	var gotContents headless.TextResourceContents
	if err := json.Unmarshal([]byte(text.Text), &gotContents); err != nil {
		t.Fatalf("result content is not valid JSON: %v (%s)", err, text.Text)
	}
	if gotContents.Source != "[gd_resource type=\"StandardMaterial3D\" format=3]\n" {
		t.Fatalf("unexpected resource contents in result: %+v", gotContents)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Operation != "read_text_resource" {
		t.Errorf("audit entry operation = %q, want %q", entry.Operation, "read_text_resource")
	}
	if entry.Outcome != audit.OutcomeOK {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeOK)
	}
	if entry.Tier != "headless" {
		t.Errorf("audit entry tier = %q, want %q", entry.Tier, "headless")
	}
}

func TestReadTextResource_Error(t *testing.T) {
	wantErr := errors.New("boom: .res files are not supported")
	reader := &fakeTextResourceReader{err: wantErr}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{TextResource: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_text_resource",
		Arguments: map[string]any{"resource_path": "packed.res"},
	})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("want IsError=true for a failed operation, got false: %+v", res.Content)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Outcome != audit.OutcomeError {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeError)
	}
}

func TestRegisterAll_NoWriteTools(t *testing.T) {
	// This is a read-only tool set: assert no write-capable tool has
	// slipped into the allowlist. Update this list deliberately when a
	// write tool is intentionally added.
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{
		SceneTree:       &fakeReader{},
		Script:          &fakeScriptReader{},
		ProjectSettings: &fakeProjectSettingsReader{},
		TextResource:    &fakeTextResourceReader{},
		Logger:          audit.New(&bytes.Buffer{}),
	})

	cs := connect(t, server)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	want := map[string]bool{
		"read_scene_tree":       true,
		"read_script":           true,
		"read_project_settings": true,
		"read_text_resource":    true,
	}
	if len(list.Tools) != len(want) {
		t.Fatalf("unexpected tool count %d, want %d: %+v", len(list.Tools), len(want), list.Tools)
	}
	for _, tl := range list.Tools {
		if !want[tl.Name] {
			t.Errorf("unexpected tool registered: %q", tl.Name)
		}
	}
}
