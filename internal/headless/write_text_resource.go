// Resource authoring: write_text_resource. Creates or overwrites a .tres
// file by constructing a Resource of a given class (or a script-defined
// custom Resource subclass) and setting property values on it, then saving
// with ResourceSaver.save() — the write-side counterpart to
// read_text_resource/read_binary_resource, and the first tool in this
// server that constructs a new Resource from inline parameters rather than
// only referencing an existing one (see SetNodePropertyParams.ResourceValue's
// doc comment for the "reference-only" boundary this deliberately revises,
// and FEATURES.md for the rationale).
//
// Unlike the scene tools in node_mutation.go, this never instantiates a
// PackedScene/SceneTree root — a Resource isn't part of a scene tree, so
// there's nothing to pack() or free() here, just a bare Resource object
// built directly and saved.
//
// Mode gating is deliberately NOT decided here: this package has no concept
// of -mode by design (see package doc comment). A built-in class_name is a
// structural, non-executable construction, but a script_path names a
// project script whose class shape is only known by loading and
// instantiating that script (script.new()) — running its _init(). Whether
// that requires -mode advanced is a policy decision made by
// internal/tools.registerWriteTextResource, before this method is ever
// called; see that function's doc comment.
package headless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PropertyValueFields is the "exactly one of ..." typed-value union for a
// single property being set on a resource under construction here.
// Deliberately mirrors SetNodePropertyParams's own value-field union
// field-for-field (same names, types, JSON tags) rather than sharing it via
// Go struct embedding: embedding would break every existing keyed
// SetNodePropertyParams{...} struct literal across the test suite (Go's
// composite-literal syntax does not allow naming a promoted field from an
// embedded struct as a literal key), which is a much bigger, riskier diff
// than the modest field-declaration duplication accepted here. The value
// *parsing* logic (the actual branch on which field is set, and how it
// becomes a GDScript Variant) is not duplicated — both operations share
// scripts/godot_operations.gd's _parse_property_value/_apply_property_value.
type PropertyValueFields struct {
	StringValue      *string      `json:"string_value,omitempty" jsonschema:"set this property to this string value; exactly one of the *_value fields must be set"`
	IntValue         *int64       `json:"int_value,omitempty" jsonschema:"set this property to this integer value; exactly one of the *_value fields must be set"`
	FloatValue       *float64     `json:"float_value,omitempty" jsonschema:"set this property to this floating-point value; exactly one of the *_value fields must be set"`
	BoolValue        *bool        `json:"bool_value,omitempty" jsonschema:"set this property to this boolean value; exactly one of the *_value fields must be set"`
	Vector2Value     *Vector2     `json:"vector2_value,omitempty" jsonschema:"set this property to this Vector2 value; exactly one of the *_value fields must be set"`
	Vector3Value     *Vector3     `json:"vector3_value,omitempty" jsonschema:"set this property to this Vector3 value; exactly one of the *_value fields must be set"`
	ColorValue       *Color       `json:"color_value,omitempty" jsonschema:"set this property to this Color value; exactly one of the *_value fields must be set"`
	Vector2iValue    *Vector2i    `json:"vector2i_value,omitempty" jsonschema:"set this property to this Vector2i value; exactly one of the *_value fields must be set"`
	Vector3iValue    *Vector3i    `json:"vector3i_value,omitempty" jsonschema:"set this property to this Vector3i value; exactly one of the *_value fields must be set"`
	QuaternionValue  *Quaternion  `json:"quaternion_value,omitempty" jsonschema:"set this property to this Quaternion value; exactly one of the *_value fields must be set"`
	Rect2Value       *Rect2       `json:"rect2_value,omitempty" jsonschema:"set this property to this Rect2 value; exactly one of the *_value fields must be set"`
	Rect2iValue      *Rect2i      `json:"rect2i_value,omitempty" jsonschema:"set this property to this Rect2i value; exactly one of the *_value fields must be set"`
	PlaneValue       *Plane       `json:"plane_value,omitempty" jsonschema:"set this property to this Plane value; exactly one of the *_value fields must be set"`
	AABBValue        *AABB        `json:"aabb_value,omitempty" jsonschema:"set this property to this AABB value; exactly one of the *_value fields must be set"`
	BasisValue       *Basis       `json:"basis_value,omitempty" jsonschema:"set this property to this Basis value; exactly one of the *_value fields must be set"`
	Transform2DValue *Transform2D `json:"transform2d_value,omitempty" jsonschema:"set this property to this Transform2D value; exactly one of the *_value fields must be set"`
	Transform3DValue *Transform3D `json:"transform3d_value,omitempty" jsonschema:"set this property to this Transform3D value; exactly one of the *_value fields must be set"`
	// NodePathValue is not a filesystem path — same NodePath syntax as
	// set_node_property's own node_path_value. Unusual on a Resource (which
	// isn't part of a scene tree), but a custom Resource subclass could
	// legitimately declare a NodePath-typed export as plain data, so it's
	// not specially excluded here.
	NodePathValue *string `json:"node_path_value,omitempty" jsonschema:"set this property to this NodePath value; exactly one of the *_value fields must be set"`

	StringArrayValue   []string  `json:"string_array_value,omitzero" jsonschema:"set this property to this PackedStringArray value; exactly one of the *_value fields must be set"`
	IntArrayValue      []int64   `json:"int_array_value,omitzero" jsonschema:"set this property to this PackedInt32Array value; exactly one of the *_value fields must be set"`
	FloatArrayValue    []float64 `json:"float_array_value,omitzero" jsonschema:"set this property to this PackedFloat32Array value; exactly one of the *_value fields must be set"`
	Vector2ArrayValue  []Vector2 `json:"vector2_array_value,omitzero" jsonschema:"set this property to this PackedVector2Array value; exactly one of the *_value fields must be set"`
	ColorArrayValue    []Color   `json:"color_array_value,omitzero" jsonschema:"set this property to this PackedColorArray value; exactly one of the *_value fields must be set"`
	Vector3ArrayValue  []Vector3 `json:"vector3_array_value,omitzero" jsonschema:"set this property to this PackedVector3Array value; exactly one of the *_value fields must be set"`
	NodePathArrayValue []string  `json:"node_path_array_value,omitzero" jsonschema:"set this property to this Array[NodePath] value; exactly one of the *_value fields must be set"`

	// ResourceValue is a project-relative path to an existing resource
	// file, validated against Root exactly like ResourcePath, then loaded
	// and assigned as this property's value. property_name "script" is
	// refused unconditionally regardless of this field — see
	// WriteTextResource's doc comment.
	ResourceValue *string `json:"resource_value,omitempty" jsonschema:"set this property to this Resource value, referencing an existing project resource file by path; exactly one of the *_value fields must be set"`

	TypedStringArrayValue  []string  `json:"typed_string_array_value,omitzero" jsonschema:"set this property to this Array[String] value; exactly one of the *_value fields must be set"`
	TypedIntArrayValue     []int64   `json:"typed_int_array_value,omitzero" jsonschema:"set this property to this Array[int] value; exactly one of the *_value fields must be set"`
	TypedFloatArrayValue   []float64 `json:"typed_float_array_value,omitzero" jsonschema:"set this property to this Array[float] value; exactly one of the *_value fields must be set"`
	TypedVector2ArrayValue []Vector2 `json:"typed_vector2_array_value,omitzero" jsonschema:"set this property to this Array[Vector2] value; exactly one of the *_value fields must be set"`
	TypedColorArrayValue   []Color   `json:"typed_color_array_value,omitzero" jsonschema:"set this property to this Array[Color] value; exactly one of the *_value fields must be set"`
	TypedVector3ArrayValue []Vector3 `json:"typed_vector3_array_value,omitzero" jsonschema:"set this property to this Array[Vector3] value; exactly one of the *_value fields must be set"`
	// TypedResourceArrayValue sets a designer-typed Array[T] property whose
	// element type T is itself a Resource subclass — each element is a
	// project-relative path to an existing resource file, validated
	// against Root and loaded exactly like ResourceValue, just per
	// element. property_name "script" is refused unconditionally
	// regardless of this field, same as ResourceValue.
	TypedResourceArrayValue []string `json:"typed_resource_array_value,omitzero" jsonschema:"set this property to this Array[T] value where T is a Resource subclass, each element a project-relative path to an existing resource file; exactly one of the *_value fields must be set"`
}

// propertyValueFieldNames is the slash-joined list of every
// PropertyValueFields field name, for a single "exactly one of ..." error
// message.
const propertyValueFieldNames = "string_value, int_value, float_value, bool_value, vector2_value, vector3_value, color_value, vector2i_value, vector3i_value, quaternion_value, rect2_value, rect2i_value, plane_value, aabb_value, basis_value, transform2d_value, transform3d_value, node_path_value, string_array_value, int_array_value, float_array_value, vector2_array_value, color_array_value, vector3_array_value, node_path_array_value, resource_value, typed_string_array_value, typed_int_array_value, typed_float_array_value, typed_vector2_array_value, typed_color_array_value, typed_vector3_array_value, typed_resource_array_value"

// valuesSet counts how many of f's *_value fields are non-nil/non-nil-slice.
// Callers require this to equal exactly 1.
func (f PropertyValueFields) valuesSet() int {
	n := 0
	for _, set := range []bool{
		f.StringValue != nil,
		f.IntValue != nil,
		f.FloatValue != nil,
		f.BoolValue != nil,
		f.Vector2Value != nil,
		f.Vector3Value != nil,
		f.ColorValue != nil,
		f.Vector2iValue != nil,
		f.Vector3iValue != nil,
		f.QuaternionValue != nil,
		f.Rect2Value != nil,
		f.Rect2iValue != nil,
		f.PlaneValue != nil,
		f.AABBValue != nil,
		f.BasisValue != nil,
		f.Transform2DValue != nil,
		f.Transform3DValue != nil,
		f.NodePathValue != nil,
		f.StringArrayValue != nil,
		f.IntArrayValue != nil,
		f.FloatArrayValue != nil,
		f.Vector2ArrayValue != nil,
		f.ColorArrayValue != nil,
		f.Vector3ArrayValue != nil,
		f.NodePathArrayValue != nil,
		f.ResourceValue != nil,
		f.TypedStringArrayValue != nil,
		f.TypedIntArrayValue != nil,
		f.TypedFloatArrayValue != nil,
		f.TypedVector2ArrayValue != nil,
		f.TypedColorArrayValue != nil,
		f.TypedVector3ArrayValue != nil,
		f.TypedResourceArrayValue != nil,
	} {
		if set {
			n++
		}
	}
	return n
}

// WriteTextResourcePropertyValue is one property-name/value pair in
// WriteTextResourceParams.Properties.
type WriteTextResourcePropertyValue struct {
	// PropertyName is the property to set on the constructed resource, e.g.
	// "max_health". "script" is refused unconditionally — see
	// WriteTextResource's doc comment.
	PropertyName string `json:"property_name" jsonschema:"the property to set on the constructed resource, e.g. \"max_health\""`
	PropertyValueFields
}

// WriteTextResourceParams are the parameters for the write_text_resource
// operation. Exactly one of ClassName/ScriptPath must be set: ClassName
// constructs a built-in ClassDB Resource subclass (e.g. "Theme"), while
// ScriptPath instances a project script defining a custom Resource
// subclass — the only way to construct one, since a custom resource's
// entire property shape is defined by that script, not by the engine.
// Properties is optional: omitted or empty means every property keeps its
// class's default value.
type WriteTextResourceParams struct {
	// ResourcePath is the .tres path to write, relative to the project
	// root. Validated against Root before use. Its parent directory must
	// already exist — this operation never creates directories.
	ResourcePath string `json:"resource_path" jsonschema:"path to the .tres file to write, relative to the project root; the parent directory must already exist"`
	// ClassName constructs a built-in, ClassDB-registered Resource
	// subclass (e.g. "Theme", "ShaderMaterial", "StyleBoxFlat"). Exactly
	// one of ClassName/ScriptPath must be set.
	ClassName *string `json:"class_name,omitempty" jsonschema:"a built-in Godot engine class name that is a Resource subclass, e.g. \"Theme\" or \"ShaderMaterial\"; exactly one of class_name/script_path must be set"`
	// ScriptPath is a project-relative path to a .gd script defining a
	// custom Resource subclass (extends Resource, with its own @export var
	// properties) — the only way to construct one, since its property
	// shape exists only in that script, not in the engine. Instantiating a
	// project script runs it (its _init(), if any), so this is only
	// available when the server was started with -mode advanced — see
	// internal/tools.registerWriteTextResource. Exactly one of
	// ClassName/ScriptPath must be set.
	ScriptPath *string `json:"script_path,omitempty" jsonschema:"path to a project .gd script defining a custom Resource subclass, relative to the project root; exactly one of class_name/script_path must be set; only available under -mode advanced"`
	// Properties are the property_name/value pairs to set on the
	// constructed resource before saving. Optional: omitted or empty means
	// every property keeps its class's default value (e.g. a blank
	// Theme.tres to build on with a later, overwrite: true call — there is
	// no separate "edit" tool).
	Properties []WriteTextResourcePropertyValue `json:"properties,omitempty" jsonschema:"property_name/value pairs to set on the constructed resource; omit or leave empty to use every default value"`
	// Overwrite, if false (the default), refuses to replace an existing
	// file at ResourcePath. ResourceSaver.save() has no "fail if exists"
	// mode of its own, so this is enforced before ever invoking Godot.
	Overwrite bool `json:"overwrite,omitempty" jsonschema:"if true, overwrite an existing file at resource_path; if false (the default), fail rather than replace it"`
}

// WriteTextResourceResult confirms a completed resource write.
type WriteTextResourceResult struct {
	// Path is the resource's res://-style path.
	Path string `json:"path"`
	// Type is the constructed resource's class: ClassName echoed back for
	// the ClassName case, or the script's own declared class_name (falling
	// back to its native base class if the script doesn't declare one) for
	// the ScriptPath case — Godot's own get_class() always reports the
	// native base class, never a script's class_name, so ScriptPath's own
	// result would be uselessly "Resource" without this.
	Type string `json:"type"`
	// Action is "created" if ResourcePath didn't already exist, or
	// "overwritten" if it did (only possible when Overwrite was true).
	Action string `json:"action"`
}

// WriteTextResource constructs a Resource (built-in class or a
// script-defined custom subclass), sets zero or more properties on it, and
// saves it as a new or overwritten .tres file. See the package doc comment
// above for why this needs no PackedScene/SceneTree machinery, and
// internal/tools.registerWriteTextResource for the -mode advanced gate on
// ScriptPath.
func (c *Client) WriteTextResource(ctx context.Context, params WriteTextResourceParams) (*WriteTextResourceResult, error) {
	absResourcePath, err := c.Root.Resolve(params.ResourcePath)
	if err != nil {
		return nil, fmt.Errorf("headless: write_text_resource: %w", err)
	}
	if filepath.Ext(absResourcePath) != ".tres" {
		return nil, fmt.Errorf("headless: write_text_resource: not a .tres file: %s", params.ResourcePath)
	}
	if (params.ClassName == nil) == (params.ScriptPath == nil) {
		return nil, errors.New("headless: write_text_resource: exactly one of class_name, script_path must be set")
	}

	var scriptResPath *string
	if params.ScriptPath != nil {
		absScriptPath, err := c.Root.Resolve(*params.ScriptPath)
		if err != nil {
			return nil, fmt.Errorf("headless: write_text_resource: %w", err)
		}
		if filepath.Ext(absScriptPath) != ".gd" {
			return nil, fmt.Errorf("headless: write_text_resource: not a .gd file: %s", *params.ScriptPath)
		}
		relScriptPath, err := filepath.Rel(c.Root.String(), absScriptPath)
		if err != nil {
			return nil, fmt.Errorf("headless: write_text_resource: computing project-relative script path: %w", err)
		}
		rp := "res://" + filepath.ToSlash(relScriptPath)
		scriptResPath = &rp
	}

	parentDir := filepath.Dir(absResourcePath)
	parentInfo, err := os.Stat(parentDir)
	if err != nil {
		return nil, fmt.Errorf("headless: write_text_resource: parent directory does not exist: %w", err)
	}
	if !parentInfo.IsDir() {
		return nil, fmt.Errorf("headless: write_text_resource: parent path is not a directory: %s", parentDir)
	}

	action := "created"
	if _, err := os.Stat(absResourcePath); err == nil {
		if !params.Overwrite {
			return nil, fmt.Errorf("headless: write_text_resource: %s already exists (set overwrite to replace it)", params.ResourcePath)
		}
		action = "overwritten"
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("headless: write_text_resource: %w", err)
	}

	seenNames := make(map[string]bool, len(params.Properties))
	resolvedProperties := make([]WriteTextResourcePropertyValue, len(params.Properties))
	for i, prop := range params.Properties {
		if prop.PropertyName == "" {
			return nil, fmt.Errorf("headless: write_text_resource: properties[%d]: property_name is required", i)
		}
		// Same hard constraint as set_node_property: assigning a Script is
		// code execution by another name, not a data write. Without this,
		// a resource_value pointing at an arbitrary .gd file (Script is
		// itself a Resource, so resource_value's own loader would happily
		// load it) would let a caller attach any script to the constructed
		// resource, bypassing the ScriptPath/-mode advanced gate entirely.
		if prop.PropertyName == "script" {
			return nil, fmt.Errorf("headless: write_text_resource: properties[%d]: refusing to set \"script\": assigning a Script is not permitted", i)
		}
		if seenNames[prop.PropertyName] {
			return nil, fmt.Errorf("headless: write_text_resource: properties[%d]: duplicate property_name %q in one call", i, prop.PropertyName)
		}
		seenNames[prop.PropertyName] = true
		if valuesSet := prop.valuesSet(); valuesSet != 1 {
			return nil, fmt.Errorf("headless: write_text_resource: properties[%d]: exactly one of %s must be set, got %d", i, propertyValueFieldNames, valuesSet)
		}

		resolved := prop
		// ResourceValue/TypedResourceArrayValue go through the same
		// Root.Resolve trust boundary as ScriptPath/ResourcePath above —
		// see SetNodeProperty's identical treatment of these two fields.
		if resolved.ResourceValue != nil {
			absPath, err := c.Root.Resolve(*resolved.ResourceValue)
			if err != nil {
				return nil, fmt.Errorf("headless: write_text_resource: properties[%d]: %w", i, err)
			}
			relPath, err := filepath.Rel(c.Root.String(), absPath)
			if err != nil {
				return nil, fmt.Errorf("headless: write_text_resource: computing project-relative resource path: %w", err)
			}
			rp := "res://" + filepath.ToSlash(relPath)
			resolved.ResourceValue = &rp
		}
		if resolved.TypedResourceArrayValue != nil {
			resolvedPaths := make([]string, len(resolved.TypedResourceArrayValue))
			for j, p := range resolved.TypedResourceArrayValue {
				absPath, err := c.Root.Resolve(p)
				if err != nil {
					return nil, fmt.Errorf("headless: write_text_resource: properties[%d]: typed_resource_array_value[%d]: %w", i, j, err)
				}
				relPath, err := filepath.Rel(c.Root.String(), absPath)
				if err != nil {
					return nil, fmt.Errorf("headless: write_text_resource: computing project-relative resource path: %w", err)
				}
				resolvedPaths[j] = "res://" + filepath.ToSlash(relPath)
			}
			resolved.TypedResourceArrayValue = resolvedPaths
		}
		resolvedProperties[i] = resolved
	}

	relResourcePath, err := filepath.Rel(c.Root.String(), absResourcePath)
	if err != nil {
		return nil, fmt.Errorf("headless: write_text_resource: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relResourcePath)

	var result struct {
		Type string `json:"type"`
	}
	if err := c.run(ctx, "write_text_resource", struct {
		Path       string                           `json:"path"`
		ClassName  *string                          `json:"class_name,omitempty"`
		ScriptPath *string                          `json:"script_path,omitempty"`
		Properties []WriteTextResourcePropertyValue `json:"properties,omitempty"`
	}{
		Path:       resPath,
		ClassName:  params.ClassName,
		ScriptPath: scriptResPath,
		Properties: resolvedProperties,
	}, &result); err != nil {
		return nil, fmt.Errorf("headless: write_text_resource: %w", err)
	}

	return &WriteTextResourceResult{
		Path:   resPath,
		Type:   result.Type,
		Action: action,
	}, nil
}
