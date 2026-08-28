// Scoped script editing: set_script_export and set_function_body.
//
// Both operate on an existing .gd script's raw text directly in Go — no
// scripts/godot_operations.gd involvement at all, the same rationale as
// ReadScript (see its doc comment): GDScript is plain UTF-8 text, so no
// engine capability is needed to read or splice it. The only place either
// tool invokes Godot is a separate, second call to `godot --headless
// --check-only --script <path>` after writing, purely as a post-write
// verifier — confirmed empirically that this flag parses a script for
// errors only (exit 0 with no output on success, exit 1 with a `SCRIPT
// ERROR:` message on failure) and executes no class-body or function-body
// code. If the check fails, the file is rolled back to its pre-write
// content and a clear error is returned — a script this tool touches is
// never left in a state that doesn't parse.
//
// Each tool splits into a pure string-in/string-out splice function (no
// *Client, no filesystem, no Godot — unit-tested directly in
// script_edit_test.go) and a thin (c *Client) SetX orchestration method
// that validates, reads, calls the pure function, writes, and verifies.
package headless

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// splitPreservingLineEnding splits source into lines, reporting which line
// ending it used ("\n" or "\r\n", detected once from whichever appears in
// source) and whether source ended with one, so joinWithLineEnding can
// reproduce the original convention exactly. Unlike SetNodeProperty's
// output (Godot's own full scene re-serialization, explicitly not a
// minimal diff), a raw text splice has no such excuse: it should stay as
// close to a minimal diff as practical.
func splitPreservingLineEnding(source string) (lines []string, ending string, trailingNewline bool) {
	ending = "\n"
	if strings.Contains(source, "\r\n") {
		ending = "\r\n"
	}
	trailingNewline = strings.HasSuffix(source, ending)
	body := source
	if trailingNewline {
		body = strings.TrimSuffix(body, ending)
	}
	if body == "" {
		return []string{}, ending, trailingNewline
	}
	return strings.Split(body, ending), ending, trailingNewline
}

// joinWithLineEnding is splitPreservingLineEnding's inverse.
func joinWithLineEnding(lines []string, ending string, trailingNewline bool) string {
	joined := strings.Join(lines, ending)
	if trailingNewline {
		joined += ending
	}
	return joined
}

var gdscriptIdentifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validGDScriptIdentifier checks only that name is syntactically a valid
// identifier — it does not check GDScript's reserved-word table (a name
// like "var" or "func" passes this check). That's a deliberate v1
// simplification: the --check-only verification step is the actual
// backstop for a reserved-word collision, after a real (rolled-back) write
// attempt, rather than duplicating GDScript's own keyword list here.
func validGDScriptIdentifier(name string) bool {
	return gdscriptIdentifierRe.MatchString(name)
}

// formatGDScriptFloat renders f the way a GDScript float literal should
// read: strconv.FormatFloat's shortest round-tripping representation, with
// a forced ".0" suffix if that representation has no decimal point at all
// (so a whole-number default for a `: float` field, e.g. 2, never reads as
// an ambiguous bare integer).
func formatGDScriptFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// checkScriptParses runs `godot --headless --check-only --script <resPath>`
// and distinguishes a clean parse failure (Godot ran and rejected the
// script — the caller should roll back and report this) from an
// infrastructure failure (the binary itself couldn't run).
func (c *Client) checkScriptParses(ctx context.Context, resPath string) error {
	// #nosec G204 -- argv is fixed (godot --headless --path <root>
	// --check-only --script <res:// path>); resPath is always Go-derived
	// from Root.Resolve via the caller, never passed through unvalidated.
	cmd := exec.CommandContext(ctx, c.GodotBin,
		"--headless",
		"--path", c.Root.String(),
		"--check-only",
		"--script", resPath,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("script does not parse: %s", strings.TrimSpace(stderr.String()))
	}
	return fmt.Errorf("running godot --check-only: %w (stderr: %s)", err, stderr.String())
}

// writeScriptChecked writes updated to absPath, verifies it via
// checkScriptParses, and rolls back to original if verification fails —
// this project's script files are never left in a state that doesn't
// parse. If the rollback write itself fails, the returned error says so
// loudly rather than silently leaving the broken content in place.
func (c *Client) writeScriptChecked(ctx context.Context, absPath, resPath string, original, updated []byte, perm os.FileMode) error {
	if err := os.WriteFile(absPath, updated, perm); err != nil {
		return fmt.Errorf("writing %s: %w", absPath, err)
	}
	if err := c.checkScriptParses(ctx, resPath); err != nil {
		if restoreErr := os.WriteFile(absPath, original, perm); restoreErr != nil {
			return fmt.Errorf("edit produced an invalid script (%w) AND restoring the original %d bytes of content failed (%v) — the file on disk may be left in a bad state, restore it manually", err, len(original), restoreErr)
		}
		return fmt.Errorf("edit produced an invalid script, rolled back to the original content: %w", err)
	}
	return nil
}

// SetScriptExportParams are the parameters for the set_script_export
// operation. Exactly one of StringValue, IntValue, FloatValue, BoolValue,
// Vector2Value, Vector3Value, ColorValue, Vector2iValue, Vector3iValue,
// QuaternionValue, Rect2Value, Rect2iValue, PlaneValue, AABBValue,
// BasisValue, Transform2DValue, Transform3DValue, NodePathValue must be
// set — same one-of discipline as SetNodePropertyParams, reusing its
// Vector2/Vector3/Color/Vector2i/Vector3i/Quaternion/Rect2/Rect2i/Plane/
// AABB/Basis/Transform2D/Transform3D types directly. This is the complete
// set of fixed-arity value types (every one SetNodeProperty itself
// supports except arrays and Resource references) — each renders to a
// GDScript literal using the exact constructor syntax already validated
// against a real Godot build for set_node_property's own GDScript side
// (see scripts/godot_operations.gd's matching branches), so there was no
// new design question for any of these, just the render-as-literal case
// FEATURES.md already anticipated. Packed/typed arrays and Resource-typed
// export defaults remain deferred — those need their own
// literal-rendering design (e.g. a preload() call for a Resource default),
// not just another scalar case; see FEATURES.md for what's deferred and
// why.
type SetScriptExportParams struct {
	// ScriptPath is the .gd path, relative to the project root. Validated
	// against Root before use. The script must already exist — this
	// operation never creates a new script file.
	ScriptPath string `json:"script_path" jsonschema:"path to an existing .gd script file, relative to the project root"`
	// Name is the exported variable's name.
	Name string `json:"name" jsonschema:"the exported variable's name, a valid GDScript identifier"`

	StringValue      *string      `json:"string_value,omitempty" jsonschema:"set the @export var's default to this string value; exactly one of the *_value fields must be set"`
	IntValue         *int64       `json:"int_value,omitempty" jsonschema:"set the @export var's default to this integer value; exactly one of the *_value fields must be set"`
	FloatValue       *float64     `json:"float_value,omitempty" jsonschema:"set the @export var's default to this floating-point value; exactly one of the *_value fields must be set"`
	BoolValue        *bool        `json:"bool_value,omitempty" jsonschema:"set the @export var's default to this boolean value; exactly one of the *_value fields must be set"`
	Vector2Value     *Vector2     `json:"vector2_value,omitempty" jsonschema:"set the @export var's default to this Vector2 value; exactly one of the *_value fields must be set"`
	Vector3Value     *Vector3     `json:"vector3_value,omitempty" jsonschema:"set the @export var's default to this Vector3 value; exactly one of the *_value fields must be set"`
	ColorValue       *Color       `json:"color_value,omitempty" jsonschema:"set the @export var's default to this Color value; exactly one of the *_value fields must be set"`
	Vector2iValue    *Vector2i    `json:"vector2i_value,omitempty" jsonschema:"set the @export var's default to this Vector2i value; exactly one of the *_value fields must be set"`
	Vector3iValue    *Vector3i    `json:"vector3i_value,omitempty" jsonschema:"set the @export var's default to this Vector3i value; exactly one of the *_value fields must be set"`
	QuaternionValue  *Quaternion  `json:"quaternion_value,omitempty" jsonschema:"set the @export var's default to this Quaternion value; exactly one of the *_value fields must be set"`
	Rect2Value       *Rect2       `json:"rect2_value,omitempty" jsonschema:"set the @export var's default to this Rect2 value; exactly one of the *_value fields must be set"`
	Rect2iValue      *Rect2i      `json:"rect2i_value,omitempty" jsonschema:"set the @export var's default to this Rect2i value; exactly one of the *_value fields must be set"`
	PlaneValue       *Plane       `json:"plane_value,omitempty" jsonschema:"set the @export var's default to this Plane value; exactly one of the *_value fields must be set"`
	AABBValue        *AABB        `json:"aabb_value,omitempty" jsonschema:"set the @export var's default to this AABB value; exactly one of the *_value fields must be set"`
	BasisValue       *Basis       `json:"basis_value,omitempty" jsonschema:"set the @export var's default to this Basis value; exactly one of the *_value fields must be set"`
	Transform2DValue *Transform2D `json:"transform2d_value,omitempty" jsonschema:"set the @export var's default to this Transform2D value; exactly one of the *_value fields must be set"`
	Transform3DValue *Transform3D `json:"transform3d_value,omitempty" jsonschema:"set the @export var's default to this Transform3D value; exactly one of the *_value fields must be set"`
	// NodePathValue addresses another node in the same, already-loaded
	// scene tree — this is not a filesystem path.
	NodePathValue *string `json:"node_path_value,omitempty" jsonschema:"set the @export var's default to this NodePath value (e.g. \"../Target\"); exactly one of the *_value fields must be set"`

	// Onready, if true, declares an `@onready var` instead of an
	// `@export var` — evaluated once at _ready() time, not exposed in the
	// editor Inspector. Same name+value shape as an export, just a
	// different annotation and a different natural insertion point (after
	// the last existing @onready var, or after the last @export var if
	// none exists yet).
	Onready bool `json:"onready,omitempty" jsonschema:"if true, declares this as an @onready var instead of an @export var (evaluated at _ready(), not exposed in the editor Inspector)"`
}

// SetScriptExportResult confirms a completed export-declaration write.
type SetScriptExportResult struct {
	// Path is the script's res://-style path.
	Path string `json:"path"`
	// Name is echoed back from the request for confirmation.
	Name string `json:"name"`
	// Action is "modified" if a declaration for Name already existed, or
	// "inserted" if this call added a new one.
	Action string `json:"action"`
	// PreviousDeclaration is the full previous declaration line, for audit
	// purposes. Empty when Action is "inserted".
	PreviousDeclaration string `json:"previous_declaration,omitempty"`
}

// renderScriptExportLiteral returns the GDScript type name and literal
// source text for whichever *_value field of params is set. Assumes
// exactly one is set — SetScriptExport validates that before calling this.
func renderScriptExportLiteral(params SetScriptExportParams) (typeName, literal string) {
	switch {
	case params.StringValue != nil:
		return "String", strconv.Quote(*params.StringValue)
	case params.IntValue != nil:
		return "int", strconv.FormatInt(*params.IntValue, 10)
	case params.FloatValue != nil:
		return "float", formatGDScriptFloat(*params.FloatValue)
	case params.BoolValue != nil:
		return "bool", strconv.FormatBool(*params.BoolValue)
	case params.Vector2Value != nil:
		v := params.Vector2Value
		return "Vector2", fmt.Sprintf("Vector2(%s, %s)", formatGDScriptFloat(v.X), formatGDScriptFloat(v.Y))
	case params.Vector3Value != nil:
		v := params.Vector3Value
		return "Vector3", fmt.Sprintf("Vector3(%s, %s, %s)", formatGDScriptFloat(v.X), formatGDScriptFloat(v.Y), formatGDScriptFloat(v.Z))
	case params.ColorValue != nil:
		v := params.ColorValue
		return "Color", fmt.Sprintf("Color(%s, %s, %s, %s)", formatGDScriptFloat(v.R), formatGDScriptFloat(v.G), formatGDScriptFloat(v.B), formatGDScriptFloat(v.A))
	case params.Vector2iValue != nil:
		v := params.Vector2iValue
		return "Vector2i", fmt.Sprintf("Vector2i(%d, %d)", v.X, v.Y)
	case params.Vector3iValue != nil:
		v := params.Vector3iValue
		return "Vector3i", fmt.Sprintf("Vector3i(%d, %d, %d)", v.X, v.Y, v.Z)
	case params.QuaternionValue != nil:
		v := params.QuaternionValue
		return "Quaternion", fmt.Sprintf("Quaternion(%s, %s, %s, %s)", formatGDScriptFloat(v.X), formatGDScriptFloat(v.Y), formatGDScriptFloat(v.Z), formatGDScriptFloat(v.W))
	case params.Rect2Value != nil:
		v := params.Rect2Value
		return "Rect2", fmt.Sprintf("Rect2(%s, %s, %s, %s)", formatGDScriptFloat(v.Position.X), formatGDScriptFloat(v.Position.Y), formatGDScriptFloat(v.Size.X), formatGDScriptFloat(v.Size.Y))
	case params.Rect2iValue != nil:
		v := params.Rect2iValue
		return "Rect2i", fmt.Sprintf("Rect2i(%d, %d, %d, %d)", v.Position.X, v.Position.Y, v.Size.X, v.Size.Y)
	case params.PlaneValue != nil:
		v := params.PlaneValue
		return "Plane", fmt.Sprintf("Plane(%s, %s, %s, %s)", formatGDScriptFloat(v.X), formatGDScriptFloat(v.Y), formatGDScriptFloat(v.Z), formatGDScriptFloat(v.D))
	case params.AABBValue != nil:
		v := params.AABBValue
		return "AABB", fmt.Sprintf("AABB(%s, %s)", formatVector3Literal(v.Position), formatVector3Literal(v.Size))
	case params.BasisValue != nil:
		v := params.BasisValue
		return "Basis", fmt.Sprintf("Basis(%s, %s, %s)", formatVector3Literal(v.X), formatVector3Literal(v.Y), formatVector3Literal(v.Z))
	case params.Transform2DValue != nil:
		v := params.Transform2DValue
		return "Transform2D", fmt.Sprintf("Transform2D(%s, %s, %s)", formatVector2Literal(v.X), formatVector2Literal(v.Y), formatVector2Literal(v.Origin))
	case params.Transform3DValue != nil:
		v := params.Transform3DValue
		return "Transform3D", fmt.Sprintf("Transform3D(Basis(%s, %s, %s), %s)", formatVector3Literal(v.Basis.X), formatVector3Literal(v.Basis.Y), formatVector3Literal(v.Basis.Z), formatVector3Literal(v.Origin))
	case params.NodePathValue != nil:
		return "NodePath", fmt.Sprintf("NodePath(%s)", strconv.Quote(*params.NodePathValue))
	}
	return "", ""
}

func formatVector2Literal(v Vector2) string {
	return fmt.Sprintf("Vector2(%s, %s)", formatGDScriptFloat(v.X), formatGDScriptFloat(v.Y))
}

func formatVector3Literal(v Vector3) string {
	return fmt.Sprintf("Vector3(%s, %s, %s)", formatGDScriptFloat(v.X), formatGDScriptFloat(v.Y), formatGDScriptFloat(v.Z))
}

// spliceExportDeclaration adds or modifies a single top-level
// `@export var <name>: <typeName> = <literal>` declaration in source, a
// .gd script's full text. A thin wrapper over spliceAnnotatedVarDeclaration
// fixing annotation to "@export" — see that function's doc comment for the
// shared mechanics.
func spliceExportDeclaration(source, name, typeName, literal string) (updated, previous, action string, err error) {
	return spliceAnnotatedVarDeclaration(source, "@export", name, typeName, literal)
}

// spliceAnnotatedVarDeclaration adds or modifies a single top-level
// `<annotation> var <name>: <typeName> = <literal>` declaration in source, a
// .gd script's full text — annotation is either "@export" or "@onready".
// Only ever matches (or inserts as) a column-0 declaration — an indented
// one (e.g. inside a nested `class` block) is a different, intentionally
// out-of-scope declaration and is never touched, matching Array[NodePath]'s
// own "top-level only" precedent in SetNodeProperty. A declaration under
// the *other* annotation with the same name is a distinct declaration and
// is never touched either.
func spliceAnnotatedVarDeclaration(source, annotation, name, typeName, literal string) (updated, previous, action string, err error) {
	lines, ending, trailingNewline := splitPreservingLineEnding(source)
	newLine := fmt.Sprintf("%s var %s: %s = %s", annotation, name, typeName, literal)
	quotedAnnotation := regexp.QuoteMeta(annotation)

	declRe := regexp.MustCompile(`^` + quotedAnnotation + `\s+var\s+` + regexp.QuoteMeta(name) + `\b`)
	for i, line := range lines {
		if declRe.MatchString(line) {
			previous = line
			lines[i] = newLine
			return joinWithLineEnding(lines, ending, trailingNewline), previous, "modified", nil
		}
	}

	// A legal but unusual split: the annotation alone on one line, "var
	// name" on the next. Refuse rather than guess which line is "the"
	// declaration.
	splitRe := regexp.MustCompile(`^` + quotedAnnotation + `\s*$`)
	varRe := regexp.MustCompile(`^var\s+` + regexp.QuoteMeta(name) + `\b`)
	for i, line := range lines {
		if splitRe.MatchString(line) && i+1 < len(lines) && varRe.MatchString(lines[i+1]) {
			return "", "", "", fmt.Errorf("declaration for %q is split across a standalone %q line and a separate \"var\" line, which this operation does not support", name, annotation)
		}
	}

	lastSameRe := regexp.MustCompile(`^` + quotedAnnotation + `\s+var\s+\w+`)
	// @onready vars conventionally follow @export vars, so an @onready
	// insertion falls back to "after the last @export var" before falling
	// back further to "after extends/class_name" — one extra rung above
	// @export's own fallback chain.
	lastExportRe := regexp.MustCompile(`^@export\s+var\s+\w+`)
	extendsClassRe := regexp.MustCompile(`^(extends|class_name)\b`)
	insertAfter := -1
	for i, line := range lines {
		if lastSameRe.MatchString(line) {
			insertAfter = i
		}
	}
	if insertAfter == -1 && annotation == "@onready" {
		for i, line := range lines {
			if lastExportRe.MatchString(line) {
				insertAfter = i
			}
		}
	}
	if insertAfter == -1 {
		for i, line := range lines {
			if extendsClassRe.MatchString(line) {
				insertAfter = i
			}
		}
	}

	newLines := make([]string, 0, len(lines)+1)
	if insertAfter == -1 {
		newLines = append(newLines, newLine)
		newLines = append(newLines, lines...)
	} else {
		newLines = append(newLines, lines[:insertAfter+1]...)
		newLines = append(newLines, newLine)
		newLines = append(newLines, lines[insertAfter+1:]...)
	}

	return joinWithLineEnding(newLines, ending, trailingNewline), "", "inserted", nil
}

// SetScriptExport loads an existing .gd script, adds or modifies one
// top-level @export var declaration, verifies the result still parses (see
// checkScriptParses), and writes it back — rolling back to the original
// content if it doesn't. property_name "script" on set_node_property is
// deliberately out of that tool's scope; this is the dedicated,
// structural-declaration-only alternative for a script's own exported
// properties.
func (c *Client) SetScriptExport(ctx context.Context, params SetScriptExportParams) (*SetScriptExportResult, error) {
	absPath, err := c.Root.Resolve(params.ScriptPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_export: %w", err)
	}
	if filepath.Ext(absPath) != ".gd" {
		return nil, fmt.Errorf("headless: set_script_export: not a .gd file: %s", params.ScriptPath)
	}
	if params.Name == "" {
		return nil, errors.New("headless: set_script_export: name is required")
	}
	if !validGDScriptIdentifier(params.Name) {
		return nil, fmt.Errorf("headless: set_script_export: %q is not a valid GDScript identifier", params.Name)
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
		params.QuaternionValue != nil,
		params.Rect2Value != nil,
		params.Rect2iValue != nil,
		params.PlaneValue != nil,
		params.AABBValue != nil,
		params.BasisValue != nil,
		params.Transform2DValue != nil,
		params.Transform3DValue != nil,
		params.NodePathValue != nil,
	} {
		if set {
			valuesSet++
		}
	}
	if valuesSet != 1 {
		return nil, fmt.Errorf("headless: set_script_export: exactly one of string_value, int_value, float_value, bool_value, vector2_value, vector3_value, color_value, vector2i_value, vector3i_value, quaternion_value, rect2_value, rect2i_value, plane_value, aabb_value, basis_value, transform2d_value, transform3d_value, node_path_value must be set, got %d", valuesSet)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_export: %w", err)
	}
	original, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_export: %w", err)
	}

	annotation := "@export"
	if params.Onready {
		annotation = "@onready"
	}
	typeName, literal := renderScriptExportLiteral(params)
	updated, previous, action, err := spliceAnnotatedVarDeclaration(string(original), annotation, params.Name, typeName, literal)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_export: %w", err)
	}

	relPath, err := filepath.Rel(c.Root.String(), absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_export: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relPath)

	if err := c.writeScriptChecked(ctx, absPath, resPath, original, []byte(updated), info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("headless: set_script_export: %w", err)
	}

	return &SetScriptExportResult{
		Path:                resPath,
		Name:                params.Name,
		Action:              action,
		PreviousDeclaration: previous,
	}, nil
}

// SetScriptSignalParams are the parameters for the set_script_signal
// operation. Unlike an @export/@onready var, a signal has no value — just a
// name and an optional parameter list — so it does not fit
// SetScriptExportParams's shape and gets its own tool.
type SetScriptSignalParams struct {
	// ScriptPath is the .gd path, relative to the project root. Validated
	// against Root before use. The script must already exist — this
	// operation never creates a new script file.
	ScriptPath string `json:"script_path" jsonschema:"path to an existing .gd script file, relative to the project root"`
	// Name is the signal's name.
	Name string `json:"name" jsonschema:"the signal's name, a valid GDScript identifier"`
	// Parameters is independently optional, following the same
	// nil-vs-set convention as SetFunctionBodyParams.Parameters: nil
	// means a bare `signal name` with no parameter list at all; a non-nil
	// value (including an empty string, rendering `signal name()`) always
	// fully determines the parameter list — there is no "leave unchanged"
	// merge here, since a signal has nothing else to merge.
	Parameters *string `json:"parameters,omitempty" jsonschema:"verbatim GDScript parameter list text, e.g. \"old_value, new_value: int\"; must not contain a newline; nil means a bare 'signal name' with no parameter list; an empty string means an explicit empty parameter list 'signal name()'"`
}

// SetScriptSignalResult confirms a completed signal-declaration write.
type SetScriptSignalResult struct {
	// Path is the script's res://-style path.
	Path string `json:"path"`
	// Name is echoed back from the request for confirmation.
	Name string `json:"name"`
	// Action is "modified" if a declaration for Name already existed, or
	// "inserted" if this call added a new one.
	Action string `json:"action"`
	// PreviousDeclaration is the full previous declaration line, for audit
	// purposes. Empty when Action is "inserted".
	PreviousDeclaration string `json:"previous_declaration,omitempty"`
}

// spliceSignalDeclaration adds or modifies a single top-level
// `signal <name>` or `signal <name>(<parameters>)` declaration in source, a
// .gd script's full text. Only ever matches (or inserts as) a column-0
// declaration, same top-level-only precedent as spliceAnnotatedVarDeclaration
// and spliceFunctionBody. Signals conventionally sit near the top of a
// script, above exports/vars, so the insertion fallback is "after the last
// existing signal" then "after extends/class_name" then "prepend at top" —
// no "split across two lines" case exists for a signal the way it does for
// `@export`/`var (a signal's keyword and name are never legally separable).
func spliceSignalDeclaration(source, name string, parameters *string) (updated, previous, action string, err error) {
	if parameters != nil && strings.Contains(*parameters, "\n") {
		return "", "", "", errors.New("parameters must not contain a newline")
	}

	lines, ending, trailingNewline := splitPreservingLineEnding(source)

	var newLine string
	if parameters == nil {
		newLine = fmt.Sprintf("signal %s", name)
	} else {
		newLine = fmt.Sprintf("signal %s(%s)", name, *parameters)
	}

	declRe := regexp.MustCompile(`^signal\s+` + regexp.QuoteMeta(name) + `\b`)
	for i, line := range lines {
		if declRe.MatchString(line) {
			previous = line
			lines[i] = newLine
			return joinWithLineEnding(lines, ending, trailingNewline), previous, "modified", nil
		}
	}

	lastSignalRe := regexp.MustCompile(`^signal\s+\w+`)
	extendsClassRe := regexp.MustCompile(`^(extends|class_name)\b`)
	insertAfter := -1
	for i, line := range lines {
		if lastSignalRe.MatchString(line) {
			insertAfter = i
		}
	}
	if insertAfter == -1 {
		for i, line := range lines {
			if extendsClassRe.MatchString(line) {
				insertAfter = i
			}
		}
	}

	newLines := make([]string, 0, len(lines)+1)
	if insertAfter == -1 {
		newLines = append(newLines, newLine)
		newLines = append(newLines, lines...)
	} else {
		newLines = append(newLines, lines[:insertAfter+1]...)
		newLines = append(newLines, newLine)
		newLines = append(newLines, lines[insertAfter+1:]...)
	}

	return joinWithLineEnding(newLines, ending, trailingNewline), "", "inserted", nil
}

// SetScriptSignal loads an existing .gd script, adds or modifies one
// top-level signal declaration, verifies the result still parses (see
// checkScriptParses), and writes it back — rolling back to the original
// content if it doesn't.
func (c *Client) SetScriptSignal(ctx context.Context, params SetScriptSignalParams) (*SetScriptSignalResult, error) {
	absPath, err := c.Root.Resolve(params.ScriptPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_signal: %w", err)
	}
	if filepath.Ext(absPath) != ".gd" {
		return nil, fmt.Errorf("headless: set_script_signal: not a .gd file: %s", params.ScriptPath)
	}
	if params.Name == "" {
		return nil, errors.New("headless: set_script_signal: name is required")
	}
	if !validGDScriptIdentifier(params.Name) {
		return nil, fmt.Errorf("headless: set_script_signal: %q is not a valid GDScript identifier", params.Name)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_signal: %w", err)
	}
	original, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_signal: %w", err)
	}

	updated, previous, action, err := spliceSignalDeclaration(string(original), params.Name, params.Parameters)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_signal: %w", err)
	}

	relPath, err := filepath.Rel(c.Root.String(), absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_signal: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relPath)

	if err := c.writeScriptChecked(ctx, absPath, resPath, original, []byte(updated), info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("headless: set_script_signal: %w", err)
	}

	return &SetScriptSignalResult{
		Path:                resPath,
		Name:                params.Name,
		Action:              action,
		PreviousDeclaration: previous,
	}, nil
}

// SetScriptIdentityParams are the parameters for the set_script_identity
// operation: a script's own class_name and/or extends declaration. Both are
// singleton, at-most-one-per-file header lines rather than named
// declarations, so they share one tool instead of fitting
// SetScriptExportParams's "name + value" shape. ClassName and Extends are
// independently optional: nil leaves that one alone, an empty string
// removes an existing declaration of that kind (no-op if absent), and any
// other value replaces or inserts it. At least one of the two must be set.
//
// Scope boundary: only the two single-declaration-per-line forms are
// supported — `class_name Foo` alone on its own line, and `extends Bar`
// alone on its own line. The combined one-line form (`class_name Foo
// extends Bar`) is refused outright rather than guessed at, matching this
// file's existing "refuse rather than guess" precedent (see the split
// @export/var case and set_function_body's multi-line-signature refusal).
//
// Changing class_name or extends has a materially bigger blast radius than
// adding one @export property: other scripts in the project that reference
// this one by its old class_name, or that assume its old base class, can
// silently break, and check-only verification (parsing this file alone)
// cannot catch that. This tool does not attempt to detect or fix those
// call sites — same division of responsibility as set_function_body's
// signature edits.
type SetScriptIdentityParams struct {
	// ScriptPath is the .gd path, relative to the project root. Validated
	// against Root before use. The script must already exist — this
	// operation never creates a new script file.
	ScriptPath string `json:"script_path" jsonschema:"path to an existing .gd script file, relative to the project root"`
	// ClassName sets the script's global class_name. Empty string removes
	// an existing class_name declaration. Nil leaves it unchanged.
	ClassName *string `json:"class_name,omitempty" jsonschema:"set the script's global class_name to this identifier; empty string removes an existing class_name declaration; nil leaves class_name unchanged; at least one of class_name/extends must be set"`
	// Extends sets the script's extends target (a class name or a
	// res://-style script path). Empty string removes an existing extends
	// declaration. Nil leaves it unchanged. Not identifier-validated,
	// since a legal target can be a quoted path, not just a bare class
	// name.
	Extends *string `json:"extends,omitempty" jsonschema:"set the script's extends target to this class name or res:// script path; empty string removes an existing extends declaration; nil leaves extends unchanged; at least one of class_name/extends must be set"`
}

// SetScriptIdentityResult confirms a completed class_name/extends write.
type SetScriptIdentityResult struct {
	// Path is the script's res://-style path.
	Path string `json:"path"`
	// PreviousClassName and PreviousExtends are the previous full
	// declaration lines, for audit purposes. Empty if that declaration
	// didn't exist before the call, or wasn't touched by it.
	PreviousClassName string `json:"previous_class_name,omitempty"`
	PreviousExtends   string `json:"previous_extends,omitempty"`
	// ClassNameAction and ExtendsAction are each one of "modified",
	// "inserted", "removed", or "" (that field of the request was nil —
	// this call didn't touch that declaration at all).
	ClassNameAction string `json:"class_name_action,omitempty"`
	ExtendsAction   string `json:"extends_action,omitempty"`
}

// spliceHeaderLine finds at most one line in lines matching declRe — a
// singleton header declaration like class_name or extends. If value is
// empty, an existing match is removed (a no-op if none exists). Otherwise
// an existing match is replaced with "keyword value", or a new line
// "keyword value" is inserted at insertAt if none is found. Returns the
// resulting lines slice, the previous full line (empty if none existed),
// and the action taken: "modified", "removed", "inserted", or "" (no-op).
func spliceHeaderLine(lines []string, declRe *regexp.Regexp, keyword, value string, insertAt int) ([]string, string, string) {
	for i, line := range lines {
		if declRe.MatchString(line) {
			previous := line
			if value == "" {
				newLines := make([]string, 0, len(lines)-1)
				newLines = append(newLines, lines[:i]...)
				newLines = append(newLines, lines[i+1:]...)
				return newLines, previous, "removed"
			}
			newLines := append([]string(nil), lines...)
			newLines[i] = fmt.Sprintf("%s %s", keyword, value)
			return newLines, previous, "modified"
		}
	}
	if value == "" {
		return lines, "", ""
	}
	newLine := fmt.Sprintf("%s %s", keyword, value)
	newLines := make([]string, 0, len(lines)+1)
	newLines = append(newLines, lines[:insertAt]...)
	newLines = append(newLines, newLine)
	newLines = append(newLines, lines[insertAt:]...)
	return newLines, "", "inserted"
}

var (
	scriptIdentityCombinedLineRe = regexp.MustCompile(`^class_name\s+\S+\s+extends\b`)
	scriptIdentityClassNameRe    = regexp.MustCompile(`^class_name\s+\w+\s*$`)
	scriptIdentityExtendsRe      = regexp.MustCompile(`^extends\s+\S.*$`)
)

// spliceScriptIdentity adds, modifies, or removes source's top-level
// class_name and/or extends declarations. className/extends nil-vs-set
// semantics are documented on SetScriptIdentityParams. class_name, when
// inserted, is placed immediately before an existing extends line if one
// exists (else at the very top); extends, when inserted, is placed
// immediately after an existing (or just-inserted) class_name line if one
// exists (else at the very top) — preserving the conventional class_name
// then extends file-header order either way.
func spliceScriptIdentity(source string, className, extends *string) (updated, prevClassName, prevExtends, classNameAction, extendsAction string, err error) {
	if className == nil && extends == nil {
		return "", "", "", "", "", errors.New("at least one of class_name or extends must be set")
	}
	if extends != nil && strings.Contains(*extends, "\n") {
		return "", "", "", "", "", errors.New("extends must not contain a newline")
	}

	lines, ending, trailingNewline := splitPreservingLineEnding(source)

	for _, line := range lines {
		if scriptIdentityCombinedLineRe.MatchString(line) {
			return "", "", "", "", "", errors.New("script's class_name and extends are declared on one combined line (\"class_name X extends Y\"), which this operation does not support; split them onto separate lines first")
		}
	}

	if className != nil {
		insertAt := 0
		for i, line := range lines {
			if scriptIdentityExtendsRe.MatchString(line) {
				insertAt = i
				break
			}
		}
		lines, prevClassName, classNameAction = spliceHeaderLine(lines, scriptIdentityClassNameRe, "class_name", *className, insertAt)
	}

	if extends != nil {
		insertAt := 0
		for i, line := range lines {
			if scriptIdentityClassNameRe.MatchString(line) {
				insertAt = i + 1
				break
			}
		}
		lines, prevExtends, extendsAction = spliceHeaderLine(lines, scriptIdentityExtendsRe, "extends", *extends, insertAt)
	}

	return joinWithLineEnding(lines, ending, trailingNewline), prevClassName, prevExtends, classNameAction, extendsAction, nil
}

// SetScriptIdentity loads an existing .gd script, adds, modifies, or
// removes its class_name and/or extends declaration, verifies the result
// still parses (see checkScriptParses), and writes it back — rolling back
// to the original content if it doesn't.
func (c *Client) SetScriptIdentity(ctx context.Context, params SetScriptIdentityParams) (*SetScriptIdentityResult, error) {
	absPath, err := c.Root.Resolve(params.ScriptPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_identity: %w", err)
	}
	if filepath.Ext(absPath) != ".gd" {
		return nil, fmt.Errorf("headless: set_script_identity: not a .gd file: %s", params.ScriptPath)
	}
	if params.ClassName == nil && params.Extends == nil {
		return nil, errors.New("headless: set_script_identity: at least one of class_name or extends must be set")
	}
	if params.ClassName != nil && *params.ClassName != "" && !validGDScriptIdentifier(*params.ClassName) {
		return nil, fmt.Errorf("headless: set_script_identity: %q is not a valid GDScript identifier", *params.ClassName)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_identity: %w", err)
	}
	original, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_identity: %w", err)
	}

	updated, prevClassName, prevExtends, classNameAction, extendsAction, err := spliceScriptIdentity(string(original), params.ClassName, params.Extends)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_identity: %w", err)
	}

	relPath, err := filepath.Rel(c.Root.String(), absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_identity: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relPath)

	if err := c.writeScriptChecked(ctx, absPath, resPath, original, []byte(updated), info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("headless: set_script_identity: %w", err)
	}

	return &SetScriptIdentityResult{
		Path:              resPath,
		PreviousClassName: prevClassName,
		PreviousExtends:   prevExtends,
		ClassNameAction:   classNameAction,
		ExtendsAction:     extendsAction,
	}, nil
}

// SetFunctionBodyParams are the parameters for the set_function_body
// operation — the advanced tier's only tool (see tools.ModeAdvanced). This
// is the one operation in this server that lets an AI client author or
// replace executable GDScript logic.
type SetFunctionBodyParams struct {
	// ScriptPath is the .gd path, relative to the project root. Validated
	// against Root before use. The script must already exist — this
	// operation never creates a new script file.
	ScriptPath string `json:"script_path" jsonschema:"path to an existing .gd script file, relative to the project root"`
	// FunctionName identifies which top-level function to modify or
	// insert. It is never itself changed by this operation — renaming
	// (updating every call site elsewhere in the project) is a
	// qualitatively bigger, cross-file operation this tool does not
	// attempt.
	FunctionName string `json:"function_name" jsonschema:"the top-level function's name, a valid GDScript identifier; identifies which function to modify or insert, and is never itself changed by this operation"`

	// Parameters and ReturnType are independently optional and only
	// meaningful when modifying an existing function: nil means "leave
	// this part of the existing signature unchanged"; a non-nil value
	// (including a non-nil empty string) replaces it. When inserting a
	// new function, nil is treated as "no parameters" / "no return type
	// annotation" respectively.
	Parameters *string `json:"parameters,omitempty" jsonschema:"verbatim GDScript parameter list text, e.g. \"delta: float\"; must not contain a newline; nil leaves an existing function's parameters unchanged"`
	ReturnType *string `json:"return_type,omitempty" jsonschema:"e.g. \"void\" or \"int\"; omitted entirely from the signature if set to an empty string; nil leaves an existing function's return type unchanged"`

	// Body is the function's new statement lines, unindented relative to
	// the function body's own indent level — one tab of indentation is
	// added automatically per non-blank line. Nested-block indentation
	// within Body (e.g. inside an if/for) is the caller's own
	// responsibility.
	Body string `json:"body" jsonschema:"the function's new body, as GDScript statement lines starting at indent level 0 (one tab of indentation is added automatically per non-blank line)"`
}

// SetFunctionBodyResult confirms a completed function write.
type SetFunctionBodyResult struct {
	// Path is the script's res://-style path.
	Path string `json:"path"`
	// FunctionName is echoed back from the request for confirmation.
	FunctionName string `json:"function_name"`
	// Action is "modified" if a top-level function with this name already
	// existed, or "inserted" if this call added a new one.
	Action string `json:"action"`
	// PreviousSignature and PreviousBody are the function's previous
	// header line and body text, for audit purposes. Both empty when
	// Action is "inserted".
	PreviousSignature string `json:"previous_signature,omitempty"`
	PreviousBody      string `json:"previous_body,omitempty"`
}

var funcHeaderRe = regexp.MustCompile(`^func\s+(\w+)\s*\((.*)\)\s*(?:->\s*(\S.*?))?\s*:$`)

// renderFunctionBodyLines splits body (unindented statement text supplied
// by the caller) into lines, prefixing every non-blank line with one tab
// and leaving blank lines empty.
func renderFunctionBodyLines(body string) []string {
	rawLines := strings.Split(body, "\n")
	out := make([]string, len(rawLines))
	for i, line := range rawLines {
		if line == "" {
			out[i] = ""
			continue
		}
		out[i] = "\t" + line
	}
	return out
}

// spliceFunctionBody replaces an existing top-level function's body (and,
// selectively, its signature) or inserts a new one at the end of the file
// if name doesn't exist yet. See SetFunctionBodyParams's doc comment for
// Parameters/ReturnType's nil-means-unchanged semantics.
func spliceFunctionBody(source, name string, parameters, returnType *string, body string) (updated, previousSignature, previousBody, action string, err error) {
	if parameters != nil && strings.Contains(*parameters, "\n") {
		return "", "", "", "", errors.New("parameters must not contain a newline")
	}

	lines, ending, trailingNewline := splitPreservingLineEnding(source)

	headerRe := regexp.MustCompile(`^func\s+` + regexp.QuoteMeta(name) + `\s*\(`)
	var matches []int
	for i, line := range lines {
		if headerRe.MatchString(line) {
			matches = append(matches, i)
		}
	}
	if len(matches) > 1 {
		return "", "", "", "", fmt.Errorf("%d top-level functions named %q found; this operation refuses to guess which one to edit", len(matches), name)
	}

	newParams := ""
	if parameters != nil {
		newParams = *parameters
	}
	newReturnType := ""
	if returnType != nil {
		newReturnType = *returnType
	}

	if len(matches) == 1 {
		h := matches[0]
		trimmed := strings.TrimRight(lines[h], " \t")
		if !strings.Contains(lines[h], ")") || !strings.HasSuffix(trimmed, ":") {
			return "", "", "", "", fmt.Errorf("function %q's signature does not fit on a single line, which this operation does not support", name)
		}
		m := funcHeaderRe.FindStringSubmatch(trimmed)
		if m == nil {
			return "", "", "", "", fmt.Errorf("could not parse function %q's signature line: %q", name, lines[h])
		}
		existingParams, existingReturnType := m[2], m[3]
		if parameters == nil {
			newParams = existingParams
		}
		if returnType == nil {
			newReturnType = existingReturnType
		}

		e := len(lines)
		for i := h + 1; i < len(lines); i++ {
			line := lines[i]
			trimmedLine := strings.TrimSpace(line)
			isBlank := line == ""
			isCommentOnly := strings.HasPrefix(trimmedLine, "#")
			isIndented := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
			if isBlank || isCommentOnly || isIndented {
				continue
			}
			e = i
			break
		}

		previousSignature = lines[h]
		previousBody = strings.Join(lines[h+1:e], ending)

		newSig := buildFunctionSignature(name, newParams, newReturnType)
		bodyLines := renderFunctionBodyLines(body)

		newLines := make([]string, 0, len(lines)+len(bodyLines))
		newLines = append(newLines, lines[:h]...)
		newLines = append(newLines, newSig)
		newLines = append(newLines, bodyLines...)
		newLines = append(newLines, lines[e:]...)

		return joinWithLineEnding(newLines, ending, trailingNewline), previousSignature, previousBody, "modified", nil
	}

	// Insert: append at the end of the file. A function's position within
	// a class body has no effect on execution, so "append at end" is the
	// simplest, most predictable placement rather than inventing a
	// heuristic nobody asked for.
	newSig := buildFunctionSignature(name, newParams, newReturnType)
	bodyLines := renderFunctionBodyLines(body)

	newLines := make([]string, 0, len(lines)+len(bodyLines)+2)
	newLines = append(newLines, lines...)
	if len(newLines) > 0 && newLines[len(newLines)-1] != "" {
		newLines = append(newLines, "")
	}
	newLines = append(newLines, newSig)
	newLines = append(newLines, bodyLines...)

	return joinWithLineEnding(newLines, ending, trailingNewline), "", "", "inserted", nil
}

func buildFunctionSignature(name, parameters, returnType string) string {
	sig := fmt.Sprintf("func %s(%s)", name, parameters)
	if returnType != "" {
		sig += " -> " + returnType
	}
	return sig + ":"
}

// SetFunctionBody loads an existing .gd script, replaces an existing
// top-level function's body (and, selectively, its signature) or inserts a
// new one, verifies the result still parses (see checkScriptParses), and
// writes it back — rolling back to the original content if it doesn't.
// check-only verification catches syntax errors only, never incorrect
// logic or call sites elsewhere in the project broken by a signature
// change — see SECURITY.md and this operation's tool description.
func (c *Client) SetFunctionBody(ctx context.Context, params SetFunctionBodyParams) (*SetFunctionBodyResult, error) {
	absPath, err := c.Root.Resolve(params.ScriptPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_function_body: %w", err)
	}
	if filepath.Ext(absPath) != ".gd" {
		return nil, fmt.Errorf("headless: set_function_body: not a .gd file: %s", params.ScriptPath)
	}
	if params.FunctionName == "" {
		return nil, errors.New("headless: set_function_body: function_name is required")
	}
	if !validGDScriptIdentifier(params.FunctionName) {
		return nil, fmt.Errorf("headless: set_function_body: %q is not a valid GDScript identifier", params.FunctionName)
	}
	if params.Body == "" {
		return nil, errors.New("headless: set_function_body: body is required")
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_function_body: %w", err)
	}
	original, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_function_body: %w", err)
	}

	updated, previousSignature, previousBody, action, err := spliceFunctionBody(string(original), params.FunctionName, params.Parameters, params.ReturnType, params.Body)
	if err != nil {
		return nil, fmt.Errorf("headless: set_function_body: %w", err)
	}

	relPath, err := filepath.Rel(c.Root.String(), absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_function_body: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relPath)

	if err := c.writeScriptChecked(ctx, absPath, resPath, original, []byte(updated), info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("headless: set_function_body: %w", err)
	}

	return &SetFunctionBodyResult{
		Path:              resPath,
		FunctionName:      params.FunctionName,
		Action:            action,
		PreviousSignature: previousSignature,
		PreviousBody:      previousBody,
	}, nil
}
