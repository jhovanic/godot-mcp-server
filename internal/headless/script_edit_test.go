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

// These exercise spliceAnnotatedVarDeclaration directly with the "@onready"
// annotation — spliceExportDeclaration's own tests above already cover the
// "@export" annotation and the shared mechanics (name-prefix guard, nested
// class exclusion, CRLF/trailing-newline preservation, split-annotation
// refusal), so only @onready-specific behavior (its own insertion fallback
// chain) is re-tested here rather than duplicating every case.

func TestSpliceAnnotatedVarDeclaration_OnreadyInsertsAfterExtendsWhenNoneExist(t *testing.T) {
	source := "extends Node\n\nfunc _ready() -> void:\n\tpass\n"
	updated, previous, action, err := spliceAnnotatedVarDeclaration(source, "@onready", "target", "Node", `get_node("Target")`)
	if err != nil {
		t.Fatalf("spliceAnnotatedVarDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q", action, "inserted")
	}
	if previous != "" {
		t.Errorf("previous = %q, want empty", previous)
	}
	want := "extends Node\n@onready var target: Node = get_node(\"Target\")\n\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceAnnotatedVarDeclaration_OnreadyInsertsAfterLastExportWhenNoOnreadyExists(t *testing.T) {
	source := "extends Node\n\n@export var speed: float = 300.0\n\nfunc _ready() -> void:\n\tpass\n"
	updated, _, action, err := spliceAnnotatedVarDeclaration(source, "@onready", "target", "int", "1")
	if err != nil {
		t.Fatalf("spliceAnnotatedVarDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q", action, "inserted")
	}
	want := "extends Node\n\n@export var speed: float = 300.0\n@onready var target: int = 1\n\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceAnnotatedVarDeclaration_OnreadyInsertsAfterLastExistingOnready(t *testing.T) {
	source := "extends Node\n\n@export var speed: float = 300.0\n@onready var a: int = 1\n@onready var b: int = 2\n\nfunc _ready() -> void:\n\tpass\n"
	updated, _, action, err := spliceAnnotatedVarDeclaration(source, "@onready", "target", "int", "3")
	if err != nil {
		t.Fatalf("spliceAnnotatedVarDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q", action, "inserted")
	}
	want := "extends Node\n\n@export var speed: float = 300.0\n@onready var a: int = 1\n@onready var b: int = 2\n@onready var target: int = 3\n\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceAnnotatedVarDeclaration_OnreadyModifiesExistingInPlace(t *testing.T) {
	source := "extends Node\n\n@onready var target: int = 1\n"
	updated, previous, action, err := spliceAnnotatedVarDeclaration(source, "@onready", "target", "int", "2")
	if err != nil {
		t.Fatalf("spliceAnnotatedVarDeclaration: %v", err)
	}
	if action != "modified" {
		t.Errorf("action = %q, want %q", action, "modified")
	}
	if previous != "@onready var target: int = 1" {
		t.Errorf("previous = %q, want the old declaration line", previous)
	}
	want := "extends Node\n\n@onready var target: int = 2\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceAnnotatedVarDeclaration_OnreadyDoesNotMatchExportOfSameName(t *testing.T) {
	source := "extends Node\n\n@export var target: int = 1\n"
	updated, _, action, err := spliceAnnotatedVarDeclaration(source, "@onready", "target", "int", "2")
	if err != nil {
		t.Fatalf("spliceAnnotatedVarDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q (an @export var of the same name must not count as a match)", action, "inserted")
	}
	if !strings.Contains(updated, "@export var target: int = 1") || !strings.Contains(updated, "@onready var target: int = 2") {
		t.Errorf("updated missing one of the two same-named declarations: %q", updated)
	}
}

func TestSpliceAnnotatedVarDeclaration_OnreadyRejectsSplitAnnotationLine(t *testing.T) {
	source := "extends Node\n\n@onready\nvar target: int = 1\n"
	_, _, _, err := spliceAnnotatedVarDeclaration(source, "@onready", "target", "int", "2")
	if err == nil {
		t.Fatal("spliceAnnotatedVarDeclaration on a split @onready/var pair, want error")
	}
}

func TestSpliceAnnotatedVarDeclaration_OnreadyPreservesCRLFLineEndings(t *testing.T) {
	source := "extends Node\r\n\r\nfunc _ready() -> void:\r\n\tpass\r\n"
	updated, _, _, err := spliceAnnotatedVarDeclaration(source, "@onready", "target", "int", "1")
	if err != nil {
		t.Fatalf("spliceAnnotatedVarDeclaration: %v", err)
	}
	if strings.Contains(updated, "\n") && !strings.Contains(updated, "\r\n") {
		t.Fatalf("updated contains a bare \\n despite a CRLF source: %q", updated)
	}
	if !strings.Contains(updated, "@onready var target: int = 1\r\n") {
		t.Errorf("updated missing CRLF-terminated new declaration: %q", updated)
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
	v2i := Vector2i{X: 1, Y: 2}
	v3i := Vector3i{X: 1, Y: 2, Z: 3}
	q := Quaternion{X: 0, Y: 0, Z: 0, W: 1}
	r2 := Rect2{Position: Vector2{X: 1, Y: 2}, Size: Vector2{X: 3, Y: 4}}
	r2i := Rect2i{Position: Vector2i{X: 1, Y: 2}, Size: Vector2i{X: 3, Y: 4}}
	pl := Plane{X: 0, Y: 1, Z: 0, D: 5}
	box := AABB{Position: Vector3{X: 0, Y: 0, Z: 0}, Size: Vector3{X: 1, Y: 1, Z: 1}}
	basis := Basis{X: Vector3{X: 1, Y: 0, Z: 0}, Y: Vector3{X: 0, Y: 1, Z: 0}, Z: Vector3{X: 0, Y: 0, Z: 1}}
	t2d := Transform2D{X: Vector2{X: 1, Y: 0}, Y: Vector2{X: 0, Y: 1}, Origin: Vector2{X: 0, Y: 0}}
	t3d := Transform3D{Basis: basis, Origin: Vector3{X: 0, Y: 0, Z: 0}}
	np := "../Target"

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
		{"vector2i", SetScriptExportParams{Vector2iValue: &v2i}, "Vector2i", "Vector2i(1, 2)"},
		{"vector3i", SetScriptExportParams{Vector3iValue: &v3i}, "Vector3i", "Vector3i(1, 2, 3)"},
		{"quaternion", SetScriptExportParams{QuaternionValue: &q}, "Quaternion", "Quaternion(0.0, 0.0, 0.0, 1.0)"},
		{"rect2", SetScriptExportParams{Rect2Value: &r2}, "Rect2", "Rect2(1.0, 2.0, 3.0, 4.0)"},
		{"rect2i", SetScriptExportParams{Rect2iValue: &r2i}, "Rect2i", "Rect2i(1, 2, 3, 4)"},
		{"plane", SetScriptExportParams{PlaneValue: &pl}, "Plane", "Plane(0.0, 1.0, 0.0, 5.0)"},
		{"aabb", SetScriptExportParams{AABBValue: &box}, "AABB", "AABB(Vector3(0.0, 0.0, 0.0), Vector3(1.0, 1.0, 1.0))"},
		{"basis", SetScriptExportParams{BasisValue: &basis}, "Basis", "Basis(Vector3(1.0, 0.0, 0.0), Vector3(0.0, 1.0, 0.0), Vector3(0.0, 0.0, 1.0))"},
		{"transform2d", SetScriptExportParams{Transform2DValue: &t2d}, "Transform2D", "Transform2D(Vector2(1.0, 0.0), Vector2(0.0, 1.0), Vector2(0.0, 0.0))"},
		{"transform3d", SetScriptExportParams{Transform3DValue: &t3d}, "Transform3D", "Transform3D(Basis(Vector3(1.0, 0.0, 0.0), Vector3(0.0, 1.0, 0.0), Vector3(0.0, 0.0, 1.0)), Vector3(0.0, 0.0, 0.0))"},
		{"node_path", SetScriptExportParams{NodePathValue: &np}, "NodePath", `NodePath("../Target")`},
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

// spliceSignalDeclaration backs the set_script_signal tool.

func TestSpliceSignalDeclaration_InsertsBareSignalAfterExtendsWhenNoneExist(t *testing.T) {
	source := "extends Node\n\nfunc _ready() -> void:\n\tpass\n"
	updated, previous, action, err := spliceSignalDeclaration(source, "died", nil)
	if err != nil {
		t.Fatalf("spliceSignalDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q", action, "inserted")
	}
	if previous != "" {
		t.Errorf("previous = %q, want empty", previous)
	}
	want := "extends Node\nsignal died\n\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceSignalDeclaration_InsertsWithParametersAfterLastExistingSignal(t *testing.T) {
	source := "extends Node\n\nsignal died\nsignal spawned\n\nfunc _ready() -> void:\n\tpass\n"
	updated, _, action, err := spliceSignalDeclaration(source, "damaged", strPtr("amount: int"))
	if err != nil {
		t.Fatalf("spliceSignalDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q", action, "inserted")
	}
	want := "extends Node\n\nsignal died\nsignal spawned\nsignal damaged(amount: int)\n\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceSignalDeclaration_InsertsEmptyParameterList(t *testing.T) {
	source := "extends Node\n"
	updated, _, _, err := spliceSignalDeclaration(source, "died", strPtr(""))
	if err != nil {
		t.Fatalf("spliceSignalDeclaration: %v", err)
	}
	if !strings.Contains(updated, "signal died()") {
		t.Errorf("updated missing explicit empty parameter list: %q", updated)
	}
}

func TestSpliceSignalDeclaration_ModifiesExistingDeclarationInPlace(t *testing.T) {
	source := "extends Node\n\nsignal damaged(amount: int)\n"
	updated, previous, action, err := spliceSignalDeclaration(source, "damaged", strPtr("amount: int, source: Node"))
	if err != nil {
		t.Fatalf("spliceSignalDeclaration: %v", err)
	}
	if action != "modified" {
		t.Errorf("action = %q, want %q", action, "modified")
	}
	if previous != "signal damaged(amount: int)" {
		t.Errorf("previous = %q, want the old declaration line", previous)
	}
	want := "extends Node\n\nsignal damaged(amount: int, source: Node)\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceSignalDeclaration_DoesNotMatchNameThatIsAPrefix(t *testing.T) {
	source := "extends Node\n\nsignal died_early\n"
	updated, _, action, err := spliceSignalDeclaration(source, "died", nil)
	if err != nil {
		t.Fatalf("spliceSignalDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q (died_early must not match a lookup for died)", action, "inserted")
	}
	if !strings.Contains(updated, "signal died_early") || !strings.Contains(updated, "signal died\n") {
		t.Errorf("updated missing one of the two distinct signals: %q", updated)
	}
}

func TestSpliceSignalDeclaration_IgnoresIndentedSignalInsideNestedClass(t *testing.T) {
	source := "extends Node\n\nclass Helper:\n\tsignal died\n\nfunc _ready() -> void:\n\tpass\n"
	updated, _, action, err := spliceSignalDeclaration(source, "died", nil)
	if err != nil {
		t.Fatalf("spliceSignalDeclaration: %v", err)
	}
	if action != "inserted" {
		t.Errorf("action = %q, want %q (the indented signal inside Helper must not count as a match)", action, "inserted")
	}
	if !strings.Contains(updated, "\tsignal died\n") {
		t.Errorf("updated lost the nested class's own signal: %q", updated)
	}
}

func TestSpliceSignalDeclaration_PreservesCRLFLineEndings(t *testing.T) {
	source := "extends Node\r\n\r\nfunc _ready() -> void:\r\n\tpass\r\n"
	updated, _, _, err := spliceSignalDeclaration(source, "died", nil)
	if err != nil {
		t.Fatalf("spliceSignalDeclaration: %v", err)
	}
	if strings.Contains(updated, "\n") && !strings.Contains(updated, "\r\n") {
		t.Fatalf("updated contains a bare \\n despite a CRLF source: %q", updated)
	}
	if !strings.Contains(updated, "signal died\r\n") {
		t.Errorf("updated missing CRLF-terminated new declaration: %q", updated)
	}
}

func TestSpliceSignalDeclaration_PreservesAbsentTrailingNewline(t *testing.T) {
	source := "extends Node\n\nsignal died"
	updated, _, _, err := spliceSignalDeclaration(source, "died", strPtr("cause: String"))
	if err != nil {
		t.Fatalf("spliceSignalDeclaration: %v", err)
	}
	if strings.HasSuffix(updated, "\n") {
		t.Errorf("updated gained a trailing newline the source never had: %q", updated)
	}
}

// spliceScriptIdentity backs the set_script_identity tool.

func TestSpliceScriptIdentity_InsertsClassNameOnlyAtTop(t *testing.T) {
	source := "extends Node\n\nfunc _ready() -> void:\n\tpass\n"
	updated, prevClassName, prevExtends, classAction, extendsAction, err := spliceScriptIdentity(source, strPtr("Player"), nil)
	if err != nil {
		t.Fatalf("spliceScriptIdentity: %v", err)
	}
	if classAction != "inserted" || extendsAction != "" {
		t.Errorf("classAction = %q, extendsAction = %q, want %q and empty", classAction, extendsAction, "inserted")
	}
	if prevClassName != "" || prevExtends != "" {
		t.Errorf("prevClassName/prevExtends should be empty, got %q / %q", prevClassName, prevExtends)
	}
	want := "class_name Player\nextends Node\n\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceScriptIdentity_InsertsExtendsOnlyWhenAbsent(t *testing.T) {
	source := "func _ready() -> void:\n\tpass\n"
	updated, _, _, _, extendsAction, err := spliceScriptIdentity(source, nil, strPtr("Node2D"))
	if err != nil {
		t.Fatalf("spliceScriptIdentity: %v", err)
	}
	if extendsAction != "inserted" {
		t.Errorf("extendsAction = %q, want %q", extendsAction, "inserted")
	}
	want := "extends Node2D\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceScriptIdentity_InsertsBothOnEmptyHeaderInConventionalOrder(t *testing.T) {
	source := "func _ready() -> void:\n\tpass\n"
	updated, _, _, classAction, extendsAction, err := spliceScriptIdentity(source, strPtr("Player"), strPtr("Node2D"))
	if err != nil {
		t.Fatalf("spliceScriptIdentity: %v", err)
	}
	if classAction != "inserted" || extendsAction != "inserted" {
		t.Errorf("classAction = %q, extendsAction = %q, want both %q", classAction, extendsAction, "inserted")
	}
	want := "class_name Player\nextends Node2D\nfunc _ready() -> void:\n\tpass\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceScriptIdentity_ModifiesExistingClassNameInPlace(t *testing.T) {
	source := "class_name OldName\nextends Node\n"
	updated, prevClassName, _, classAction, extendsAction, err := spliceScriptIdentity(source, strPtr("NewName"), nil)
	if err != nil {
		t.Fatalf("spliceScriptIdentity: %v", err)
	}
	if classAction != "modified" || extendsAction != "" {
		t.Errorf("classAction = %q, extendsAction = %q, want %q and empty", classAction, extendsAction, "modified")
	}
	if prevClassName != "class_name OldName" {
		t.Errorf("prevClassName = %q", prevClassName)
	}
	want := "class_name NewName\nextends Node\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceScriptIdentity_ModifiesExistingExtendsInPlace(t *testing.T) {
	source := "extends Node\n"
	updated, _, prevExtends, _, extendsAction, err := spliceScriptIdentity(source, nil, strPtr("Node2D"))
	if err != nil {
		t.Fatalf("spliceScriptIdentity: %v", err)
	}
	if extendsAction != "modified" {
		t.Errorf("extendsAction = %q, want %q", extendsAction, "modified")
	}
	if prevExtends != "extends Node" {
		t.Errorf("prevExtends = %q", prevExtends)
	}
	want := "extends Node2D\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceScriptIdentity_RemovesClassNameOnEmptyString(t *testing.T) {
	source := "class_name Player\nextends Node\n"
	updated, prevClassName, _, classAction, extendsAction, err := spliceScriptIdentity(source, strPtr(""), nil)
	if err != nil {
		t.Fatalf("spliceScriptIdentity: %v", err)
	}
	if classAction != "removed" || extendsAction != "" {
		t.Errorf("classAction = %q, extendsAction = %q, want %q and empty", classAction, extendsAction, "removed")
	}
	if prevClassName != "class_name Player" {
		t.Errorf("prevClassName = %q", prevClassName)
	}
	want := "extends Node\n"
	if updated != want {
		t.Errorf("updated =\n%q\nwant\n%q", updated, want)
	}
}

func TestSpliceScriptIdentity_RemovingAbsentClassNameIsANoop(t *testing.T) {
	source := "extends Node\n"
	updated, prevClassName, _, classAction, _, err := spliceScriptIdentity(source, strPtr(""), nil)
	if err != nil {
		t.Fatalf("spliceScriptIdentity: %v", err)
	}
	if classAction != "" {
		t.Errorf("classAction = %q, want empty (nothing to remove)", classAction)
	}
	if prevClassName != "" {
		t.Errorf("prevClassName = %q, want empty", prevClassName)
	}
	if updated != source {
		t.Errorf("updated =\n%q\nwant unchanged\n%q", updated, source)
	}
}

func TestSpliceScriptIdentity_RejectsWhenBothNil(t *testing.T) {
	source := "extends Node\n"
	_, _, _, _, _, err := spliceScriptIdentity(source, nil, nil)
	if err == nil {
		t.Fatal("spliceScriptIdentity with both class_name and extends nil, want error")
	}
}

func TestSpliceScriptIdentity_RejectsCombinedOneLineForm(t *testing.T) {
	source := "class_name Player extends Node\n"
	_, _, _, _, _, err := spliceScriptIdentity(source, strPtr("Player2"), nil)
	if err == nil {
		t.Fatal("spliceScriptIdentity against a combined \"class_name X extends Y\" line, want error")
	}
}

func TestSpliceScriptIdentity_PreservesCRLFLineEndings(t *testing.T) {
	source := "extends Node\r\n\r\nfunc _ready() -> void:\r\n\tpass\r\n"
	updated, _, _, _, _, err := spliceScriptIdentity(source, strPtr("Player"), nil)
	if err != nil {
		t.Fatalf("spliceScriptIdentity: %v", err)
	}
	if strings.Contains(updated, "\n") && !strings.Contains(updated, "\r\n") {
		t.Fatalf("updated contains a bare \\n despite a CRLF source: %q", updated)
	}
	if !strings.Contains(updated, "class_name Player\r\n") {
		t.Errorf("updated missing CRLF-terminated new declaration: %q", updated)
	}
}
