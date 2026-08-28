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
// Vector2Value, Vector3Value, ColorValue must be set — same one-of
// discipline as SetNodePropertyParams, reusing its Vector2/Vector3/Color
// types directly. v1 is scoped to primitives plus Vector2/Vector3/Color,
// mirroring set_node_property's own "primitives, then the three simplest
// structs" phase-in — every other value type needs its own
// GDScript-literal-rendering rule (e.g. a preload() call for a Resource
// default), not just another scalar case; see FEATURES.md for what's
// deferred and why.
type SetScriptExportParams struct {
	// ScriptPath is the .gd path, relative to the project root. Validated
	// against Root before use. The script must already exist — this
	// operation never creates a new script file.
	ScriptPath string `json:"script_path" jsonschema:"path to an existing .gd script file, relative to the project root"`
	// Name is the exported variable's name.
	Name string `json:"name" jsonschema:"the exported variable's name, a valid GDScript identifier"`

	StringValue  *string  `json:"string_value,omitempty" jsonschema:"set the @export var's default to this string value; exactly one of the *_value fields must be set"`
	IntValue     *int64   `json:"int_value,omitempty" jsonschema:"set the @export var's default to this integer value; exactly one of the *_value fields must be set"`
	FloatValue   *float64 `json:"float_value,omitempty" jsonschema:"set the @export var's default to this floating-point value; exactly one of the *_value fields must be set"`
	BoolValue    *bool    `json:"bool_value,omitempty" jsonschema:"set the @export var's default to this boolean value; exactly one of the *_value fields must be set"`
	Vector2Value *Vector2 `json:"vector2_value,omitempty" jsonschema:"set the @export var's default to this Vector2 value; exactly one of the *_value fields must be set"`
	Vector3Value *Vector3 `json:"vector3_value,omitempty" jsonschema:"set the @export var's default to this Vector3 value; exactly one of the *_value fields must be set"`
	ColorValue   *Color   `json:"color_value,omitempty" jsonschema:"set the @export var's default to this Color value; exactly one of the *_value fields must be set"`
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
	}
	return "", ""
}

// spliceExportDeclaration adds or modifies a single top-level
// `@export var <name>: <typeName> = <literal>` declaration in source, a
// .gd script's full text. Only ever matches (or inserts as) a
// column-0 declaration — an indented one (e.g. inside a nested `class`
// block) is a different, intentionally out-of-scope declaration and is
// never touched, matching Array[NodePath]'s own "top-level only" precedent
// in SetNodeProperty.
func spliceExportDeclaration(source, name, typeName, literal string) (updated, previous, action string, err error) {
	lines, ending, trailingNewline := splitPreservingLineEnding(source)
	newLine := fmt.Sprintf("@export var %s: %s = %s", name, typeName, literal)

	declRe := regexp.MustCompile(`^@export\s+var\s+` + regexp.QuoteMeta(name) + `\b`)
	for i, line := range lines {
		if declRe.MatchString(line) {
			previous = line
			lines[i] = newLine
			return joinWithLineEnding(lines, ending, trailingNewline), previous, "modified", nil
		}
	}

	// A legal but unusual split: "@export" alone on one line, "var name"
	// on the next. Refuse rather than guess which line is "the"
	// declaration.
	splitRe := regexp.MustCompile(`^@export\s*$`)
	varRe := regexp.MustCompile(`^var\s+` + regexp.QuoteMeta(name) + `\b`)
	for i, line := range lines {
		if splitRe.MatchString(line) && i+1 < len(lines) && varRe.MatchString(lines[i+1]) {
			return "", "", "", fmt.Errorf("declaration for %q is split across a standalone \"@export\" line and a separate \"var\" line, which this operation does not support", name)
		}
	}

	lastExportRe := regexp.MustCompile(`^@export\s+var\s+\w+`)
	extendsClassRe := regexp.MustCompile(`^(extends|class_name)\b`)
	insertAfter := -1
	for i, line := range lines {
		if lastExportRe.MatchString(line) {
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
	} {
		if set {
			valuesSet++
		}
	}
	if valuesSet != 1 {
		return nil, fmt.Errorf("headless: set_script_export: exactly one of string_value, int_value, float_value, bool_value, vector2_value, vector3_value, color_value must be set, got %d", valuesSet)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_export: %w", err)
	}
	original, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("headless: set_script_export: %w", err)
	}

	typeName, literal := renderScriptExportLiteral(params)
	updated, previous, action, err := spliceExportDeclaration(string(original), params.Name, typeName, literal)
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
