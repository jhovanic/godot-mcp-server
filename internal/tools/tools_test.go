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

// fakeBinaryResourceReader is a test double for tools.BinaryResourceReader.
type fakeBinaryResourceReader struct {
	gotParams headless.ReadBinaryResourceParams
	contents  *headless.BinaryResourceContents
	err       error
}

func (f *fakeBinaryResourceReader) ReadBinaryResource(_ context.Context, params headless.ReadBinaryResourceParams) (*headless.BinaryResourceContents, error) {
	f.gotParams = params
	return f.contents, f.err
}

// fakeImportSettingsReader is a test double for tools.ImportSettingsReader.
type fakeImportSettingsReader struct {
	gotParams headless.ReadImportSettingsParams
	settings  *headless.ImportSettings
	err       error
}

func (f *fakeImportSettingsReader) ReadImportSettings(_ context.Context, params headless.ReadImportSettingsParams) (*headless.ImportSettings, error) {
	f.gotParams = params
	return f.settings, f.err
}

// fakeNodePropertySetter is a test double for tools.NodePropertySetter.
type fakeNodePropertySetter struct {
	gotParams headless.SetNodePropertyParams
	result    *headless.SetNodePropertyResult
	err       error
}

func (f *fakeNodePropertySetter) SetNodeProperty(_ context.Context, params headless.SetNodePropertyParams) (*headless.SetNodePropertyResult, error) {
	f.gotParams = params
	return f.result, f.err
}

// fakeScriptExportSetter is a test double for tools.ScriptExportSetter.
type fakeScriptExportSetter struct {
	gotParams headless.SetScriptExportParams
	result    *headless.SetScriptExportResult
	err       error
}

func (f *fakeScriptExportSetter) SetScriptExport(_ context.Context, params headless.SetScriptExportParams) (*headless.SetScriptExportResult, error) {
	f.gotParams = params
	return f.result, f.err
}

// fakeFunctionBodySetter is a test double for tools.FunctionBodySetter.
type fakeFunctionBodySetter struct {
	gotParams headless.SetFunctionBodyParams
	result    *headless.SetFunctionBodyResult
	err       error
}

func (f *fakeFunctionBodySetter) SetFunctionBody(_ context.Context, params headless.SetFunctionBodyParams) (*headless.SetFunctionBodyResult, error) {
	f.gotParams = params
	return f.result, f.err
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

func TestReadBinaryResource_Success(t *testing.T) {
	reader := &fakeBinaryResourceReader{
		contents: &headless.BinaryResourceContents{
			Path:   "res://materials/red.res",
			Source: "[gd_resource type=\"StandardMaterial3D\" format=3]\n",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{BinaryResource: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_binary_resource",
		Arguments: map[string]any{"resource_path": "materials/red.res"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if reader.gotParams.ResourcePath != "materials/red.res" {
		t.Fatalf("handler did not pass through params, got %+v", reader.gotParams)
	}

	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	var gotContents headless.BinaryResourceContents
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
	if entry.Operation != "read_binary_resource" {
		t.Errorf("audit entry operation = %q, want %q", entry.Operation, "read_binary_resource")
	}
	if entry.Outcome != audit.OutcomeOK {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeOK)
	}
	if entry.Tier != "headless" {
		t.Errorf("audit entry tier = %q, want %q", entry.Tier, "headless")
	}
}

func TestReadBinaryResource_Error(t *testing.T) {
	wantErr := errors.New("boom: failed to load resource")
	reader := &fakeBinaryResourceReader{err: wantErr}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{BinaryResource: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_binary_resource",
		Arguments: map[string]any{"resource_path": "materials/missing.res"},
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

func TestReadImportSettings_Success(t *testing.T) {
	reader := &fakeImportSettingsReader{
		settings: &headless.ImportSettings{
			Path:   "res://icon.png.import",
			Source: "[remap]\n\nimporter=\"texture\"\n",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{ImportSettings: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_import_settings",
		Arguments: map[string]any{"asset_path": "icon.png"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if reader.gotParams.AssetPath != "icon.png" {
		t.Fatalf("handler did not pass through params, got %+v", reader.gotParams)
	}

	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	var gotSettings headless.ImportSettings
	if err := json.Unmarshal([]byte(text.Text), &gotSettings); err != nil {
		t.Fatalf("result content is not valid JSON: %v (%s)", err, text.Text)
	}
	if gotSettings.Source != "[remap]\n\nimporter=\"texture\"\n" {
		t.Fatalf("unexpected import settings in result: %+v", gotSettings)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Operation != "read_import_settings" {
		t.Errorf("audit entry operation = %q, want %q", entry.Operation, "read_import_settings")
	}
	if entry.Outcome != audit.OutcomeOK {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeOK)
	}
	if entry.Tier != "headless" {
		t.Errorf("audit entry tier = %q, want %q", entry.Tier, "headless")
	}
}

func TestReadImportSettings_Error(t *testing.T) {
	wantErr := errors.New("boom: no .import sidecar")
	reader := &fakeImportSettingsReader{err: wantErr}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, tools.Deps{ImportSettings: reader, Logger: logger})

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_import_settings",
		Arguments: map[string]any{"asset_path": "not_imported.png"},
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

var readToolNames = map[string]bool{
	"read_scene_tree":       true,
	"read_script":           true,
	"read_project_settings": true,
	"read_text_resource":    true,
	"read_binary_resource":  true,
	"read_import_settings":  true,
}

func fullDeps(logger *audit.Logger, mode tools.Mode) tools.Deps {
	return tools.Deps{
		SceneTree:       &fakeReader{},
		Script:          &fakeScriptReader{},
		ProjectSettings: &fakeProjectSettingsReader{},
		TextResource:    &fakeTextResourceReader{},
		BinaryResource:  &fakeBinaryResourceReader{},
		ImportSettings:  &fakeImportSettingsReader{},
		NodeProperty:    &fakeNodePropertySetter{},
		ScriptExport:    &fakeScriptExportSetter{},
		FunctionBody:    &fakeFunctionBodySetter{},
		Mode:            mode,
		Logger:          logger,
	}
}

// readWriteToolNames is the write-tool set ModeReadWrite adds on top of
// readToolNames — a strict subset of what ModeAdvanced adds, since
// ModeAdvanced is a superset of ModeReadWrite.
var readWriteToolNames = map[string]bool{
	"set_node_property": true,
	"set_script_export": true,
}

// TestRegisterAll_ModeGatesWriteTools asserts the write tool set advertised
// to the MCP client tracks deps.Mode exactly: only the read tools in
// ModeReadOnly (including the zero value of Mode, so an unset Mode fails
// safe rather than fails open), plus set_node_property and
// set_script_export in ModeReadWrite, plus set_function_body in
// ModeAdvanced (a strict superset of ModeReadWrite's own tools). Update the
// write-tool lists deliberately when the next write tool is intentionally
// added.
func TestRegisterAll_ModeGatesWriteTools(t *testing.T) {
	cases := []struct {
		name string
		mode tools.Mode
		want map[string]bool
	}{
		{name: "zero value", mode: "", want: readToolNames},
		{name: "read-only", mode: tools.ModeReadOnly, want: readToolNames},
		{name: "read-write", mode: tools.ModeReadWrite, want: union(readToolNames, readWriteToolNames)},
		{name: "advanced", mode: tools.ModeAdvanced, want: union(readToolNames, union(readWriteToolNames, map[string]bool{"set_function_body": true}))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
			tools.RegisterAll(server, fullDeps(audit.New(&bytes.Buffer{}), tc.mode))

			cs := connect(t, server)
			list, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}

			if len(list.Tools) != len(tc.want) {
				t.Fatalf("unexpected tool count %d, want %d: %+v", len(list.Tools), len(tc.want), list.Tools)
			}
			for _, tl := range list.Tools {
				if !tc.want[tl.Name] {
					t.Errorf("unexpected tool registered: %q", tl.Name)
				}
			}
		})
	}
}

func union(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestSetNodeProperty_Success(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Label",
			PropertyName:  "text",
			PreviousValue: "old text",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "Label",
			"property_name": "text",
			"string_value":  "hello",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if setter.gotParams.ScenePath != "main.tscn" || setter.gotParams.NodePath != "Label" || setter.gotParams.PropertyName != "text" {
		t.Fatalf("handler did not pass through params, got %+v", setter.gotParams)
	}
	if setter.gotParams.StringValue == nil || *setter.gotParams.StringValue != "hello" {
		t.Fatalf("handler did not pass through string_value, got %+v", setter.gotParams.StringValue)
	}

	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	var gotResult headless.SetNodePropertyResult
	if err := json.Unmarshal([]byte(text.Text), &gotResult); err != nil {
		t.Fatalf("result content is not valid JSON: %v (%s)", err, text.Text)
	}
	if gotResult.PropertyName != "text" {
		t.Fatalf("unexpected result: %+v", gotResult)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Operation != "set_node_property" {
		t.Errorf("audit entry operation = %q, want %q", entry.Operation, "set_node_property")
	}
	if entry.Outcome != audit.OutcomeOK {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeOK)
	}
	if entry.Tier != "headless" {
		t.Errorf("audit entry tier = %q, want %q", entry.Tier, "headless")
	}
}

func TestSetNodeProperty_Vector2Success(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "",
			PropertyName:  "position",
			PreviousValue: "(0, 0)",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "",
			"property_name": "position",
			"vector2_value": map[string]any{"x": 1.5, "y": -2.5},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if setter.gotParams.Vector2Value == nil || setter.gotParams.Vector2Value.X != 1.5 || setter.gotParams.Vector2Value.Y != -2.5 {
		t.Fatalf("handler did not pass through vector2_value, got %+v", setter.gotParams.Vector2Value)
	}
}

func TestSetNodeProperty_ColorSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "",
			PropertyName:  "modulate",
			PreviousValue: "(1, 1, 1, 1)",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "",
			"property_name": "modulate",
			"color_value":   map[string]any{"r": 0.5, "g": 0.25, "b": 0.75, "a": 1.0},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.ColorValue
	if got == nil || got.R != 0.5 || got.G != 0.25 || got.B != 0.75 || got.A != 1.0 {
		t.Fatalf("handler did not pass through color_value, got %+v", got)
	}
}

func TestSetNodeProperty_Vector3Success(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Cube",
			PropertyName:  "position",
			PreviousValue: "(0, 0, 0)",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "Cube",
			"property_name": "position",
			"vector3_value": map[string]any{"x": 1.5, "y": -2.5, "z": 3.5},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.Vector3Value
	if got == nil || got.X != 1.5 || got.Y != -2.5 || got.Z != 3.5 {
		t.Fatalf("handler did not pass through vector3_value, got %+v", got)
	}
}

func TestSetNodeProperty_Vector2iSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Sprite",
			PropertyName:  "frame_coords",
			PreviousValue: "(0, 0)",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":     "main.tscn",
			"node_path":      "Sprite",
			"property_name":  "frame_coords",
			"vector2i_value": map[string]any{"x": 2, "y": 3},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.Vector2iValue
	if got == nil || got.X != 2 || got.Y != 3 {
		t.Fatalf("handler did not pass through vector2i_value, got %+v", got)
	}
}

func TestSetNodeProperty_Vector3iSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "IntGrid",
			PropertyName:  "grid_position",
			PreviousValue: "(0, 0, 0)",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":     "main.tscn",
			"node_path":      "IntGrid",
			"property_name":  "grid_position",
			"vector3i_value": map[string]any{"x": 2, "y": 3, "z": 4},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.Vector3iValue
	if got == nil || got.X != 2 || got.Y != 3 || got.Z != 4 {
		t.Fatalf("handler did not pass through vector3i_value, got %+v", got)
	}
}

func TestSetNodeProperty_QuaternionSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Cube",
			PropertyName:  "quaternion",
			PreviousValue: "(0, 0, 0, 1)",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":       "main.tscn",
			"node_path":        "Cube",
			"property_name":    "quaternion",
			"quaternion_value": map[string]any{"x": 0, "y": 0.7071067811865476, "z": 0, "w": 0.7071067811865476},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.QuaternionValue
	if got == nil || got.X != 0 || got.Y != 0.7071067811865476 || got.Z != 0 || got.W != 0.7071067811865476 {
		t.Fatalf("handler did not pass through quaternion_value, got %+v", got)
	}
}

func TestSetNodeProperty_Rect2Success(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Sprite",
			PropertyName:  "region_rect",
			PreviousValue: "[P: (0, 0), S: (0, 0)]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "Sprite",
			"property_name": "region_rect",
			"rect2_value": map[string]any{
				"position": map[string]any{"x": 1.5, "y": 2.5},
				"size":     map[string]any{"x": 10, "y": 20},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.Rect2Value
	if got == nil || got.Position.X != 1.5 || got.Position.Y != 2.5 || got.Size.X != 10 || got.Size.Y != 20 {
		t.Fatalf("handler did not pass through rect2_value, got %+v", got)
	}
}

func TestSetNodeProperty_Rect2iSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Win",
			PropertyName:  "nonclient_area",
			PreviousValue: "[P: (0, 0), S: (0, 0)]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "Win",
			"property_name": "nonclient_area",
			"rect2i_value": map[string]any{
				"position": map[string]any{"x": 1, "y": 2},
				"size":     map[string]any{"x": 3, "y": 4},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.Rect2iValue
	if got == nil || got.Position.X != 1 || got.Position.Y != 2 || got.Size.X != 3 || got.Size.Y != 4 {
		t.Fatalf("handler did not pass through rect2i_value, got %+v", got)
	}
}

func TestSetNodeProperty_PlaneSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "IntGrid",
			PropertyName:  "boundary_plane",
			PreviousValue: "(0, 1, 0, 0)",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "IntGrid",
			"property_name": "boundary_plane",
			"plane_value":   map[string]any{"x": 0, "y": 0, "z": 1, "d": 5},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.PlaneValue
	if got == nil || got.X != 0 || got.Y != 0 || got.Z != 1 || got.D != 5 {
		t.Fatalf("handler did not pass through plane_value, got %+v", got)
	}
}

func TestSetNodeProperty_AABBSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Notifier",
			PropertyName:  "aabb",
			PreviousValue: "[P: (0, 0, 0), S: (0, 0, 0)]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "Notifier",
			"property_name": "aabb",
			"aabb_value": map[string]any{
				"position": map[string]any{"x": 1, "y": 2, "z": 3},
				"size":     map[string]any{"x": 4, "y": 5, "z": 6},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.AABBValue
	if got == nil || got.Position.X != 1 || got.Position.Y != 2 || got.Position.Z != 3 || got.Size.X != 4 || got.Size.Y != 5 || got.Size.Z != 6 {
		t.Fatalf("handler did not pass through aabb_value, got %+v", got)
	}
}

func TestSetNodeProperty_BasisSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Cube",
			PropertyName:  "basis",
			PreviousValue: "[X: (1, 0, 0), Y: (0, 1, 0), Z: (0, 0, 1)]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "Cube",
			"property_name": "basis",
			"basis_value": map[string]any{
				"x": map[string]any{"x": 2, "y": 0, "z": 0},
				"y": map[string]any{"x": 0, "y": 3, "z": 0},
				"z": map[string]any{"x": 0, "y": 0, "z": 4},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.BasisValue
	if got == nil || got.X.X != 2 || got.Y.Y != 3 || got.Z.Z != 4 {
		t.Fatalf("handler did not pass through basis_value, got %+v", got)
	}
}

func TestSetNodeProperty_Transform2DSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "",
			PropertyName:  "transform",
			PreviousValue: "[X: (1, 0), Y: (0, 1), O: (0, 0)]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "",
			"property_name": "transform",
			"transform2d_value": map[string]any{
				"x":      map[string]any{"x": 1, "y": 0},
				"y":      map[string]any{"x": 0, "y": 1},
				"origin": map[string]any{"x": 10, "y": 20},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.Transform2DValue
	if got == nil || got.Origin.X != 10 || got.Origin.Y != 20 || got.X.X != 1 || got.Y.Y != 1 {
		t.Fatalf("handler did not pass through transform2d_value, got %+v", got)
	}
}

func TestSetNodeProperty_Transform3DSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Cube",
			PropertyName:  "transform",
			PreviousValue: "[X: (1, 0, 0), Y: (0, 1, 0), Z: (0, 0, 1), O: (0, 0, 0)]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "Cube",
			"property_name": "transform",
			"transform3d_value": map[string]any{
				"basis": map[string]any{
					"x": map[string]any{"x": 2, "y": 0, "z": 0},
					"y": map[string]any{"x": 0, "y": 3, "z": 0},
					"z": map[string]any{"x": 0, "y": 0, "z": 4},
				},
				"origin": map[string]any{"x": 10, "y": 20, "z": 30},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.Transform3DValue
	if got == nil || got.Origin.X != 10 || got.Origin.Y != 20 || got.Origin.Z != 30 || got.Basis.X.X != 2 || got.Basis.Y.Y != 3 || got.Basis.Z.Z != 4 {
		t.Fatalf("handler did not pass through transform3d_value, got %+v", got)
	}
}

func TestSetNodeProperty_NodePathSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Remote",
			PropertyName:  "remote_path",
			PreviousValue: "",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":      "main.tscn",
			"node_path":       "Remote",
			"property_name":   "remote_path",
			"node_path_value": "../Target",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if setter.gotParams.NodePathValue == nil || *setter.gotParams.NodePathValue != "../Target" {
		t.Fatalf("handler did not pass through node_path_value, got %+v", setter.gotParams.NodePathValue)
	}
}

func TestSetNodeProperty_StringArraySuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Spawner",
			PropertyName:  "_spawnable_scenes",
			PreviousValue: "[]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":         "main.tscn",
			"node_path":          "Spawner",
			"property_name":      "_spawnable_scenes",
			"string_array_value": []any{"res://a.tscn", "res://b.tscn"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.StringArrayValue
	if len(got) != 2 || got[0] != "res://a.tscn" || got[1] != "res://b.tscn" {
		t.Fatalf("handler did not pass through string_array_value, got %+v", got)
	}
}

func TestSetNodeProperty_IntArraySuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Split",
			PropertyName:  "split_offsets",
			PreviousValue: "[]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":      "main.tscn",
			"node_path":       "Split",
			"property_name":   "split_offsets",
			"int_array_value": []any{10, -20, 30},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.IntArrayValue
	if len(got) != 3 || got[0] != 10 || got[1] != -20 || got[2] != 30 {
		t.Fatalf("handler did not pass through int_array_value, got %+v", got)
	}
}

func TestSetNodeProperty_FloatArraySuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Label",
			PropertyName:  "tab_stops",
			PreviousValue: "[]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":        "main.tscn",
			"node_path":         "Label",
			"property_name":     "tab_stops",
			"float_array_value": []any{1.5, 2.5, -3.5},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.FloatArrayValue
	if len(got) != 3 || got[0] != 1.5 || got[1] != 2.5 || got[2] != -3.5 {
		t.Fatalf("handler did not pass through float_array_value, got %+v", got)
	}
}

func TestSetNodeProperty_Vector2ArraySuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Poly",
			PropertyName:  "polygon",
			PreviousValue: "[]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "Poly",
			"property_name": "polygon",
			"vector2_array_value": []any{
				map[string]any{"x": 1.5, "y": 2.5},
				map[string]any{"x": -3, "y": 4},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.Vector2ArrayValue
	if len(got) != 2 || got[0].X != 1.5 || got[0].Y != 2.5 || got[1].X != -3 || got[1].Y != 4 {
		t.Fatalf("handler did not pass through vector2_array_value, got %+v", got)
	}
}

func TestSetNodeProperty_ColorArraySuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "ColorPoly",
			PropertyName:  "vertex_colors",
			PreviousValue: "[]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "ColorPoly",
			"property_name": "vertex_colors",
			"color_array_value": []any{
				map[string]any{"r": 1, "g": 0, "b": 0, "a": 1},
				map[string]any{"r": 0, "g": 1, "b": 0, "a": 0.5},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.ColorArrayValue
	if len(got) != 2 || got[0].R != 1 || got[0].G != 0 || got[1].G != 1 || got[1].A != 0.5 {
		t.Fatalf("handler did not pass through color_array_value, got %+v", got)
	}
}

func TestSetNodeProperty_Vector3ArraySuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Obs",
			PropertyName:  "vertices",
			PreviousValue: "[]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "Obs",
			"property_name": "vertices",
			"vector3_array_value": []any{
				map[string]any{"x": 1.5, "y": 2.5, "z": 3.5},
				map[string]any{"x": -1, "y": -2, "z": -3},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.Vector3ArrayValue
	if len(got) != 2 || got[0].X != 1.5 || got[0].Z != 3.5 || got[1].X != -1 || got[1].Z != -3 {
		t.Fatalf("handler did not pass through vector3_array_value, got %+v", got)
	}
}

func TestSetNodeProperty_NodePathArraySuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Ctrl",
			PropertyName:  "accessibility_flow_to_nodes",
			PreviousValue: "[]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":            "main.tscn",
			"node_path":             "Ctrl",
			"property_name":         "accessibility_flow_to_nodes",
			"node_path_array_value": []any{"../A", "../B"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.NodePathArrayValue
	if len(got) != 2 || got[0] != "../A" || got[1] != "../B" {
		t.Fatalf("handler did not pass through node_path_array_value, got %+v", got)
	}
}

func TestSetNodeProperty_ResourceValueSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "Sprite",
			PropertyName:  "texture",
			PreviousValue: "<null>",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":     "main.tscn",
			"node_path":      "Sprite",
			"property_name":  "texture",
			"resource_value": "placeholder.tres",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.ResourceValue
	if got == nil || *got != "placeholder.tres" {
		t.Fatalf("handler did not pass through resource_value, got %+v", got)
	}
}

// TestSetNodeProperty_TypedArrayValuesSuccess checks that all six
// Typed*ArrayValue fields make it from the MCP request arguments through to
// headless.SetNodePropertyParams unchanged, table-driven since the shape of
// the check is identical across all six element types.
func TestSetNodeProperty_TypedArrayValuesSuccess(t *testing.T) {
	tests := []struct {
		name     string
		argKey   string
		argValue any
		check    func(t *testing.T, got headless.SetNodePropertyParams)
	}{
		{
			name:     "TypedStringArrayValue",
			argKey:   "typed_string_array_value",
			argValue: []any{"a", "b"},
			check: func(t *testing.T, got headless.SetNodePropertyParams) {
				if len(got.TypedStringArrayValue) != 2 || got.TypedStringArrayValue[0] != "a" || got.TypedStringArrayValue[1] != "b" {
					t.Fatalf("handler did not pass through typed_string_array_value, got %+v", got.TypedStringArrayValue)
				}
			},
		},
		{
			name:     "TypedIntArrayValue",
			argKey:   "typed_int_array_value",
			argValue: []any{1, 2},
			check: func(t *testing.T, got headless.SetNodePropertyParams) {
				if len(got.TypedIntArrayValue) != 2 || got.TypedIntArrayValue[0] != 1 || got.TypedIntArrayValue[1] != 2 {
					t.Fatalf("handler did not pass through typed_int_array_value, got %+v", got.TypedIntArrayValue)
				}
			},
		},
		{
			name:     "TypedFloatArrayValue",
			argKey:   "typed_float_array_value",
			argValue: []any{1.5, 2.5},
			check: func(t *testing.T, got headless.SetNodePropertyParams) {
				if len(got.TypedFloatArrayValue) != 2 || got.TypedFloatArrayValue[0] != 1.5 || got.TypedFloatArrayValue[1] != 2.5 {
					t.Fatalf("handler did not pass through typed_float_array_value, got %+v", got.TypedFloatArrayValue)
				}
			},
		},
		{
			name:     "TypedVector2ArrayValue",
			argKey:   "typed_vector2_array_value",
			argValue: []any{map[string]any{"x": 1.0, "y": 2.0}},
			check: func(t *testing.T, got headless.SetNodePropertyParams) {
				if len(got.TypedVector2ArrayValue) != 1 || got.TypedVector2ArrayValue[0] != (headless.Vector2{X: 1, Y: 2}) {
					t.Fatalf("handler did not pass through typed_vector2_array_value, got %+v", got.TypedVector2ArrayValue)
				}
			},
		},
		{
			name:     "TypedColorArrayValue",
			argKey:   "typed_color_array_value",
			argValue: []any{map[string]any{"r": 1.0, "g": 0.0, "b": 0.0, "a": 1.0}},
			check: func(t *testing.T, got headless.SetNodePropertyParams) {
				if len(got.TypedColorArrayValue) != 1 || got.TypedColorArrayValue[0] != (headless.Color{R: 1, G: 0, B: 0, A: 1}) {
					t.Fatalf("handler did not pass through typed_color_array_value, got %+v", got.TypedColorArrayValue)
				}
			},
		},
		{
			name:     "TypedVector3ArrayValue",
			argKey:   "typed_vector3_array_value",
			argValue: []any{map[string]any{"x": 1.0, "y": 2.0, "z": 3.0}},
			check: func(t *testing.T, got headless.SetNodePropertyParams) {
				if len(got.TypedVector3ArrayValue) != 1 || got.TypedVector3ArrayValue[0] != (headless.Vector3{X: 1, Y: 2, Z: 3}) {
					t.Fatalf("handler did not pass through typed_vector3_array_value, got %+v", got.TypedVector3ArrayValue)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setter := &fakeNodePropertySetter{
				result: &headless.SetNodePropertyResult{
					Path:          "res://main.tscn",
					NodePath:      "IntGrid",
					PropertyName:  "some_property",
					PreviousValue: "[]",
				},
			}
			var logBuf bytes.Buffer
			logger := audit.New(&logBuf)

			deps := fullDeps(logger, tools.ModeReadWrite)
			deps.NodeProperty = setter
			server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
			tools.RegisterAll(server, deps)

			cs := connect(t, server)
			ctx := context.Background()

			res, err := cs.CallTool(ctx, &mcp.CallToolParams{
				Name: "set_node_property",
				Arguments: map[string]any{
					"scene_path":    "main.tscn",
					"node_path":     "IntGrid",
					"property_name": "some_property",
					tt.argKey:       tt.argValue,
				},
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if res.IsError {
				t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
			}
			tt.check(t, setter.gotParams)
		})
	}
}

func TestSetNodeProperty_TypedResourceArrayValueSuccess(t *testing.T) {
	setter := &fakeNodePropertySetter{
		result: &headless.SetNodePropertyResult{
			Path:          "res://main.tscn",
			NodePath:      "IntGrid",
			PropertyName:  "typed_textures",
			PreviousValue: "[]",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":                 "main.tscn",
			"node_path":                  "IntGrid",
			"property_name":              "typed_textures",
			"typed_resource_array_value": []any{"a.tres", "b.tres"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	got := setter.gotParams.TypedResourceArrayValue
	if len(got) != 2 || got[0] != "a.tres" || got[1] != "b.tres" {
		t.Fatalf("handler did not pass through typed_resource_array_value, got %+v", got)
	}
}

func TestSetNodeProperty_Error(t *testing.T) {
	wantErr := errors.New("boom: no node at Label")
	setter := &fakeNodePropertySetter{err: wantErr}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.NodeProperty = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_node_property",
		Arguments: map[string]any{
			"scene_path":    "main.tscn",
			"node_path":     "Label",
			"property_name": "text",
			"string_value":  "hello",
		},
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

func TestSetScriptExport_Success(t *testing.T) {
	setter := &fakeScriptExportSetter{
		result: &headless.SetScriptExportResult{
			Path:   "res://player.gd",
			Name:   "health",
			Action: "inserted",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.ScriptExport = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_script_export",
		Arguments: map[string]any{
			"script_path": "player.gd",
			"name":        "health",
			"int_value":   100,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if setter.gotParams.ScriptPath != "player.gd" || setter.gotParams.Name != "health" {
		t.Fatalf("handler did not pass through params, got %+v", setter.gotParams)
	}
	if setter.gotParams.IntValue == nil || *setter.gotParams.IntValue != 100 {
		t.Fatalf("handler did not pass through int_value, got %+v", setter.gotParams.IntValue)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Operation != "set_script_export" {
		t.Errorf("audit entry operation = %q, want %q", entry.Operation, "set_script_export")
	}
	if entry.Outcome != audit.OutcomeOK {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeOK)
	}
}

func TestSetScriptExport_Error(t *testing.T) {
	wantErr := errors.New("boom: no such script")
	setter := &fakeScriptExportSetter{err: wantErr}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeReadWrite)
	deps.ScriptExport = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_script_export",
		Arguments: map[string]any{
			"script_path": "player.gd",
			"name":        "health",
			"int_value":   100,
		},
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

// TestSetScriptExport_NotRegisteredUnderReadOnly confirms set_script_export
// is a write tool: never advertised to the MCP client under ModeReadOnly.
func TestSetScriptExport_NotRegisteredUnderReadOnly(t *testing.T) {
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, fullDeps(logger, tools.ModeReadOnly))

	cs := connect(t, server)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tl := range list.Tools {
		if tl.Name == "set_script_export" {
			t.Fatal("set_script_export was advertised under ModeReadOnly")
		}
	}
}

func TestSetFunctionBody_Success(t *testing.T) {
	setter := &fakeFunctionBodySetter{
		result: &headless.SetFunctionBodyResult{
			Path:         "res://player.gd",
			FunctionName: "take_damage",
			Action:       "modified",
		},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeAdvanced)
	deps.FunctionBody = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_function_body",
		Arguments: map[string]any{
			"script_path":   "player.gd",
			"function_name": "take_damage",
			"body":          "health -= amount",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if setter.gotParams.ScriptPath != "player.gd" || setter.gotParams.FunctionName != "take_damage" || setter.gotParams.Body != "health -= amount" {
		t.Fatalf("handler did not pass through params, got %+v", setter.gotParams)
	}

	logLine := strings.TrimSpace(logBuf.String())
	var entry audit.Entry
	if err := json.Unmarshal([]byte(logLine), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logLine)
	}
	if entry.Operation != "set_function_body" {
		t.Errorf("audit entry operation = %q, want %q", entry.Operation, "set_function_body")
	}
	if entry.Outcome != audit.OutcomeOK {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeOK)
	}
}

func TestSetFunctionBody_PassesThroughOptionalSignatureFields(t *testing.T) {
	setter := &fakeFunctionBodySetter{
		result: &headless.SetFunctionBodyResult{Path: "res://player.gd", FunctionName: "attack", Action: "inserted"},
	}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeAdvanced)
	deps.FunctionBody = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_function_body",
		Arguments: map[string]any{
			"script_path":   "player.gd",
			"function_name": "attack",
			"parameters":    "target: Node",
			"return_type":   "void",
			"body":          "target.take_damage(10)",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError=true, content: %+v", res.Content)
	}
	if setter.gotParams.Parameters == nil || *setter.gotParams.Parameters != "target: Node" {
		t.Fatalf("handler did not pass through parameters, got %+v", setter.gotParams.Parameters)
	}
	if setter.gotParams.ReturnType == nil || *setter.gotParams.ReturnType != "void" {
		t.Fatalf("handler did not pass through return_type, got %+v", setter.gotParams.ReturnType)
	}
}

func TestSetFunctionBody_Error(t *testing.T) {
	wantErr := errors.New("boom: function signature does not fit on a single line")
	setter := &fakeFunctionBodySetter{err: wantErr}
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	deps := fullDeps(logger, tools.ModeAdvanced)
	deps.FunctionBody = setter
	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, deps)

	cs := connect(t, server)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "set_function_body",
		Arguments: map[string]any{
			"script_path":   "player.gd",
			"function_name": "take_damage",
			"body":          "pass",
		},
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

// TestSetFunctionBody_NotRegisteredUnderReadWrite confirms set_function_body
// is gated strictly behind ModeAdvanced, not merely ModeReadWrite — the one
// tool in this server that lets an AI client author or replace executable
// GDScript logic must never be reachable from the ordinary write tier.
func TestSetFunctionBody_NotRegisteredUnderReadWrite(t *testing.T) {
	var logBuf bytes.Buffer
	logger := audit.New(&logBuf)

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server-test", Version: "v0.0.1"}, nil)
	tools.RegisterAll(server, fullDeps(logger, tools.ModeReadWrite))

	cs := connect(t, server)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tl := range list.Tools {
		if tl.Name == "set_function_body" {
			t.Fatal("set_function_body was advertised under ModeReadWrite")
		}
	}
}
