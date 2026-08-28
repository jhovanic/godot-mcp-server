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
		"add_node":
			return _op_add_node(params)
		"remove_node":
			return _op_remove_node(params)
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
## sets exactly one property (string/int/float/bool/Vector2/Vector3/Color/
## Vector2i/Vector3i/Quaternion/Rect2/Rect2i/Plane/AABB/Basis/Transform2D/
## Transform3D/NodePath/PackedStringArray/PackedInt32Array/
## PackedFloat32Array/PackedVector2Array/PackedColorArray/
## PackedVector3Array/Array[NodePath]/Resource/Array[String]/Array[int]/
## Array[float]/Array[Vector2]/Array[Color]/Array[Vector3]/Array[T] where T
## is a Resource subclass — the caller sends exactly one of string_value/
## int_value/float_value/bool_value/vector2_value/vector3_value/
## color_value/vector2i_value/vector3i_value/quaternion_value/rect2_value/
## rect2i_value/plane_value/aabb_value/basis_value/transform2d_value/
## transform3d_value/node_path_value/string_array_value/int_array_value/
## float_array_value/vector2_array_value/color_array_value/
## vector3_array_value/node_path_array_value/resource_value/
## typed_string_array_value/typed_int_array_value/typed_float_array_value/
## typed_vector2_array_value/typed_color_array_value/
## typed_vector3_array_value/typed_resource_array_value) on one node
## addressed by node_path (relative to the scene root; empty string means
## the root itself), then re-packs and saves the scene.
##
## property_name "script" is refused unconditionally, regardless of which
## *_value field is sent: assigning a Script is code execution, not a data
## write (see CLAUDE.md's first hard constraint).
##
## Object.set() silently no-ops on an unknown property name instead of
## erroring, so this reads the property back after setting it and only
## saves if the value actually changed to the requested one — a mistyped
## property_name or a type Godot can't coerce is reported as an error here,
## never written as a no-op. resource_value additionally gets its class
## checked against the property's declared type before set() is ever
## called, since Object.set() does not itself enforce that a Resource-typed
## property receives a compatible class.
func _op_set_node_property(params: Variant) -> Dictionary:
	if typeof(params) != TYPE_DICTIONARY or not params.has("path") or not params.has("node_path") or not params.has("property_name"):
		return _err("set_node_property: missing \"path\", \"node_path\" or \"property_name\" param")

	var path: String = params["path"]
	var node_path: String = params["node_path"]
	var property_name: String = params["property_name"]
	if not path.begins_with("res://"):
		return _err("set_node_property: path must be a res:// path")
	# Defense in depth alongside the Go-side check in
	# headless.Client.SetNodeProperty: assigning a Script is code execution
	# by another name, not a data write, so this operation must never write
	# it regardless of what calls this script directly.
	if property_name == "script":
		return _err("set_node_property: refusing to set \"script\": assigning a Script is not permitted")

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
	if params.get("vector3_value") != null:
		var v3: Dictionary = params["vector3_value"]
		value = Vector3(float(v3.get("x", 0.0)), float(v3.get("y", 0.0)), float(v3.get("z", 0.0)))
		values_set += 1
	if params.get("color_value") != null:
		var c: Dictionary = params["color_value"]
		value = Color(float(c.get("r", 0.0)), float(c.get("g", 0.0)), float(c.get("b", 0.0)), float(c.get("a", 1.0)))
		values_set += 1
	if params.get("vector2i_value") != null:
		var v2i: Dictionary = params["vector2i_value"]
		value = Vector2i(int(v2i.get("x", 0)), int(v2i.get("y", 0)))
		values_set += 1
	if params.get("vector3i_value") != null:
		var v3i: Dictionary = params["vector3i_value"]
		value = Vector3i(int(v3i.get("x", 0)), int(v3i.get("y", 0)), int(v3i.get("z", 0)))
		values_set += 1
	if params.get("quaternion_value") != null:
		var q: Dictionary = params["quaternion_value"]
		value = Quaternion(float(q.get("x", 0.0)), float(q.get("y", 0.0)), float(q.get("z", 0.0)), float(q.get("w", 1.0)))
		values_set += 1
	if params.get("rect2_value") != null:
		var r2: Dictionary = params["rect2_value"]
		var r2_pos: Dictionary = r2.get("position", {})
		var r2_size: Dictionary = r2.get("size", {})
		value = Rect2(float(r2_pos.get("x", 0.0)), float(r2_pos.get("y", 0.0)), float(r2_size.get("x", 0.0)), float(r2_size.get("y", 0.0)))
		values_set += 1
	if params.get("rect2i_value") != null:
		var r2i: Dictionary = params["rect2i_value"]
		var r2i_pos: Dictionary = r2i.get("position", {})
		var r2i_size: Dictionary = r2i.get("size", {})
		value = Rect2i(int(r2i_pos.get("x", 0)), int(r2i_pos.get("y", 0)), int(r2i_size.get("x", 0)), int(r2i_size.get("y", 0)))
		values_set += 1
	if params.get("plane_value") != null:
		var pl: Dictionary = params["plane_value"]
		value = Plane(float(pl.get("x", 0.0)), float(pl.get("y", 0.0)), float(pl.get("z", 0.0)), float(pl.get("d", 0.0)))
		values_set += 1
	if params.get("aabb_value") != null:
		var box: Dictionary = params["aabb_value"]
		var box_pos: Dictionary = box.get("position", {})
		var box_size: Dictionary = box.get("size", {})
		value = AABB(
			Vector3(float(box_pos.get("x", 0.0)), float(box_pos.get("y", 0.0)), float(box_pos.get("z", 0.0))),
			Vector3(float(box_size.get("x", 0.0)), float(box_size.get("y", 0.0)), float(box_size.get("z", 0.0)))
		)
		values_set += 1
	if params.get("basis_value") != null:
		var basis_dict: Dictionary = params["basis_value"]
		var bx: Dictionary = basis_dict.get("x", {})
		var by: Dictionary = basis_dict.get("y", {})
		var bz: Dictionary = basis_dict.get("z", {})
		value = Basis(
			Vector3(float(bx.get("x", 0.0)), float(bx.get("y", 0.0)), float(bx.get("z", 0.0))),
			Vector3(float(by.get("x", 0.0)), float(by.get("y", 0.0)), float(by.get("z", 0.0))),
			Vector3(float(bz.get("x", 0.0)), float(bz.get("y", 0.0)), float(bz.get("z", 0.0)))
		)
		values_set += 1
	if params.get("transform2d_value") != null:
		var t2d: Dictionary = params["transform2d_value"]
		var t2d_x: Dictionary = t2d.get("x", {})
		var t2d_y: Dictionary = t2d.get("y", {})
		var t2d_origin: Dictionary = t2d.get("origin", {})
		value = Transform2D(
			Vector2(float(t2d_x.get("x", 0.0)), float(t2d_x.get("y", 0.0))),
			Vector2(float(t2d_y.get("x", 0.0)), float(t2d_y.get("y", 0.0))),
			Vector2(float(t2d_origin.get("x", 0.0)), float(t2d_origin.get("y", 0.0)))
		)
		values_set += 1
	if params.get("transform3d_value") != null:
		var t3d: Dictionary = params["transform3d_value"]
		var t3d_basis: Dictionary = t3d.get("basis", {})
		var t3d_bx: Dictionary = t3d_basis.get("x", {})
		var t3d_by: Dictionary = t3d_basis.get("y", {})
		var t3d_bz: Dictionary = t3d_basis.get("z", {})
		var t3d_origin: Dictionary = t3d.get("origin", {})
		value = Transform3D(
			Basis(
				Vector3(float(t3d_bx.get("x", 0.0)), float(t3d_bx.get("y", 0.0)), float(t3d_bx.get("z", 0.0))),
				Vector3(float(t3d_by.get("x", 0.0)), float(t3d_by.get("y", 0.0)), float(t3d_by.get("z", 0.0))),
				Vector3(float(t3d_bz.get("x", 0.0)), float(t3d_bz.get("y", 0.0)), float(t3d_bz.get("z", 0.0)))
			),
			Vector3(float(t3d_origin.get("x", 0.0)), float(t3d_origin.get("y", 0.0)), float(t3d_origin.get("z", 0.0)))
		)
		values_set += 1
	if params.get("node_path_value") != null:
		value = NodePath(str(params["node_path_value"]))
		values_set += 1
	if params.get("string_array_value") != null:
		value = PackedStringArray(params["string_array_value"])
		values_set += 1
	if params.get("int_array_value") != null:
		value = PackedInt32Array(params["int_array_value"])
		values_set += 1
	if params.get("float_array_value") != null:
		value = PackedFloat32Array(params["float_array_value"])
		values_set += 1
	if params.get("vector2_array_value") != null:
		var v2a_items: Array = params["vector2_array_value"]
		var v2a_list: Array[Vector2] = []
		for v2a_item in v2a_items:
			v2a_list.append(Vector2(float(v2a_item.get("x", 0.0)), float(v2a_item.get("y", 0.0))))
		value = PackedVector2Array(v2a_list)
		values_set += 1
	if params.get("color_array_value") != null:
		var ca_items: Array = params["color_array_value"]
		var ca_list: Array[Color] = []
		for ca_item in ca_items:
			ca_list.append(Color(float(ca_item.get("r", 0.0)), float(ca_item.get("g", 0.0)), float(ca_item.get("b", 0.0)), float(ca_item.get("a", 1.0))))
		value = PackedColorArray(ca_list)
		values_set += 1
	if params.get("vector3_array_value") != null:
		var v3a_items: Array = params["vector3_array_value"]
		var v3a_list: Array[Vector3] = []
		for v3a_item in v3a_items:
			v3a_list.append(Vector3(float(v3a_item.get("x", 0.0)), float(v3a_item.get("y", 0.0)), float(v3a_item.get("z", 0.0))))
		value = PackedVector3Array(v3a_list)
		values_set += 1
	if params.get("node_path_array_value") != null:
		var npa_items: Array = params["node_path_array_value"]
		var npa_list: Array[NodePath] = []
		for npa_item in npa_items:
			npa_list.append(NodePath(str(npa_item)))
		value = npa_list
		values_set += 1
	if params.get("resource_value") != null:
		var res_path: String = params["resource_value"]
		if not res_path.begins_with("res://"):
			return _err("set_node_property: resource_value must be a res:// path")
		if not ResourceLoader.exists(res_path):
			return _err("set_node_property: no resource at %s" % res_path)
		var loaded_resource: Resource = load(res_path)
		if loaded_resource == null:
			return _err("set_node_property: failed to load resource at %s" % res_path)
		value = loaded_resource
		values_set += 1
	# The six Typed*ArrayValue branches below build a genuine typed
	# Array[T], the same way Array[NodePath] does above — not the
	# corresponding Packed*Array constructor. Reusing e.g. PackedStringArray
	# here would let Object.set() coerce it into the property's declared
	# Array[String] silently, but then the post-set "actual != value" check
	# below would compare an Array against a PackedStringArray, which
	# GDScript's != operator raises a runtime error on instead of
	# evaluating — a real crash reproduced while building this, not a
	# hypothetical one.
	if params.get("typed_string_array_value") != null:
		var tsa_items: Array = params["typed_string_array_value"]
		var tsa_list: Array[String] = []
		for tsa_item in tsa_items:
			tsa_list.append(str(tsa_item))
		value = tsa_list
		values_set += 1
	if params.get("typed_int_array_value") != null:
		var tia_items: Array = params["typed_int_array_value"]
		var tia_list: Array[int] = []
		for tia_item in tia_items:
			tia_list.append(int(tia_item))
		value = tia_list
		values_set += 1
	if params.get("typed_float_array_value") != null:
		var tfa_items: Array = params["typed_float_array_value"]
		var tfa_list: Array[float] = []
		for tfa_item in tfa_items:
			tfa_list.append(float(tfa_item))
		value = tfa_list
		values_set += 1
	if params.get("typed_vector2_array_value") != null:
		var tv2a_items: Array = params["typed_vector2_array_value"]
		var tv2a_list: Array[Vector2] = []
		for tv2a_item in tv2a_items:
			tv2a_list.append(Vector2(float(tv2a_item.get("x", 0.0)), float(tv2a_item.get("y", 0.0))))
		value = tv2a_list
		values_set += 1
	if params.get("typed_color_array_value") != null:
		var tca_items: Array = params["typed_color_array_value"]
		var tca_list: Array[Color] = []
		for tca_item in tca_items:
			tca_list.append(Color(float(tca_item.get("r", 0.0)), float(tca_item.get("g", 0.0)), float(tca_item.get("b", 0.0)), float(tca_item.get("a", 1.0))))
		value = tca_list
		values_set += 1
	if params.get("typed_vector3_array_value") != null:
		var tv3a_items: Array = params["typed_vector3_array_value"]
		var tv3a_list: Array[Vector3] = []
		for tv3a_item in tv3a_items:
			tv3a_list.append(Vector3(float(tv3a_item.get("x", 0.0)), float(tv3a_item.get("y", 0.0)), float(tv3a_item.get("z", 0.0))))
		value = tv3a_list
		values_set += 1
	# typed_resource_array_value can only load each element here — building
	# the genuinely-typed Array[T] it needs to become requires knowing the
	# target property's declared element class, which isn't known until the
	# target node is resolved further down. `value` holds a plain Array of
	# loaded Resources until then; is_typed_resource_array flags that this
	# plain Array still needs that conversion before target.set().
	var is_typed_resource_array := false
	if params.get("typed_resource_array_value") != null:
		var tra_paths: Array = params["typed_resource_array_value"]
		var tra_loaded: Array = []
		for tra_path in tra_paths:
			var tra_path_str: String = str(tra_path)
			if not tra_path_str.begins_with("res://"):
				return _err("set_node_property: typed_resource_array_value must be a res:// path")
			if not ResourceLoader.exists(tra_path_str):
				return _err("set_node_property: no resource at %s" % tra_path_str)
			var tra_resource: Resource = load(tra_path_str)
			if tra_resource == null:
				return _err("set_node_property: failed to load resource at %s" % tra_path_str)
			tra_loaded.append(tra_resource)
		value = tra_loaded
		is_typed_resource_array = true
		values_set += 1
	if values_set != 1:
		return _err("set_node_property: exactly one of string_value/int_value/float_value/bool_value/vector2_value/vector3_value/color_value/vector2i_value/vector3i_value/quaternion_value/rect2_value/rect2i_value/plane_value/aabb_value/basis_value/transform2d_value/transform3d_value/node_path_value/string_array_value/int_array_value/float_array_value/vector2_array_value/color_array_value/vector3_array_value/node_path_array_value/resource_value/typed_string_array_value/typed_int_array_value/typed_float_array_value/typed_vector2_array_value/typed_color_array_value/typed_vector3_array_value/typed_resource_array_value must be set")

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

	# Object.set() does not enforce a Resource-typed property's declared
	# class — it will happily store a reference of the wrong class, and the
	# generic actual != value check below wouldn't catch that (the wrong
	# reference was genuinely stored, so get() echoes it straight back). So
	# a loaded resource_value is checked against the property's declared
	# class via ClassDB before ever calling set(), and rejected with a
	# specific error instead of silently writing an incompatible reference.
	if value is Resource:
		for prop in target.get_property_list():
			if prop["name"] != property_name:
				continue
			var expected_class: String = prop.get("class_name", "")
			if expected_class != "" and not ClassDB.is_parent_class(value.get_class(), expected_class):
				var value_class: String = value.get_class()
				root.free()
				return _err("set_node_property: resource of class %s is not compatible with %s (expects %s)" % [value_class, property_name, expected_class])
			break

	# is_typed_resource_array: `value` is still the plain Array of loaded
	# Resources the early parsing block built — it has to become a
	# genuinely-typed Array[T] before target.set() (see
	# TypedResourceArrayValue's doc comment on SetNodePropertyParams for
	# why: reusing an untyped Array here would risk the same Array-vs-wrong-
	# Variant-type crash Array[String] had). Unlike the scalar Resource
	# check above, per-element class compatibility doesn't need a
	# hand-rolled ClassDB check: Godot's own TypedArray container enforces
	# it (with proper subclass tolerance) the moment an incompatible
	# element is appended — confirmed empirically: the append is silently
	# rejected (array size doesn't grow) rather than raising a catchable
	# script error, so each append's size is checked to detect it.
	if is_typed_resource_array:
		var tra_expected_class := ""
		var tra_property_found := false
		for prop in target.get_property_list():
			if prop["name"] != property_name:
				continue
			tra_property_found = true
			if prop["type"] != TYPE_ARRAY:
				root.free()
				return _err("set_node_property: %s is not an array property" % property_name)
			# A typed array's element class lives in hint_string, not
			# class_name (which is empty for array-typed properties). For a
			# Resource-subclass element it's the compound
			# "<TYPE_OBJECT>/<PROPERTY_HINT_RESOURCE_TYPE>:<ClassName>"
			# encoding (e.g. "24/17:Texture2D") — confirmed empirically,
			# and different from the plain class-name form Array[NodePath]
			# uses, since NodePath is a builtin Variant type, not an
			# Object/Resource one.
			var tra_hint_string: String = prop.get("hint_string", "")
			tra_expected_class = tra_hint_string.split(":")[-1] if ":" in tra_hint_string else tra_hint_string
			break
		if not tra_property_found:
			root.free()
			return _err("set_node_property: no property named %s" % property_name)
		if tra_expected_class == "" or not ClassDB.class_exists(tra_expected_class) or not ClassDB.is_parent_class(tra_expected_class, "Resource"):
			root.free()
			return _err("set_node_property: %s is not a typed array of a Resource subclass" % property_name)

		var tra_typed_array := Array([], TYPE_OBJECT, StringName(tra_expected_class), null)
		var tra_loaded_elements: Array = value
		for i in tra_loaded_elements.size():
			var tra_element: Resource = tra_loaded_elements[i]
			tra_typed_array.append(tra_element)
			if tra_typed_array.size() != i + 1:
				var tra_element_class: String = tra_element.get_class()
				root.free()
				return _err("set_node_property: element %d (class %s) is not compatible with %s's declared element type %s" % [i, tra_element_class, property_name, tra_expected_class])
		value = tra_typed_array

	var previous: Variant = target.get(property_name)
	target.set(property_name, value)
	var actual: Variant = target.get(property_name)
	if actual != value:
		# target.get_class() must be read before root.free(): freeing root
		# also frees every descendant (including target, unless target is
		# root itself), and calling a method on an already-freed instance is
		# a silent no-op that returns null in GDScript rather than raising —
		# so capturing it after the free would have quietly corrupted this
		# error message instead of failing loudly.
		var target_class := target.get_class()
		root.free()
		return _err("set_node_property: setting %s on %s did not take effect (got %s, want %s) — likely an unknown property name or a type Godot couldn't coerce" % [property_name, target_class, actual, value])

	var new_packed := PackedScene.new()
	var pack_err := new_packed.pack(root)
	root.free()
	if pack_err != OK:
		return _err("set_node_property: failed to re-pack scene (error code %d)" % pack_err)

	var save_err := ResourceSaver.save(new_packed, path)
	if save_err != OK:
		return _err("set_node_property: failed to save %s (error code %d)" % [path, save_err])

	return {"ok": true, "result": {"previous_value": str(previous)}}


## add_node: loads a .tscn file (already-validated res:// path), adds one
## new child node under parent_node_path (relative to the scene root; empty
## string means the root itself) — either a bare node of a built-in Godot
## class (type_name) or an instance of another project .tscn
## (instance_scene_path), exactly one of the two — then re-packs and saves
## the scene.
##
## type_name must be a ClassDB-registered class that is a Node and can be
## instantiated; a project-defined class_name type is not accepted here —
## instantiating one always attaches its backing script to the new node, a
## different trust question deliberately deferred (see FEATURES.md).
##
## instance_scene_path is instanced the same way the editor's own "Instance
## Child Scene" works: the resulting node keeps a live reference to the
## sub-scene (its scene_file_path is non-empty after instantiate()), which
## Godot's own pack() serializes as an `instance=ExtResource(...)` node
## rather than flattening the sub-scene's contents — so only the new node
## itself needs owner set below, not every descendant inside it.
##
## A name collision with an existing sibling under the parent is rejected
## outright rather than silently renamed (Godot's own add_child() would
## otherwise auto-uniquify it), and the new node's name is read back after
## assignment as a backstop against any other silent renaming/sanitization
## Godot might do — the same "don't let Godot's silent coercion look like
## success" concern set_node_property already guards against for property
## writes.
func _op_add_node(params: Variant) -> Dictionary:
	if typeof(params) != TYPE_DICTIONARY or not params.has("path") or not params.has("parent_node_path") or not params.has("name"):
		return _err("add_node: missing \"path\", \"parent_node_path\" or \"name\" param")

	var path: String = params["path"]
	var parent_node_path: String = params["parent_node_path"]
	var name: String = params["name"]
	if not path.begins_with("res://"):
		return _err("add_node: path must be a res:// path")
	if name.is_empty():
		return _err("add_node: name must not be empty")

	var type_name: Variant = params.get("type_name")
	var instance_scene_path: Variant = params.get("instance_scene_path")
	var has_type := type_name != null
	var has_instance := instance_scene_path != null
	if has_type == has_instance:
		return _err("add_node: exactly one of type_name, instance_scene_path must be set")

	if not ResourceLoader.exists(path, "PackedScene"):
		return _err("add_node: no scene resource at %s" % path)

	var packed: PackedScene = load(path)
	if packed == null:
		return _err("add_node: failed to load %s" % path)

	var root: Node = packed.instantiate()
	if root == null:
		return _err("add_node: failed to instantiate %s" % path)

	var parent: Node = root
	if parent_node_path != "":
		parent = root.get_node_or_null(NodePath(parent_node_path))
	if parent == null:
		root.free()
		return _err("add_node: no node at %s" % parent_node_path)

	if parent.get_node_or_null(NodePath(name)) != null:
		root.free()
		return _err("add_node: a node named %s already exists under %s" % [name, parent_node_path])

	var new_node: Node = null
	if has_type:
		var type_name_str: String = str(type_name)
		if not ClassDB.class_exists(type_name_str):
			root.free()
			return _err("add_node: unknown class %s" % type_name_str)
		if not ClassDB.is_parent_class(type_name_str, "Node"):
			root.free()
			return _err("add_node: %s is not a Node subclass" % type_name_str)
		if not ClassDB.can_instantiate(type_name_str):
			root.free()
			return _err("add_node: %s cannot be instantiated" % type_name_str)
		new_node = ClassDB.instantiate(type_name_str)
	else:
		var instance_path_str: String = str(instance_scene_path)
		if not instance_path_str.begins_with("res://"):
			root.free()
			return _err("add_node: instance_scene_path must be a res:// path")
		if not ResourceLoader.exists(instance_path_str, "PackedScene"):
			root.free()
			return _err("add_node: no scene resource at %s" % instance_path_str)
		var sub_packed: PackedScene = load(instance_path_str)
		if sub_packed == null:
			root.free()
			return _err("add_node: failed to load %s" % instance_path_str)
		new_node = sub_packed.instantiate()
		if new_node == null:
			root.free()
			return _err("add_node: failed to instantiate %s" % instance_path_str)

	new_node.name = name
	parent.add_child(new_node)
	new_node.owner = root

	if new_node.name != name:
		# Godot's own name validation silently sanitizes/renames rather than
		# erroring on some inputs — same category of silent coercion
		# set_node_property already guards against for property writes.
		var actual_name: String = new_node.name
		root.free()
		return _err("add_node: node was added as %s instead of the requested %s" % [actual_name, name])

	var actual_type: String = new_node.get_class()

	var new_packed := PackedScene.new()
	var pack_err := new_packed.pack(root)
	root.free()
	if pack_err != OK:
		return _err("add_node: failed to re-pack scene (error code %d)" % pack_err)

	var save_err := ResourceSaver.save(new_packed, path)
	if save_err != OK:
		return _err("add_node: failed to save %s (error code %d)" % [path, save_err])

	return {"ok": true, "result": {"type": actual_type}}


## remove_node: loads a .tscn file (already-validated res:// path), removes
## one node addressed by node_path (relative to the scene root) and its
## entire subtree, then re-packs and saves the scene. node_path must not be
## empty and must not resolve to the scene root itself — there's no
## sensible in-place result for removing a scene's own root node, and it
## can't be reparented anywhere.
func _op_remove_node(params: Variant) -> Dictionary:
	if typeof(params) != TYPE_DICTIONARY or not params.has("path") or not params.has("node_path"):
		return _err("remove_node: missing \"path\" or \"node_path\" param")

	var path: String = params["path"]
	var node_path: String = params["node_path"]
	if not path.begins_with("res://"):
		return _err("remove_node: path must be a res:// path")
	if node_path.is_empty():
		return _err("remove_node: node_path must not be empty (removing the scene root is refused)")

	if not ResourceLoader.exists(path, "PackedScene"):
		return _err("remove_node: no scene resource at %s" % path)

	var packed: PackedScene = load(path)
	if packed == null:
		return _err("remove_node: failed to load %s" % path)

	var root: Node = packed.instantiate()
	if root == null:
		return _err("remove_node: failed to instantiate %s" % path)

	var target: Node = root.get_node_or_null(NodePath(node_path))
	if target == null:
		root.free()
		return _err("remove_node: no node at %s" % node_path)
	if target == root:
		root.free()
		return _err("remove_node: node_path resolves to the scene root, which cannot be removed")

	# target.get_class() and the subtree count must both be read before
	# root.free() (and before target.free()): freeing frees every
	# descendant, and calling a method on an already-freed instance is a
	# silent no-op that returns null rather than raising — see
	# _op_set_node_property's matching comment above.
	var removed_type: String = target.get_class()
	var removed_count: int = _count_subtree(target)

	var parent := target.get_parent()
	parent.remove_child(target)
	target.free()

	var new_packed := PackedScene.new()
	var pack_err := new_packed.pack(root)
	root.free()
	if pack_err != OK:
		return _err("remove_node: failed to re-pack scene (error code %d)" % pack_err)

	var save_err := ResourceSaver.save(new_packed, path)
	if save_err != OK:
		return _err("remove_node: failed to save %s (error code %d)" % [path, save_err])

	return {"ok": true, "result": {"removed_type": removed_type, "removed_node_count": removed_count}}


func _count_subtree(node: Node) -> int:
	var count := 1
	for child in node.get_children():
		count += _count_subtree(child)
	return count


func _err(message: String) -> Dictionary:
	return {"ok": false, "error": message}
