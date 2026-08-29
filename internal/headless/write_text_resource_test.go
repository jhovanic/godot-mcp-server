package headless

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jhovanic/godot-mcp-server/internal/validate"
)

// WriteTextResource validates ResourcePath/ScriptPath, the .tres/.gd
// extensions, the parent-directory and overwrite guards, and every
// Properties entry before ever invoking Godot, so these rejection cases
// are testable without a Godot binary: newDirectReadTestClient's garbage
// GodotBin would fail loudly if any of them reached exec.Command. The
// success path genuinely needs Godot — see TestWriteTextResource_RealGodot*
// in integration_test.go.

func TestWriteTextResource_RejectsOutOfRootResourcePath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	className := "Theme"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "../outside.tres",
		ClassName:    &className,
	})
	if err == nil {
		t.Fatal("WriteTextResource with a traversal resource_path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("WriteTextResource error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestWriteTextResource_RejectsOutOfRootScriptPath(t *testing.T) {
	c := newDirectReadTestClient(t, t.TempDir())

	scriptPath := "../outside.gd"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ScriptPath:   &scriptPath,
	})
	if err == nil {
		t.Fatal("WriteTextResource with a traversal script_path, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("WriteTextResource error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestWriteTextResource_RejectsNonTresExtension(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "notes.txt",
		ClassName:    &className,
	})
	if err == nil {
		t.Fatal("WriteTextResource on a non-.tres path, want error")
	}
}

func TestWriteTextResource_RejectsNonGdScriptPathExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a script"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	scriptPath := "notes.txt"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ScriptPath:   &scriptPath,
	})
	if err == nil {
		t.Fatal("WriteTextResource with a non-.gd script_path, want error")
	}
}

func TestWriteTextResource_RejectsZeroValuesSet(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
	})
	if err == nil {
		t.Fatal("WriteTextResource with neither class_name nor script_path set, want error")
	}
	if !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("WriteTextResource error = %v, want an \"exactly one of\" message", err)
	}
}

func TestWriteTextResource_RejectsBothValuesSet(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	scriptPath := "stats.gd"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
		ScriptPath:   &scriptPath,
	})
	if err == nil {
		t.Fatal("WriteTextResource with both class_name and script_path set, want error")
	}
	if !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("WriteTextResource error = %v, want an \"exactly one of\" message", err)
	}
}

func TestWriteTextResource_RejectsMissingParentDirectory(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "does_not_exist/stats.tres",
		ClassName:    &className,
	})
	if err == nil {
		t.Fatal("WriteTextResource with a missing parent directory, want error")
	}
}

func TestWriteTextResource_RejectsExistingFileWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stats.tres"), []byte("[gd_resource type=\"Resource\" format=3]\n\n[resource]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
	})
	if err == nil {
		t.Fatal("WriteTextResource against an existing file without overwrite, want error")
	}
}

func TestWriteTextResource_ExistingFileWithOverwriteIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stats.tres"), []byte("[gd_resource type=\"Resource\" format=3]\n\n[resource]\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
		Overwrite:    true,
	})
	if err == nil {
		t.Fatal("WriteTextResource with overwrite against a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "already exists") {
		t.Fatalf("WriteTextResource rejected an overwrite-permitted existing file: %v", err)
	}
}

func TestWriteTextResource_EmptyPropertiesIsValid(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
	})
	if err == nil {
		t.Fatal("WriteTextResource with no properties and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "properties") {
		t.Fatalf("WriteTextResource rejected an empty properties list: %v", err)
	}
}

func TestWriteTextResource_RejectsScriptPropertyName(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	strVal := "res://evil.gd"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
		Properties: []WriteTextResourcePropertyValue{
			{PropertyName: "script", PropertyValueFields: PropertyValueFields{ResourceValue: &strVal}},
		},
	})
	if err == nil {
		t.Fatal("WriteTextResource with property_name \"script\", want error")
	}
	if !strings.Contains(err.Error(), "script") {
		t.Fatalf("WriteTextResource error = %v, want it to mention \"script\"", err)
	}
}

func TestWriteTextResource_RejectsDuplicatePropertyNames(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	i1, i2 := int64(1), int64(2)
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
		Properties: []WriteTextResourcePropertyValue{
			{PropertyName: "max_health", PropertyValueFields: PropertyValueFields{IntValue: &i1}},
			{PropertyName: "max_health", PropertyValueFields: PropertyValueFields{IntValue: &i2}},
		},
	})
	if err == nil {
		t.Fatal("WriteTextResource with a duplicate property_name, want error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("WriteTextResource error = %v, want it to mention \"duplicate\"", err)
	}
}

func TestWriteTextResource_RejectsPropertyWithZeroValuesSet(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
		Properties: []WriteTextResourcePropertyValue{
			{PropertyName: "max_health"},
		},
	})
	if err == nil {
		t.Fatal("WriteTextResource with a properties entry lacking any value, want error")
	}
	if !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("WriteTextResource error = %v, want an \"exactly one of\" message", err)
	}
}

func TestWriteTextResource_RejectsPropertyWithMultipleValuesSet(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	i := int64(1)
	s := "hello"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
		Properties: []WriteTextResourcePropertyValue{
			{PropertyName: "max_health", PropertyValueFields: PropertyValueFields{IntValue: &i, StringValue: &s}},
		},
	})
	if err == nil {
		t.Fatal("WriteTextResource with a properties entry setting two values, want error")
	}
	if !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("WriteTextResource error = %v, want an \"exactly one of\" message", err)
	}
}

func TestWriteTextResource_RejectsEmptyPropertyName(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	i := int64(1)
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
		Properties: []WriteTextResourcePropertyValue{
			{PropertyName: "", PropertyValueFields: PropertyValueFields{IntValue: &i}},
		},
	})
	if err == nil {
		t.Fatal("WriteTextResource with an empty property_name, want error")
	}
}

func TestWriteTextResource_RejectsOutOfRootResourceValueProperty(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	outside := "../outside.tres"
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
		Properties: []WriteTextResourcePropertyValue{
			{PropertyName: "icon", PropertyValueFields: PropertyValueFields{ResourceValue: &outside}},
		},
	})
	if err == nil {
		t.Fatal("WriteTextResource with a traversal resource_value property, want error")
	}
	if !errors.Is(err, validate.ErrOutsideRoot) {
		t.Fatalf("WriteTextResource error = %v, want wrapping validate.ErrOutsideRoot", err)
	}
}

func TestWriteTextResource_OneValidPropertyIsValid(t *testing.T) {
	dir := t.TempDir()
	c := newDirectReadTestClient(t, dir)

	className := "Theme"
	i := int64(100)
	_, err := c.WriteTextResource(context.Background(), WriteTextResourceParams{
		ResourcePath: "stats.tres",
		ClassName:    &className,
		Properties: []WriteTextResourcePropertyValue{
			{PropertyName: "max_health", PropertyValueFields: PropertyValueFields{IntValue: &i}},
		},
	})
	if err == nil {
		t.Fatal("WriteTextResource with one valid property and a garbage GodotBin, want an exec error")
	}
	if strings.Contains(err.Error(), "exactly one of") || strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("WriteTextResource rejected a valid single property: %v", err)
	}
}
