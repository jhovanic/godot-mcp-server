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
      order for what to build next:
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
    - [ ] Rect2 / Rect2i — a position + size pair (two Vector2/Vector2i); UI layout and collision
          bounds
    - [ ] Plane — a Vector3 normal plus a float distance
    - [ ] AABB — a 3D axis-aligned bounding box (a position Vector3 + a size Vector3)
    - [ ] Basis — a 3x3 matrix (9 floats); 3D rotation/scale without translation
    - [ ] Transform2D / Transform3D — the actual stored representation behind a node's own
          transform. Note from building Vector3 support: Node3D's `position`/`rotation`/`scale`
          aren't stored properties at all, only `transform` is — those are synthetic accessors
          onto it (see `TestSetNodeProperty_RealGodot_Vector3`'s doc comment) — so full transform
          support may end up mattering more in practice than the individual-component vectors
          already built
    - [ ] NodePath — addresses another node in the *already-loaded* scene tree (not a filesystem
          path, so it doesn't reopen the path-validation question the way resource references
          below do); trivial to implement (wrap a string in `NodePath(...)`) but still worth a
          deliberate look before adding, per CLAUDE.md's "when in doubt" rule
    - [ ] Packed arrays (PackedStringArray, PackedInt32Array, PackedFloat32Array,
          PackedVector2Array, PackedColorArray, ...) — variable-length, and each array type needs
          its own wire representation; a materially different design from the fixed-arity structs
          above, not just another field
    - [ ] Resource / sub-resource references (Texture2D, Material, PackedScene, ...) — the biggest
          item: reopens path validation (the referenced resource has to resolve inside the
          project root the same way a directly-addressed file does), so this needs its own
          maintainer conversation before starting, per CLAUDE.md
    - [ ] Direct Node object references (a property typed to expect a live Node instance, not a
          NodePath string) — likely stays out of scope entirely; there's no clean way to express
          "assign this other node" as a scoped, structured tool argument the way everything above
          can be
- [ ] Scoped script edit via structured diff (not arbitrary rewrite)
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
