## Fixed, single-entry-point operations script for the headless CLI tier.
##
## This script is intentionally thin: JSON in, dispatch to one of a fixed set
## of named operation functions, JSON out. It performs no path validation and
## no authorization decisions of its own — those happen in Go
## (internal/validate, internal/headless) before this process is ever
## started. This script only ever receives a single "res://..." path that
## the Go layer has already confirmed resolves inside the project root.
##
## There is no `eval`, no dynamic script loading, and no branch here that
## executes anything other than the operation named in the fixed `match`
## below. Adding a new operation means adding a new named case here and a
## corresponding typed caller in internal/headless — never a generic
## "run this" case. See CLAUDE.md / SECURITY.md.
extends SceneTree


func _init() -> void:
	var response := _dispatch(_read_request())
	print(JSON.stringify(response))
	quit()


## Reads the "--json <payload>" argument passed after `--` on the command
## line and parses it. Returns a Dictionary with an "operation" key and a
## "params" key, or null if the payload was missing/malformed.
func _read_request() -> Variant:
	var args := OS.get_cmdline_user_args()
	var payload := ""
	for i in args.size():
		if args[i] == "--json" and i + 1 < args.size():
			payload = args[i + 1]
			break

	if payload.is_empty():
		return null

	var parsed: Variant = JSON.parse_string(payload)
	if typeof(parsed) != TYPE_DICTIONARY:
		return null
	return parsed


## Dispatches to a fixed, named operation. Unknown operations are rejected;
## nothing here interprets `operation` as anything other than a lookup key
## into this fixed set of cases.
func _dispatch(request: Variant) -> Dictionary:
	if request == null:
		return _err("invalid or missing --json request payload")

	var operation: String = request.get("operation", "")
	var params: Variant = request.get("params", {})

	match operation:
		"read_scene_tree":
			return _op_read_scene_tree(params)
		"read_binary_resource":
			return _op_read_binary_resource(params)
		"set_node_property":
			return _op_set_node_property(params)
		_:
			return _err("unknown operation: %s" % operation)


## read_scene_tree: loads a .tscn file (already-validated res:// path) and
## returns its node tree as name/type/children. Read-only — does not modify
## the scene resource or write anything to disk.
func _op_read_scene_tree(params: Variant) -> Dictionary:
	if typeof(params) != TYPE_DICTIONARY or not params.has("path"):
		return _err("read_scene_tree: missing \"path\" param")

	var path: String = params["path"]
	if not path.begins_with("res://"):
		return _err("read_scene_tree: path must be a res:// path")

	if not ResourceLoader.exists(path, "PackedScene"):
		return _err("read_scene_tree: no scene resource at %s" % path)

	var packed: PackedScene = load(path)
	if packed == null:
		return _err("read_scene_tree: failed to load %s" % path)

	var instance: Node = packed.instantiate()
	if instance == null:
		return _err("read_scene_tree: failed to instantiate %s" % path)

	var tree := _node_to_dict(instance)
	instance.free()

	return {"ok": true, "result": tree}


func _node_to_dict(node: Node) -> Dictionary:
	var children: Array = []
	for child in node.get_children():
		children.append(_node_to_dict(child))

	var out := {
		"name": node.name as String,
		"type": node.get_class(),
	}
	if not children.is_empty():
		out["children"] = children
	return out


## read_binary_resource: loads a .res binary resource (already-validated
## res:// path) and re-serializes it to a .tres text file at out_path — a
## Go-generated absolute path outside the project root, never something
## this script chooses or the AI client sees (see
## internal/headless.Client.ReadBinaryResource, which reads that file back
## and deletes it). Read-only with respect to the project: does not modify
## the .res file or anything else inside it. There is no in-memory
## "serialize this Resource to a string" API in GDScript, only the
## file-based ResourceSaver.save() used here.
func _op_read_binary_resource(params: Variant) -> Dictionary:
	if typeof(params) != TYPE_DICTIONARY or not params.has("path") or not params.has("out_path"):
		return _err("read_binary_resource: missing \"path\" or \"out_path\" param")

	var path: String = params["path"]
	var out_path: String = params["out_path"]
	if not path.begins_with("res://"):
		return _err("read_binary_resource: path must be a res:// path")

	if not ResourceLoader.exists(path):
		return _err("read_binary_resource: no resource at %s" % path)

	var resource: Resource = load(path)
	if resource == null:
		return _err("read_binary_resource: failed to load %s" % path)

	var err := ResourceSaver.save(resource, out_path)
	if err != OK:
		return _err("read_binary_resource: failed to re-serialize %s as text (error code %d)" % [path, err])

	return {"ok": true}


## set_node_property: loads a .tscn file (already-validated res:// path),
## sets exactly one property (string/int/float/bool/Vector2/Color — the
## caller sends exactly one of string_value/int_value/float_value/bool_value/
## vector2_value/color_value) on one node addressed by node_path (relative to
## the scene root; empty string means the root itself), then re-packs and
## saves the scene.
##
## Object.set() silently no-ops on an unknown property name instead of
## erroring, so this reads the property back after setting it and only
## saves if the value actually changed to the requested one — a mistyped
## property_name or a type Godot can't coerce is reported as an error here,
## never written as a no-op.
func _op_set_node_property(params: Variant) -> Dictionary:
	if typeof(params) != TYPE_DICTIONARY or not params.has("path") or not params.has("node_path") or not params.has("property_name"):
		return _err("set_node_property: missing \"path\", \"node_path\" or \"property_name\" param")

	var path: String = params["path"]
	var node_path: String = params["node_path"]
	var property_name: String = params["property_name"]
	if not path.begins_with("res://"):
		return _err("set_node_property: path must be a res:// path")

	var value: Variant = null
	var values_set := 0
	if params.get("string_value") != null:
		value = str(params["string_value"])
		values_set += 1
	if params.get("int_value") != null:
		value = int(params["int_value"])
		values_set += 1
	if params.get("float_value") != null:
		value = float(params["float_value"])
		values_set += 1
	if params.get("bool_value") != null:
		value = bool(params["bool_value"])
		values_set += 1
	if params.get("vector2_value") != null:
		var v: Dictionary = params["vector2_value"]
		value = Vector2(float(v.get("x", 0.0)), float(v.get("y", 0.0)))
		values_set += 1
	if params.get("color_value") != null:
		var c: Dictionary = params["color_value"]
		value = Color(float(c.get("r", 0.0)), float(c.get("g", 0.0)), float(c.get("b", 0.0)), float(c.get("a", 1.0)))
		values_set += 1
	if values_set != 1:
		return _err("set_node_property: exactly one of string_value/int_value/float_value/bool_value/vector2_value/color_value must be set")

	if not ResourceLoader.exists(path, "PackedScene"):
		return _err("set_node_property: no scene resource at %s" % path)

	var packed: PackedScene = load(path)
	if packed == null:
		return _err("set_node_property: failed to load %s" % path)

	var root: Node = packed.instantiate()
	if root == null:
		return _err("set_node_property: failed to instantiate %s" % path)

	var target: Node = root
	if node_path != "":
		target = root.get_node_or_null(NodePath(node_path))
	if target == null:
		root.free()
		return _err("set_node_property: no node at %s" % node_path)

	var previous: Variant = target.get(property_name)
	target.set(property_name, value)
	var actual: Variant = target.get(property_name)
	if actual != value:
		root.free()
		return _err("set_node_property: setting %s on %s did not take effect (got %s, want %s) — likely an unknown property name or a type Godot couldn't coerce" % [property_name, target.get_class(), actual, value])

	var new_packed := PackedScene.new()
	var pack_err := new_packed.pack(root)
	root.free()
	if pack_err != OK:
		return _err("set_node_property: failed to re-pack scene (error code %d)" % pack_err)

	var save_err := ResourceSaver.save(new_packed, path)
	if save_err != OK:
		return _err("set_node_property: failed to save %s (error code %d)" % [path, save_err])

	return {"ok": true, "result": {"previous_value": str(previous)}}


func _err(message: String) -> Dictionary:
	return {"ok": false, "error": message}
