package headless

import (
	"strings"
	"testing"
)

// These tests exercise the pure, dependency-free string-splicing functions
// behind set_script_export and set_function_body: no *Client, no
// filesystem, no Godot. See script_edit.go's doc comments for why both
// tools are plain Go text operations rather than routing through
// scripts/godot_operations.gd.

func TestSpliceExportDeclaration_InsertsAfterExtendsWhenNoExportsExist(t *testing.T) {
	source := "extends Node\n\nfunc _ready() -> void:\n\tpass\n"
	updated, previous, action, err := spliceExportDeclaration(source, "health", "int", "100")
	if err != nil {
		t.Fatalf("spliceExportDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q", action, "inserted")
	}
	if previous != "" {
		t.Errorf("previous = %q, want empty", previous)
	}
	want := "extends Node\n@export var health: int = 100\n\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceExportDeclaration_InsertsAfterLastExistingExport(t *testing.T) {
	source := "extends Node\n\n@export var speed: float = 300.0\n@export var jumps: int = 1\n\nfunc _ready() -> void:\n\tpass\n"
	updated, _, action, err := spliceExportDeclaration(source, "health", "int", "100")
	if err != nil {
		t.Fatalf("spliceExportDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q", action, "inserted")
	}
	want := "extends Node\n\n@export var speed: float = 300.0\n@export var jumps: int = 1\n@export var health: int = 100\n\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceExportDeclaration_ModifiesExistingDeclarationInPlace_ReturnsPreviousLine(t *testing.T) {
	source := "extends Node\n\n@export var health: int = 100\n\nfunc _ready() -> void:\n\tpass\n"
	updated, previous, action, err := spliceExportDeclaration(source, "health", "int", "200")
	if err != nil {
		t.Fatalf("spliceExportDeclaration: %v", err)
	}
	if action != "modified" {
		t.Errorf("action = %q, want %q", action, "modified")
	}
	if previous != "@export var health: int = 100" {
		t.Errorf("previous = %q, want the old declaration line", previous)
	}
	want := "extends Node\n\n@export var health: int = 200\n\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceExportDeclaration_DoesNotMatchNameThatIsAPrefix(t *testing.T) {
	source := "extends Node\n\n@export var health_max: int = 999\n"
	updated, _, action, err := spliceExportDeclaration(source, "health", "int", "100")
	if err != nil {
		t.Fatalf("spliceExportDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q (health_max must not match a lookup for health)", action, "inserted")
	}
	if !strings.Contains(updated, "@export var health_max: int = 999") {
		t.Errorf("updated lost the unrelated health_max declaration: %q", updated)
	}
	if !strings.Contains(updated, "@export var health: int = 100") {
		t.Errorf("updated missing the new health declaration: %q", updated)
	}
}

func TestSpliceExportDeclaration_IgnoresIndentedExportInsideNestedClass(t *testing.T) {
	source := "extends Node\n\nclass Helper:\n\t@export var health: int = 1\n\nfunc _ready() -> void:\n\tpass\n"
	updated, _, action, err := spliceExportDeclaration(source, "health", "int", "100")
	if err != nil {
		t.Fatalf("spliceExportDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q (the indented declaration inside Helper must not count as a match)", action, "inserted")
	}
	if !strings.Contains(updated, "\t@export var health: int = 1") {
		t.Errorf("updated lost the nested class's own declaration: %q", updated)
	}
}

func TestSpliceExportDeclaration_PreservesCRLFLineEndings(t *testing.T) {
	source := "extends Node\r\n\r\nfunc _ready() -> void:\r\n\tpass\r\n"
	updated, _, _, err := spliceExportDeclaration(source, "health", "int", "100")
	if err != nil {
		t.Fatalf("spliceExportDeclaration: %v", err)
	}
	if strings.Contains(updated, "\n") && !strings.Contains(updated, "\r\n") {
		t.Fatalf("updated contains a bare \\n despite a CRLF source: %q", updated)
	}
	if !strings.Contains(updated, "@export var health: int = 100\r\n") {
		t.Errorf("updated missing CRLF-terminated new declaration: %q", updated)
	}
}

func TestSpliceExportDeclaration_PreservesAbsentTrailingNewline(t *testing.T) {
	source := "extends Node\n\n@export var health: int = 100"
	updated, _, _, err := spliceExportDeclaration(source, "health", "int", "200")
	if err != nil {
		t.Fatalf("spliceExportDeclaration: %v", err)
	}
	if strings.HasSuffix(updated, "\n") {
		t.Errorf("updated gained a trailing newline the source never had: %q", updated)
	}
}

func TestSpliceExportDeclaration_RejectsSplitAnnotationLine(t *testing.T) {
	source := "extends Node\n\n@export\nvar health: int = 100\n"
	_, _, _, err := spliceExportDeclaration(source, "health", "int", "200")
	if err == nil {
		t.Fatal("spliceExportDeclaration on a split @export/var pair, want error")
	}
}

func TestRenderScriptExportLiteral(t *testing.T) {
	str := "hello \"world\""
	i := int64(-5)
	f := 2.0
	f2 := 2.5
	b := true
	v2 := Vector2{X: 1, Y: 2}
	v3 := Vector3{X: 1, Y: 2, Z: 3}
	c := Color{R: 1, G: 0, B: 0, A: 1}

	tests := []struct {
		name        string
		params      SetScriptExportParams
		wantType    string
		wantLiteral string
	}{
		{"string", SetScriptExportParams{StringValue: &str}, "String", `"hello \"world\""`},
		{"int", SetScriptExportParams{IntValue: &i}, "int", "-5"},
		{"float whole number gets .0", SetScriptExportParams{FloatValue: &f}, "float", "2.0"},
		{"float fractional", SetScriptExportParams{FloatValue: &f2}, "float", "2.5"},
		{"bool", SetScriptExportParams{BoolValue: &b}, "bool", "true"},
		{"vector2", SetScriptExportParams{Vector2Value: &v2}, "Vector2", "Vector2(1.0, 2.0)"},
		{"vector3", SetScriptExportParams{Vector3Value: &v3}, "Vector3", "Vector3(1.0, 2.0, 3.0)"},
		{"color", SetScriptExportParams{ColorValue: &c}, "Color", "Color(1.0, 0.0, 0.0, 1.0)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotLiteral := renderScriptExportLiteral(tt.params)
			if gotType != tt.wantType || gotLiteral != tt.wantLiteral {
				t.Errorf("renderScriptExportLiteral() = (%q, %q), want (%q, %q)", gotType, gotLiteral, tt.wantType, tt.wantLiteral)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func TestSpliceFunctionBody_ReplacesBodyPreservingSignatureWhenNilFields(t *testing.T) {
	source := "extends Node\n\nfunc take_damage(amount: int) -> void:\n\thealth -= amount\n\tprint(health)\n\nfunc other() -> void:\n\tpass\n"
	updated, prevSig, prevBody, action, err := spliceFunctionBody(source, "take_damage", nil, nil, "health -= amount * 2")
	if err != nil {
		t.Fatalf("spliceFunctionBody: %v", err)
	}
	if action != "modified" {
		t.Errorf("action = %q, want %q", action, "modified")
	}
	if prevSig != "func take_damage(amount: int) -> void:" {
		t.Errorf("previousSignature = %q", prevSig)
	}
	if prevBody != "\thealth -= amount\n\tprint(health)\n" {
		t.Errorf("previousBody = %q", prevBody)
	}
	// The blank line separating take_damage from other() is swallowed into
	// take_damage's own body span (blank lines always count as "still
	// inside the body" — see TestSpliceFunctionBody_TreatsBlankAndCommentOnlyLinesAsStillInsideBody),
	// so replacing the body with content that has no trailing blank line
	// of its own naturally removes that separator too.
	want := "extends Node\n\nfunc take_damage(amount: int) -> void:\n\thealth -= amount * 2\nfunc other() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceFunctionBody_ReplacesParametersOnly(t *testing.T) {
	source := "extends Node\n\nfunc take_damage(amount: int) -> void:\n\thealth -= amount\n"
	updated, _, _, _, err := spliceFunctionBody(source, "take_damage", strPtr("amount: int, source: Node"), nil, "health -= amount")
	if err != nil {
		t.Fatalf("spliceFunctionBody: %v", err)
	}
	if !strings.Contains(updated, "func take_damage(amount: int, source: Node) -> void:") {
		t.Errorf("updated missing new parameters with preserved return type: %q", updated)
	}
}

func TestSpliceFunctionBody_ReplacesReturnTypeOnly(t *testing.T) {
	source := "extends Node\n\nfunc take_damage(amount: int) -> void:\n\thealth -= amount\n"
	updated, _, _, _, err := spliceFunctionBody(source, "take_damage", nil, strPtr("bool"), "health -= amount\nreturn true")
	if err != nil {
		t.Fatalf("spliceFunctionBody: %v", err)
	}
	if !strings.Contains(updated, "func take_damage(amount: int) -> bool:") {
		t.Errorf("updated missing new return type with preserved parameters: %q", updated)
	}
}

func TestSpliceFunctionBody_ReplacesBothParametersAndReturnType(t *testing.T) {
	source := "extends Node\n\nfunc take_damage(amount: int) -> void:\n\thealth -= amount\n"
	updated, _, _, _, err := spliceFunctionBody(source, "take_damage", strPtr(""), strPtr(""), "pass")
	if err != nil {
		t.Fatalf("spliceFunctionBody: %v", err)
	}
	if !strings.Contains(updated, "func take_damage() :") && !strings.Contains(updated, "func take_damage():") {
		t.Errorf("updated missing empty-signature rewrite: %q", updated)
	}
}

func TestSpliceFunctionBody_InsertsNewFunctionAtEndOfFile_WithSignature(t *testing.T) {
	source := "extends Node\n\nfunc _ready() -> void:\n\tpass\n"
	updated, prevSig, prevBody, action, err := spliceFunctionBody(source, "attack", strPtr("target: Node"), strPtr("void"), "target.take_damage(10)")
	if err != nil {
		t.Fatalf("spliceFunctionBody: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q", action, "inserted")
	}
	if prevSig != "" || prevBody != "" {
		t.Errorf("previousSignature/previousBody should be empty on insert, got %q / %q", prevSig, prevBody)
	}
	if !strings.Contains(updated, "func attack(target: Node) -> void:\n\ttarget.take_damage(10)") {
		t.Errorf("updated missing inserted function: %q", updated)
	}
	if !strings.Contains(updated, "func _ready() -> void:\n\tpass") {
		t.Errorf("updated lost the existing function: %q", updated)
	}
}

func TestSpliceFunctionBody_InsertsNewFunction_NoParametersNoReturnType(t *testing.T) {
	source := "extends Node\n"
	updated, _, _, action, err := spliceFunctionBody(source, "reset", nil, nil, "pass")
	if err != nil {
		t.Fatalf("spliceFunctionBody: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q", action, "inserted")
	}
	if !strings.Contains(updated, "func reset():\n\tpass") {
		t.Errorf("updated missing bare inserted function: %q", updated)
	}
}

func TestSpliceFunctionBody_IgnoresNestedFuncInsideAnotherClass(t *testing.T) {
	source := "extends Node\n\nclass Helper:\n\tfunc attack() -> void:\n\t\tpass\n\nfunc _ready() -> void:\n\tpass\n"
	updated, _, _, action, err := spliceFunctionBody(source, "attack", nil, nil, "print(\"top-level attack\")")
	if err != nil {
		t.Fatalf("spliceFunctionBody: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q (the nested Helper.attack must not count as a match)", action, "inserted")
	}
	if !strings.Contains(updated, "\tfunc attack() -> void:\n\t\tpass") {
		t.Errorf("updated lost Helper's own nested attack(): %q", updated)
	}
	if !strings.Contains(updated, "func attack():\n\tprint(\"top-level attack\")") {
		t.Errorf("updated missing the new top-level attack(): %q", updated)
	}
}

func TestSpliceFunctionBody_TreatsBlankAndCommentOnlyLinesAsStillInsideBody(t *testing.T) {
	source := "extends Node\n\nfunc foo() -> void:\n\tpass\n\n# a comment\n\nfunc bar() -> void:\n\tpass\n"
	updated, _, prevBody, _, err := spliceFunctionBody(source, "foo", nil, nil, "print(1)")
	if err != nil {
		t.Fatalf("spliceFunctionBody: %v", err)
	}
	if prevBody != "\tpass\n\n# a comment\n" {
		t.Errorf("previousBody = %q, want it to swallow the trailing blank line and comment", prevBody)
	}
	if !strings.Contains(updated, "func bar() -> void:\n\tpass") {
		t.Errorf("updated lost bar(): %q", updated)
	}
}

func TestSpliceFunctionBody_StopsAtNextColumnZeroStatementOrEOF(t *testing.T) {
	source := "extends Node\n\nfunc foo() -> void:\n\tpass\nfunc bar() -> void:\n\tpass\n"
	_, _, prevBody, _, err := spliceFunctionBody(source, "foo", nil, nil, "print(1)")
	if err != nil {
		t.Fatalf("spliceFunctionBody: %v", err)
	}
	if prevBody != "\tpass" {
		t.Errorf("previousBody = %q, want just the one indented line", prevBody)
	}
}

func TestSpliceFunctionBody_RejectsMultiLineSignature(t *testing.T) {
	source := "extends Node\n\nfunc foo(\n\ta: int,\n) -> void:\n\tpass\n"
	_, _, _, _, err := spliceFunctionBody(source, "foo", nil, nil, "print(1)")
	if err == nil {
		t.Fatal("spliceFunctionBody against a multi-line signature, want error")
	}
}

func TestSpliceFunctionBody_RejectsParametersContainingNewline(t *testing.T) {
	source := "extends Node\n\nfunc foo() -> void:\n\tpass\n"
	_, _, _, _, err := spliceFunctionBody(source, "foo", strPtr("a: int,\nb: int"), nil, "print(1)")
	if err == nil {
		t.Fatal("spliceFunctionBody with a newline embedded in parameters, want error")
	}
}

func TestSpliceFunctionBody_RejectsAmbiguousDuplicateName(t *testing.T) {
	source := "extends Node\n\nfunc foo() -> void:\n\tpass\n\nfunc foo() -> void:\n\tpass\n"
	_, _, _, _, err := spliceFunctionBody(source, "foo", nil, nil, "print(1)")
	if err == nil {
		t.Fatal("spliceFunctionBody against a duplicate top-level function name, want error")
	}
}

func TestSpliceFunctionBody_IndentsBodyLinesConsistently_PreservesBlankLinesUnindented(t *testing.T) {
	source := "extends Node\n\nfunc foo() -> void:\n\tpass\n"
	updated, _, _, _, err := spliceFunctionBody(source, "foo", nil, nil, "var x = 1\n\nprint(x)")
	if err != nil {
		t.Fatalf("spliceFunctionBody: %v", err)
	}
	want := "extends Node\n\nfunc foo() -> void:\n\tvar x = 1\n\n\tprint(x)\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}
