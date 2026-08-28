package headless

import (
	"strings"
	"testing"
)

// These tests exercise preserveSceneUIDs, the pure text-level fix for the
// uid="..." attributes Godot's own PackedScene.pack()-from-a-live-tree
// round trip drops from a resaved .tscn — see scene_uid.go's doc comment
// for how this was confirmed (three different Godot-side approaches were
// tried and all lost the uid identically; there is no Godot API fix).

func TestPreserveSceneUIDs_RestoresMissingGDSceneUID(t *testing.T) {
	original := "[gd_scene load_steps=2 format=3 uid=\"uid://dp18m7gwcwhl0\"]\n\n[node name=\"Main\" type=\"Node\"]\n"
	updated := "[gd_scene format=3]\n\n[node name=\"Main\" type=\"Node\"]\n"

	patched, changed := preserveSceneUIDs(original, updated)
	if !changed {
		t.Fatal("preserveSceneUIDs reported no change, want the gd_scene uid restored")
	}
	if !strings.Contains(patched, `[gd_scene format=3 uid="uid://dp18m7gwcwhl0"]`) {
		t.Errorf("patched missing restored gd_scene uid: %q", patched)
	}
}

func TestPreserveSceneUIDs_RestoresMissingExtResourceUID(t *testing.T) {
	original := "[gd_scene format=3]\n\n" +
		`[ext_resource type="Script" uid="uid://b0i8615afj62o" path="res://Alpha.gd" id="1_8wgy4"]` + "\n\n" +
		"[node name=\"Main\" type=\"Node\"]\n"
	updated := "[gd_scene format=3]\n\n" +
		`[ext_resource type="Script" path="res://Alpha.gd" id="1_8wgy4"]` + "\n\n" +
		"[node name=\"Main\" type=\"Node\"]\n"

	patched, changed := preserveSceneUIDs(original, updated)
	if !changed {
		t.Fatal("preserveSceneUIDs reported no change, want the ext_resource uid restored")
	}
	if !strings.Contains(patched, `[ext_resource type="Script" uid="uid://b0i8615afj62o" path="res://Alpha.gd" id="1_8wgy4"]`) {
		t.Errorf("patched missing restored ext_resource uid: %q", patched)
	}
}

func TestPreserveSceneUIDs_RestoresMultipleExtResourceUIDsIndependently(t *testing.T) {
	original := "[gd_scene format=3]\n\n" +
		`[ext_resource type="Script" uid="uid://aaa" path="res://a.gd" id="1_a"]` + "\n" +
		`[ext_resource type="Texture2D" uid="uid://bbb" path="res://b.png" id="2_b"]` + "\n\n" +
		"[node name=\"Main\" type=\"Node\"]\n"
	updated := "[gd_scene format=3]\n\n" +
		`[ext_resource type="Script" path="res://a.gd" id="1_a"]` + "\n" +
		`[ext_resource type="Texture2D" path="res://b.png" id="2_b"]` + "\n\n" +
		"[node name=\"Main\" type=\"Node\"]\n"

	patched, changed := preserveSceneUIDs(original, updated)
	if !changed {
		t.Fatal("preserveSceneUIDs reported no change, want both ext_resource uids restored")
	}
	if !strings.Contains(patched, `[ext_resource type="Script" uid="uid://aaa" path="res://a.gd" id="1_a"]`) {
		t.Errorf("patched missing restored uid for a.gd: %q", patched)
	}
	if !strings.Contains(patched, `[ext_resource type="Texture2D" uid="uid://bbb" path="res://b.png" id="2_b"]`) {
		t.Errorf("patched missing restored uid for b.png: %q", patched)
	}
}

func TestPreserveSceneUIDs_NoOpWhenOriginalHasNoUIDs(t *testing.T) {
	original := "[gd_scene format=3]\n\n[node name=\"Main\" type=\"Node\"]\n"
	updated := "[gd_scene format=3]\n\n[node name=\"Main\" type=\"Node\" unique_id=123]\n"

	patched, changed := preserveSceneUIDs(original, updated)
	if changed {
		t.Fatalf("preserveSceneUIDs reported a change when original had no uids to restore: %q", patched)
	}
	if patched != updated {
		t.Errorf("patched = %q, want updated unchanged: %q", patched, updated)
	}
}

func TestPreserveSceneUIDs_NoOpWhenUpdatedAlreadyHasTheUID(t *testing.T) {
	original := "[gd_scene format=3 uid=\"uid://dp18m7gwcwhl0\"]\n\n[node name=\"Main\" type=\"Node\"]\n"
	updated := "[gd_scene format=3 uid=\"uid://dp18m7gwcwhl0\"]\n\n[node name=\"Main\" type=\"Node\" unique_id=123]\n"

	patched, changed := preserveSceneUIDs(original, updated)
	if changed {
		t.Fatalf("preserveSceneUIDs reported a change when updated already had the uid: %q", patched)
	}
	if patched != updated {
		t.Errorf("patched = %q, want updated unchanged: %q", patched, updated)
	}
}

func TestPreserveSceneUIDs_LeavesNewExtResourceWithNoOriginalCounterpartAlone(t *testing.T) {
	original := "[gd_scene format=3]\n\n[node name=\"Main\" type=\"Node\"]\n"
	updated := "[gd_scene format=3]\n\n" +
		`[ext_resource type="Texture2D" path="res://new.png" id="1_new"]` + "\n\n" +
		"[node name=\"Main\" type=\"Node\"]\n"

	patched, changed := preserveSceneUIDs(original, updated)
	if changed {
		t.Fatalf("preserveSceneUIDs reported a change for a brand-new ext_resource with nothing to restore: %q", patched)
	}
	if patched != updated {
		t.Errorf("patched = %q, want updated unchanged: %q", patched, updated)
	}
}

func TestPreserveSceneUIDs_NeverTouchesSubResourceLines(t *testing.T) {
	original := "[gd_scene format=3]\n\n" +
		`[sub_resource type="Curve" id="Curve_abc"]` + "\n\n" +
		"[node name=\"Main\" type=\"Node\"]\n"
	updated := original

	patched, changed := preserveSceneUIDs(original, updated)
	if changed {
		t.Fatalf("preserveSceneUIDs reported a change for a sub_resource-only scene: %q", patched)
	}
	if patched != updated {
		t.Errorf("patched = %q, want updated unchanged: %q", patched, updated)
	}
}

func TestPreserveSceneUIDs_PreservesCRLFLineEndings(t *testing.T) {
	original := "[gd_scene load_steps=2 format=3 uid=\"uid://dp18m7gwcwhl0\"]\r\n\r\n[node name=\"Main\" type=\"Node\"]\r\n"
	updated := "[gd_scene format=3]\r\n\r\n[node name=\"Main\" type=\"Node\"]\r\n"

	patched, changed := preserveSceneUIDs(original, updated)
	if !changed {
		t.Fatal("preserveSceneUIDs reported no change, want the gd_scene uid restored")
	}
	if strings.Contains(patched, "\n") && !strings.Contains(patched, "\r\n") {
		t.Fatalf("patched contains a bare \\n despite a CRLF source: %q", patched)
	}
	if !strings.Contains(patched, "[gd_scene format=3 uid=\"uid://dp18m7gwcwhl0\"]\r\n") {
		t.Errorf("patched missing CRLF-terminated restored line: %q", patched)
	}
}
