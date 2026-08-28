// Restoring resource uid="..." attributes SetNodeProperty's own resave
// drops from a .tscn.
//
// _op_set_node_property's load/instantiate/mutate/pack/save round trip
// (see SetNodeProperty's doc comment) rebuilds the scene's SceneState from
// a live, already-instantiated Node tree via PackedScene.pack(). That has
// no channel for carrying forward a resource's uid="..." attribute — it
// was never part of the Node tree's runtime state, only of the original
// file's own serialized text and Godot's UID cache, neither of which
// pack() from a live tree consults. Confirmed empirically against a real
// Godot 4.7.2 binary: reusing the originally-loaded PackedScene object for
// pack() instead of a fresh one, and calling take_over_path() before
// saving, both produce byte-identical output to the current approach —
// the uid is gone every time, regardless of which Godot object gets
// saved. There is no Godot-side API fix available within this
// architecture, so this is a Go-side text-level restore of exactly what
// Godot dropped, not new content.
package headless

import "regexp"

var (
	gdSceneLineRe     = regexp.MustCompile(`^\[gd_scene\b.*\]$`)
	gdSceneUIDRe      = regexp.MustCompile(`\buid="([^"]*)"`)
	extResourceLineRe = regexp.MustCompile(`^\[ext_resource\b.*\]$`)
	extResourcePathRe = regexp.MustCompile(`\bpath="([^"]*)"`)
	extResourceTypeRe = regexp.MustCompile(`^(\[ext_resource\s+type="[^"]*")(.*)$`)
)

// preserveSceneUIDs restores any uid="..." attribute present in original's
// [gd_scene ...] header or [ext_resource ...] lines that's missing from
// the corresponding line in updated. ext_resource entries are matched by
// their path="..." attribute — the stable identity key, since id=
// numbering isn't guaranteed stable if the resource set changes in the
// same write (e.g. a resource_value on the same call introducing a new
// reference). [sub_resource ...] entries never carry a uid in Godot's own
// format (a sub-resource isn't an independently addressable file), so
// they're never touched. Returns the patched text and whether anything
// actually changed, so the caller can skip a needless rewrite for the
// common case: a scene with no uids to restore at all.
func preserveSceneUIDs(original, updated string) (patched string, changed bool) {
	origLines, _, _ := splitPreservingLineEnding(original)

	var gdSceneUID string
	pathToUID := make(map[string]string)
	for _, line := range origLines {
		if gdSceneLineRe.MatchString(line) {
			if m := gdSceneUIDRe.FindStringSubmatch(line); m != nil {
				gdSceneUID = m[1]
			}
			continue
		}
		if extResourceLineRe.MatchString(line) {
			pathMatch := extResourcePathRe.FindStringSubmatch(line)
			uidMatch := gdSceneUIDRe.FindStringSubmatch(line)
			if pathMatch != nil && uidMatch != nil {
				pathToUID[pathMatch[1]] = uidMatch[1]
			}
		}
	}

	updLines, ending, trailingNewline := splitPreservingLineEnding(updated)
	for i, line := range updLines {
		if gdSceneLineRe.MatchString(line) {
			if gdSceneUID == "" || gdSceneUIDRe.MatchString(line) {
				continue
			}
			updLines[i] = insertBeforeClosingBracket(line, ` uid="`+gdSceneUID+`"`)
			changed = true
			continue
		}
		if extResourceLineRe.MatchString(line) {
			if gdSceneUIDRe.MatchString(line) {
				continue
			}
			pathMatch := extResourcePathRe.FindStringSubmatch(line)
			if pathMatch == nil {
				continue
			}
			uid, ok := pathToUID[pathMatch[1]]
			if !ok {
				continue
			}
			m := extResourceTypeRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			updLines[i] = m[1] + ` uid="` + uid + `"` + m[2]
			changed = true
		}
	}

	if !changed {
		return updated, false
	}
	return joinWithLineEnding(updLines, ending, trailingNewline), true
}

// insertBeforeClosingBracket inserts text immediately before line's final
// "]", assuming line ends with "]" (true for every gd_scene/ext_resource
// header line this function is ever called with).
func insertBeforeClosingBracket(line, text string) string {
	return line[:len(line)-1] + text + "]"
}
