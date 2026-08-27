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


func _err(message: String) -> Dictionary:
	return {"ok": false, "error": message}
