## Runtime tier listener template — NOT used automatically by godot-mcp-server.
##
## This file is a template the operator copies into their own project and
## registers as an autoload, to opt that project into the TCP runtime tier
## (live scene tree / node property reads while the game is running).
## godot-mcp-server never writes this file or edits project.godot for
## you — see README.md's "Enabling the TCP runtime tier" section for the
## two-line project.godot change this needs.
##
## Binds the first free port in a fixed, documented range (PORT_RANGE_START
## .. PORT_RANGE_END) on 127.0.0.1 only — never 0.0.0.0 or any other
## address, per this project's permanent "no non-local network exposure by
## default" constraint. A single fixed port doesn't survive contact with
## reality (something else might already be bound to it, and Godot's own
## "run multiple instances" feature means more than one live listener can
## legitimately exist at once for local multiplayer testing), so a range is
## tried instead, stopping at the first port that binds. godot-mcp-server's
## own default range must match this one — see internal/runtime.DefaultPortRange
## in the server's source; if you change one, change the other to match.
##
## Speaks the same "fixed, named operations only, never eval" wire protocol
## as the headless CLI tier's own scripts/godot_operations.gd, just over a
## socket instead of stdin/argv: one newline-terminated JSON request per
## connection (`{"operation": ..., "params": {...}}`), one newline-terminated
## JSON response back (`{"ok": true, "result": ...}` or
## `{"ok": false, "error": "..."}`), then the connection is closed.
##
## Read-only. There is no operation here that mutates the running game's
## state — this template only ever exposes reads (project identity, scene
## tree, node property values).
extends Node

const PORT_RANGE_START := 9080
const PORT_RANGE_END := 9089

## Above this many bytes with no newline yet, a connection is treated as
## malformed (or hostile) and dropped — defense in depth, even though the
## same-machine trust model already treats "a process on this machine" as
## trusted (see SECURITY.md).
const MAX_PENDING_BYTES := 1 << 20 # 1 MiB

var _server: TCPServer
var _peers: Array[StreamPeerTCP] = []
var _peer_buffers: Array[String] = []


func _ready() -> void:
	_server = TCPServer.new()
	for port in range(PORT_RANGE_START, PORT_RANGE_END + 1):
		if _server.listen(port, "127.0.0.1") == OK:
			print("[mcp-runtime] listening on 127.0.0.1:%d" % port)
			return
	push_warning("[mcp-runtime] could not bind any port in %d-%d — is something else already using the whole range?" % [PORT_RANGE_START, PORT_RANGE_END])


func _process(_delta: float) -> void:
	if _server == null or not _server.is_listening():
		return

	while _server.is_connection_available():
		var peer := _server.take_connection()
		_peers.append(peer)
		_peer_buffers.append("")

	# Iterate backwards so remove_at() during the loop is safe.
	for i in range(_peers.size() - 1, -1, -1):
		var peer: StreamPeerTCP = _peers[i]
		peer.poll()
		if peer.get_status() != StreamPeerTCP.STATUS_CONNECTED:
			_peers.remove_at(i)
			_peer_buffers.remove_at(i)
			continue

		var available := peer.get_available_bytes()
		if available <= 0:
			continue

		_peer_buffers[i] += peer.get_utf8_string(available)
		if _peer_buffers[i].length() > MAX_PENDING_BYTES:
			_peers.remove_at(i)
			_peer_buffers.remove_at(i)
			continue

		var newline_idx := _peer_buffers[i].find("\n")
		if newline_idx == -1:
			continue

		var line := _peer_buffers[i].substr(0, newline_idx)
		var response := _dispatch(line)
		# put_utf8_string() is NOT a plain write — StreamPeer prepends its
		# own 4-byte length header to it (confirmed empirically against a
		# real Godot 4.7.2 binary: a plain-text client reading a
		# newline-terminated line saw four extra leading bytes). put_data()
		# with the string's own raw UTF-8 bytes writes exactly the bytes
		# intended, no extra framing, matching what a plain TCP client
		# reading up to '\n' expects.
		var response_text := JSON.stringify(response) + "\n"
		peer.put_data(response_text.to_utf8_buffer())
		peer.disconnect_from_host()
		_peers.remove_at(i)
		_peer_buffers.remove_at(i)


## Dispatches a single fixed operation name — never free-form/user-supplied
## text interpreted as code, same discipline as
## scripts/godot_operations.gd's own _dispatch.
func _dispatch(line: String) -> Dictionary:
	var parsed: Variant = JSON.parse_string(line)
	if typeof(parsed) != TYPE_DICTIONARY:
		return _err("invalid or missing JSON request")

	var operation: String = parsed.get("operation", "")
	var params: Variant = parsed.get("params", {})

	match operation:
		"ping":
			return {"ok": true, "result": "pong"}
		"hello":
			return _op_hello()
		"read_scene_tree":
			return _op_read_scene_tree()
		"read_node_property":
			return _op_read_node_property(params)
		_:
			return _err("unknown operation: %s" % operation)


## hello: reachability probe used by DiscoverInstances, extended with the
## project's own configured name so multiple simultaneous listeners
## (several instances of one project, or different projects on the same
## machine) can be told apart.
func _op_hello() -> Dictionary:
	var project_name: String = ProjectSettings.get_setting("application/config/name", "")
	return {"ok": true, "result": {"project_name": project_name}}


func _op_read_scene_tree() -> Dictionary:
	var scene := get_tree().current_scene
	if scene == null:
		return _err("no current scene")
	return {"ok": true, "result": _node_to_dict(scene, scene)}


## Mirrors scripts/godot_operations.gd's own _node_to_dict, plus a "path"
## field (this node's address relative to root, in Godot's own NodePath
## syntax) so a read_scene_tree result can be chained directly into
## read_node_property without the caller reconstructing paths by hand.
func _node_to_dict(node: Node, root: Node) -> Dictionary:
	var children: Array = []
	for child in node.get_children():
		children.append(_node_to_dict(child, root))

	var out := {
		"name": node.name as String,
		"type": node.get_class(),
		"path": str(root.get_path_to(node)),
	}
	if not children.is_empty():
		out["children"] = children
	return out


## Reads one live property off a node addressed relative to the current
## scene root (empty string means the root itself), the same node_path
## convention used everywhere else in this project. Read-only: never calls
## set(). Returns the value as a string representation (str(value)), not a
## fully round-trippable typed value — this exists so an AI caller can
## understand what a value currently is, not so it can be fed back into
## another structured call (mirrors SetNodePropertyResult.PreviousValue's
## own str() precedent in the headless tier).
func _op_read_node_property(params: Variant) -> Dictionary:
	if typeof(params) != TYPE_DICTIONARY or not params.has("node_path") or not params.has("property_name"):
		return _err("missing \"node_path\" or \"property_name\" param")

	var node_path: String = params["node_path"]
	var property_name: String = params["property_name"]

	var scene := get_tree().current_scene
	if scene == null:
		return _err("no current scene")

	var target: Node = scene
	if node_path != "":
		target = scene.get_node_or_null(NodePath(node_path))
	if target == null:
		return _err("no node at %s" % node_path)

	var found := false
	for prop in target.get_property_list():
		if prop["name"] == property_name:
			found = true
			break
	if not found:
		return _err("no property named %s" % property_name)

	var value: Variant = target.get(property_name)
	return {"ok": true, "result": {"value": str(value), "type": type_string(typeof(value))}}


func _err(message: String) -> Dictionary:
	return {"ok": false, "error": message}
