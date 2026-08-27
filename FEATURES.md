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
      logs
- [ ] Read vs. write tool separation, with write tools held to a stricter review bar (no write
      tool exists yet to hold to that bar — nothing to separate from)
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
- [ ] Read script contents
- [ ] Read project settings and resources
- [ ] Scoped node property edit (structured, not free-form script edit)
- [ ] Scoped script edit via structured diff (not arbitrary rewrite)
- [ ] Add / remove node (parameterized)
- [ ] Asset/import inspection

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
- [ ] Config-driven tool scoping (e.g. read-only mode vs. read/write mode) rather than requiring a
      fork to change what's exposed
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
