# Features

A running list of what's planned for `godot-mcp-server`, organized by the project's priority
order. Nothing here is built yet unless noted otherwise — this is a design reference, not a
changelog.

## Security (foundation — everything else depends on this)

- [x] MCP server core in Go, using `github.com/modelcontextprotocol/go-sdk`
- [x] Fixed, explicit tool allowlist — no generic execution tool, ever
- [x] Path validation on every file operation: resolves inside configured project root, rejects
      `../` traversal outright
- [x] TCP runtime tier only ever dials `127.0.0.1`, never exposed beyond loopback by default (the
      Go side is a client — the autoload script owns the bound socket, see README's architecture)
- [x] Per-invocation audit logging (operation, params, result) independent of the MCP client's own
      logs (stderr always; also `logs/<session>.txt` next to the binary by default, so a human
      has a durable file to review — see `SECURITY.md`)
- [x] Read vs. write tool separation, with write tools held to a stricter review bar — mechanism
      (`-mode read-only`/`-mode read-write`, defaulting to read-only, gating registration in
      `internal/tools.RegisterAll` so write tools aren't advertised at all outside `read-write`) is
      in place; no write tool exists yet to actually plug into it
- [ ] `SECURITY.md` threat model kept current as tools are added
- [x] GitHub Actions workflows with third-party actions pinned by commit SHA, least-privilege
      `permissions:` blocks
- [x] Signed release binaries (cosign or goreleaser's built-in signing) + published checksums
      (`checksums.txt` via `goreleaser`'s `checksum` block, signed keylessly via cosign/Sigstore
      OIDC in `release.yml` — no maintainer-held signing key)

## Features (built on top of the security foundation)

### Headless CLI tier
- [x] Single fixed Go/GDScript operations entry point (name TBD — not required to be
      `godot_operations.gd`, that's just the convention seen in prior art)
- [x] Read scene tree / node structure
- [x] Read script contents (deliberately doesn't invoke Godot — `.gd` files are plain text, so
      this is a validated direct file read, not a `godot --headless` round trip; see
      `internal/headless.Client.ReadScript`'s doc comment)
- [x] Read project settings (`project.godot`, the one per-project config file — same
      direct-read pattern as script contents, no Godot round trip, no path param needed)
- [x] Read text resources — tool `read_text_resource` (deliberately not the bare `read_resource`,
      which would misleadingly suggest it also covers `.res`): scoped to `.tres` text resources
      only, same direct-read pattern as scripts, no Godot round trip
- [x] Read binary resources — tool `read_binary_resource`: `.res` is binary-packed, so this
      genuinely needs Godot, the same as `read_scene_tree` needs it for `.tscn`. Loads the
      resource and re-serializes it through Godot's own `.tres` text format (`ResourceSaver.save`
      is file-based — there's no in-memory "serialize to a string" API), writing only to a
      Go-generated temp path outside the project root, never anything the AI client sees or
      controls, cleaned up before the call returns
- [x] Asset/import inspection — tool `read_import_settings`: `.import` sidecar files are plain
      `ConfigFile`-style text (same as `project.godot`), generated next to an asset once a
      project's been opened/imported at least once, so this is a direct read like project
      settings — no Godot round trip. Takes the asset's own path (e.g. `icon.png`), not the
      `.import` file's path, and reads the sibling `<path>.import`
- [x] Scoped node property edit (structured, not free-form script edit) — tool `set_node_property`.
      Only registered under `-mode read-write`. Note: writes go through Godot's own scene
      re-pack/save, so the result is Godot's full current serialization of the scene, not a
      minimal diff (see `internal/headless.Client.SetNodeProperty`'s doc comment). Value types,
      roughly ordered from simplest to implement to most complex — which also tends to track how
      commonly each is actually needed on a node property, so this list doubles as a priority
      order for what to build next. Decided (2026-08-27), then revised twice the same day —
      first once the `Array[T]`/`Packed*Array` split turned out to hide a real bug (see the
      `Array[String]` note below), then again once `Array[Resource]`'s design questions turned
      out to be answerable (see its own note below) rather than genuinely blocked: coverage now
      includes primitives, the fixed-arity structs, the primitive/vector/color packed arrays,
      external resource-file references, and every typed-`Array[T]` element type with an existing
      `Packed*Array` or scalar sibling, including `Array[Resource]`. The freeze still holds for
      what's left below (`PackedByteArray`, `PackedInt64Array`/`PackedFloat64Array`/
      `PackedVector4Array`) — those stay deliberately unimplemented until actual tool usage shows
      a property that needs one, not built ahead of demand:
    - [x] Primitives: string, int, float, bool (int also covers Godot's int-backed enums, since
          `Object.set()` takes the raw int either way — no separate enum type needed)
    - [x] Vector2 (2D position, scale, size, ...)
    - [x] Vector3 (3D position, scale, ...)
    - [x] Color (modulate, self_modulate, ...)
    - [x] Vector2i / Vector3i — integer-component vectors (grid coordinates, pixel sizes, ...);
          same fixed-arity pattern as Vector2/Vector3, just int fields instead of float. Note:
          no built-in Node class exposes a Vector3i property at all (verified via ClassDB
          introspection against a real build — the only one in the engine is a Resource property,
          out of this tool's reach either way), so its real-Godot test target is a custom
          script-exported property instead, which is arguably the more representative use case
          for this tool regardless
    - [x] Quaternion — 4-float rotation representation, alternative to Euler angles; same
          fixed-arity pattern as the vectors above, new only in what it's used for
    - [x] Rect2 / Rect2i — a position + size pair (two Vector2/Vector2i); UI layout and collision
          bounds
    - [x] Plane — a Vector3 normal plus a float distance. Note: like Vector3i, no built-in Node
          class exposes a Plane property (the only one in the engine is a Resource property), so
          its real-Godot test target is a custom script-exported property too
    - [x] AABB — a 3D axis-aligned bounding box (a position Vector3 + a size Vector3)
    - [x] Basis — a 3x3 matrix (9 floats); 3D rotation/scale without translation
    - [x] Transform2D / Transform3D — the actual stored representation behind a node's own
          transform. Note: this runs opposite ways for 2D vs 3D — Node3D stores `transform`
          directly (`position`/`rotation`/`scale`/`basis`/`quaternion` are synthetic accessors
          onto it), while Node2D stores `position`/`rotation`/`scale`/`skew` directly (`transform`
          itself is the synthetic accessor)
    - [x] NodePath — addresses another node in the *already-loaded* scene tree (not a filesystem
          path, so it doesn't reopen the path-validation question the way resource references
          below do)
    - [ ] Packed arrays — variable-length, and each array type needs its own wire representation;
          a materially different design from the fixed-arity structs above, not just another
          field. Scoped one array type at a time rather than all at once:
        - [x] PackedStringArray — simplest wire shape (a plain JSON array of strings, no
              int/float ambiguity); establishes the variable-length-array pattern (including the
              nil-vs-explicitly-empty-array distinction other array types will need too — see
              `SetNodePropertyParams.StringArrayValue`'s doc comment) for the rest to follow
        - [x] PackedInt32Array — same flat-scalar-array pattern, next simplest
        - [x] PackedFloat32Array — same flat-scalar-array pattern
        - [x] PackedVector2Array — each element is itself a nested `{x, y}` object, not a bare
              scalar; genuinely common for 2D collision/shape data (`Polygon2D.polygon`,
              `CollisionPolygon2D.polygon`, `Line2D.points`)
        - [x] PackedColorArray — nested `{r, g, b, a}` elements
        - [x] PackedVector3Array — nested `{x, y, z}` elements. Note: of the two built-in Node
              targets, CPUParticles3D's emission_points/emission_normals are both
              usage=0 (runtime-only, never persisted) per ClassDB — NavigationObstacle3D.vertices
              is the only one that actually saves
        - [ ] PackedByteArray — raw binary (e.g. `TileMapLayer.tile_map_data`); likely out of
              scope, since it's internal engine-packed data rather than something meant for
              external hand-editing
        - [ ] PackedInt64Array / PackedFloat64Array / PackedVector4Array — no built-in Node
              property uses any of these three at all (verified via ClassDB introspection), so
              low priority
    - [x] Typed `Array[T]` properties — Godot's designer-typed generic arrays (e.g.
          `Control.accessibility_flow_to_nodes`) are a different Variant mechanism from the fixed
          `Packed*Array` family above (a generic `Array` carrying element-type metadata, not a
          native packed array), so this is its own item, not a `Packed*Array` variant. Note: the
          genericness is in the engine's type system only — neither `SetNodePropertyParams` nor
          `_op_set_node_property` has any generic "any `T`" path; each element type below is its
          own Go field and its own hand-written GDScript branch, same as every fixed-arity value
          type above. `String`/`int`/`float`/`Vector2`/`Color`/`Vector3`/`NodePath` (2026-08-27)
          are the complete set of element types with an existing `Packed*Array` or scalar
          sibling; none of the seven has a built-in Node property target (verified via ClassDB
          introspection against a real build, same as `Vector3i`/`Plane` above), so
          `custom_props_holder.gd`'s exported properties are the real-Godot test target for all
          of them, same as those two:
        - [x] `Array[NodePath]` — same trust boundary as the existing scalar `NodePathValue`
              (addresses nodes already in the loaded scene tree, not the filesystem)
        - [x] `Array[String]` — reusing `StringArrayValue`'s `PackedStringArray` here instead of a
              dedicated `Array[String]` value would have been a real, reproduced bug, not just a
              missed optimization: `Object.set()` silently coerces a `PackedStringArray` into an
              `Array[String]`-typed property, but the post-set `actual != value` verification then
              compares an `Array` against a `PackedStringArray`, which GDScript's `!=` operator
              raises a runtime error on instead of evaluating — crashing `_op_set_node_property`
              mid-request and returning a malformed response instead of a clean error. A note the
              string content might itself be a `res://` path changes nothing here: the property's
              declared type is `String`, not `Resource`, so nothing loads and no resource-specific
              handling applies — an AI caller can already pass `res://`-shaped strings as ordinary
              elements
        - [x] `Array[int]`, `Array[float]`, `Array[Vector2]`, `Array[Color]`, `Array[Vector3]` —
              same shape as `Array[String]`/`Array[NodePath]` above, no new design questions
        - [x] `Array[T]` where `T` is a Resource subclass (e.g. `Array[Texture2D]`) — field
              `TypedResourceArrayValue`/`typed_resource_array_value`, each element a
              project-relative path resolved through `Root.Resolve` exactly like `ResourceValue`,
              just per element. This one needed genuinely new machinery, not just another
              hand-written `Array[T] = []` branch, and an earlier note here (a typed array's
              element class lives in `hint_string` as a *plain class name*) turned out to be
              wrong for this case — that was only verified against `Array[NodePath]`, a builtin
              Variant element type. For a Resource element, `hint_string` uses the *compound*
              `"<TYPE_OBJECT>/<PROPERTY_HINT_RESOURCE_TYPE>:<ClassName>"` encoding (e.g.
              `"24/17:Texture2D"`, confirmed via ClassDB probe against a real build) — the class
              name has to be parsed out of the part after the `:`. Godot also doesn't expose
              `Array.set_typed()` to GDScript for building a runtime-typed array dynamically (the
              element class isn't known until the target property is inspected); the working
              mechanism is the 4-argument constructor, `Array([], TYPE_OBJECT,
              StringName(expected_class), null)`, also confirmed empirically. One thing that
              turned out simpler than expected: once the array is properly typed this way, Godot's
              own `TypedArray` container enforces per-element class compatibility (with subclass
              tolerance) on `append()` — no hand-rolled `ClassDB.is_parent_class` check needed the
              way the scalar `ResourceValue` case required, since `Object.set()` alone doesn't
              enforce that. The one wrinkle: a rejected `append()` isn't a catchable script error,
              just a silent no-op (plus a stderr `ERROR` print), so each append's array size is
              checked to detect and cleanly report it. No built-in Node target exists here either
              (reverified via ClassDB), so `custom_props_holder.gd`'s `typed_textures:
              Array[Texture2D]` is the real-Godot test target.
    - [x] Resource / sub-resource references (Texture2D, Material, PackedScene, ...) — reopened
          path validation as expected, so needed its own maintainer conversation before starting,
          per CLAUDE.md; that conversation happened (2026-08-27) and settled scope for v1, all of
          which is implemented (`SetNodePropertyParams.ResourceValue`, `internal/tools`'s
          `resource_value`, and `_op_set_node_property`'s matching branch in
          `scripts/godot_operations.gd`):
        - External resource *files* only, referenced by a project-relative path validated through
          `Root.Resolve` exactly like `ScenePath`/`ScriptPath` today, converted to `res://`, then
          loaded via `load()` on that derived path. `_op_set_node_property` must never call
          `load()` on anything the caller supplies directly — Godot's `load()` also accepts
          absolute OS paths and `user://`, which would sidestep `Root.Resolve` entirely.
        - Reference-only, not construct-in-place: the tool points a property at an existing
          resource file; it never builds a new Resource from inline parameters (a materially
          bigger surface — general object construction — that nothing else in this design does).
        - Embedded sub-resources (a `.tscn`'s own `[sub_resource]` blocks, addressed by an in-file
          ID rather than a filesystem path) are explicitly out of scope. There's no tool that
          surfaces a scene's sub-resource table to the AI in the first place — `read_scene_tree`
          reports only name/type/children (see `_node_to_dict` in `scripts/godot_operations.gd`),
          no properties at all — so there's nothing for a caller to even name yet. Revisit only if
          `read_scene_tree` grows property/resource reporting.
        - The loaded resource's runtime class must be checked against the property's declared
          type (via `get_property_list()`/ClassDB) before calling `set()`, and a mismatch
          rejected with a clear error. `Object.set()` silently no-ops or coerces badly on a type
          mismatch, and unlike a human using the inspector, an agentic caller has no other way to
          notice its "successful" tool call didn't do what it asked.
        - `property_name == "script"` (and any other `Script`/`GDScript`-typed property) is
          hard-blocked unconditionally, regardless of the above — a resource reference there is
          arbitrary code execution by another name, a direct violation of CLAUDE.md's first hard
          constraint, and not something to leave to Godot's own `set()` to (not) catch.
    - [ ] Direct Node object references (a property typed to expect a live Node instance, not a
          NodePath string) — likely stays out of scope entirely; there's no clean way to express
          "assign this other node" as a scoped, structured tool argument the way everything above
          can be
- [x] Scoped script edit via structured diff (not arbitrary rewrite) — split into two tools across
      two risk tiers, per SECURITY.md's prescription that anything riskier than the stated
      defaults should be an explicit, off-by-default "advanced" tool rather than a loosening of
      them. Both operate on an existing `.gd` script's raw text directly in Go (no
      `scripts/godot_operations.gd` involvement — same rationale as `read_script`, see its doc
      comment: GDScript is plain UTF-8 text, no engine capability is needed to read or splice it),
      and both verify the result via `godot --headless --check-only --script <path>` after
      writing, rolling back to the original file contents if it doesn't parse (confirmed
      empirically against a real Godot 4.7.2 binary: `--check-only` exits 0 with no output on a
      valid script, exits 1 with a `SCRIPT ERROR: Parse Error: ...` message on an invalid one, and
      is parse-only — it never executes class-body or function-body code, the analogue of
      `set_node_property`'s "read the value back and verify it changed" pattern, adapted to
      script text). Neither tool creates a new script file from scratch — both require the target
      `.gd` file to already exist.
    - [x] `set_script_export` — adds or modifies a single top-level `@export var <name>: <Type> =
          <default>` declaration. Registered under `-mode read-write` (and, since `-mode advanced`
          is a strict superset, also under `-mode advanced`) — a structural, non-executable
          declaration edit is the same risk class as `set_node_property`, not the `-mode advanced`
          tier. v1 value-type scope mirrors `set_node_property`'s own "primitives first" ordering:
        - [x] Primitives: string, int, float, bool
        - [x] Vector2, Vector3, Color
        - [x] Vector2i, Vector3i, Quaternion, Rect2, Rect2i, Plane, AABB, Basis, Transform2D,
              Transform3D, NodePath — confirmed no new design question, as expected: each renders
              to the exact GDScript constructor syntax already validated for `set_node_property`'s
              own GDScript side (see `scripts/godot_operations.gd`'s matching branches), and all
              11 were reverified as valid `@export` defaults against a real Godot 4.7.2 binary
              before implementing
        - [ ] Packed/typed arrays and Resource-typed export defaults — deferred: an array or
              Resource default needs its own literal-rendering design (e.g. a `preload()` call for
              a Resource default), not just another scalar case
        - [ ] Other declaration kinds — deliberately out of v1, each for its own reason, not
              bundled into `set_script_export` just because it's also "a line near the top of a
              script":
            - [ ] `signal` declarations — a different statement shape entirely (no type/default
                  pair), would need its own params shape, not a variant of this tool's
            - [ ] `class_name` / `extends` edits — change the script's identity/inheritance, a much
                  bigger blast radius than adding one property (every file referencing this script
                  by class name, or relying on its base class, is affected)
            - [ ] `@onready var` declarations — same declaration shape as `@export var` but a
                  different annotation with different semantics (evaluated at `_ready()`, not
                  editor-exposed); revisit once `@export var` is proven out
    - [x] `set_function_body` — replaces an existing top-level function's body (and, selectively,
          its parameters and/or return type — `nil` on either leaves that part of the existing
          signature unchanged) with caller-supplied GDScript source text, or inserts a new
          top-level function (appended at the end of the file) if the named function doesn't exist
          yet. This is the one tool in this server that lets an AI client author or replace
          executable logic, so it does not fit under `-mode read-write`: gated behind a new
          `-mode advanced` value (`internal/tools.Mode`'s third value, `ModeAdvanced`, a strict
          superset of `ModeReadWrite` — registers everything `-mode read-write` does, plus this),
          requiring an explicit, separate operator opt-in at startup (also prints a loud stderr
          warning naming the risk), matching SECURITY.md's prescribed shape for anything riskier
          than the stated defaults. Full signature replacement (not just the body) was a
          deliberate call: the tool is already gated behind an explicit opt-in, so intent is
          established, and correctness of call sites elsewhere in the project after a signature
          change is left to the operator/AI to catch — e.g. via the future TCP tier's
          console/debugger-output reading, iteratively, as separate scoped calls — rather than
          asking this single-function-scoped tool to be a safe whole-project refactorer.
          `function_name` itself is never changed by this operation — renaming (updating every
          call site's call syntax elsewhere in the project) is a qualitatively bigger, cross-file
          operation, out of scope here. `--check-only` verification catches syntax errors, not
          incorrect logic — it cannot tell a caller their function body is wrong, unsafe, or
          behaves unexpectedly, only that it parses. v1 scope: top-level functions only (a `func`
          line at zero indentation, so a nested `class`'s own methods are never matched by
          mistake); single-line function signatures only (a wrapped multi-line `func foo(\n  a,\n)
          -> void:` parameter list is refused with a clear error, not guessed at).
- [ ] Add / remove node (parameterized)

### TCP runtime tier
- [ ] Autoload listener script (localhost-only) for live editor/game state
- [ ] Read current scene state from a running game
- [ ] Read console/debugger output
- [ ] Trigger inputs for interactive testing (scoped, logged)

### Distribution
- [x] Cross-compiled release binaries (Linux/macOS/Windows, amd64/arm64) via `goreleaser` in
      GitHub Actions, triggered on tag push (workflow + `.goreleaser.yaml` in place and validated
      with `goreleaser check` and a `--snapshot` build across the full matrix; no tag has actually
      been pushed yet)
- [x] `go install`-able module path
- [ ] Homebrew tap / Scoop bucket (later, once stable)

### Configuration
- [x] Config-driven tool scoping (e.g. read-only mode vs. read/write mode) rather than requiring a
      fork to change what's exposed — `-mode` flag on `cmd/godot-mcp-server`
- [ ] Explicit, off-by-default "advanced" tool category for anything that would otherwise widen
      the trust boundary (see `SECURITY.md`)

## Quality-of-life (after the above is solid)

- [ ] Improved error messages / diagnostics for common misconfiguration
- [ ] Example MCP client configs for common clients (Claude Code, Claude Desktop, etc.)
- [ ] Better install ergonomics (single-command install script, package manager distribution)
- [ ] Contributor-facing docs beyond `CONTRIBUTING.md` (architecture deep-dive, tool-authoring
      guide)

## Explicitly out of scope

See `SECURITY.md`'s "Won't-fix / won't-add" section — arbitrary code execution tools, disabled
path validation, and non-loopback binding by default are permanent non-goals, not backlog items.
