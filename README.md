# godot-mcp-server

[![CI](https://github.com/jhovanic/godot-mcp-server/actions/workflows/ci.yml/badge.svg)](https://github.com/jhovanic/godot-mcp-server/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jhovanic/godot-mcp-server)](https://github.com/jhovanic/godot-mcp-server/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/jhovanic/godot-mcp-server)](go.mod)
[![License: MIT](https://img.shields.io/github/license/jhovanic/godot-mcp-server)](LICENSE)
[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/jhovanic)

A self-hosted [Model Context Protocol](https://modelcontextprotocol.io) server for Godot, written in Go.

## Why this exists

Existing Godot MCP servers are third-party tools you don't control — you can't fully audit what
they do, and several expose broad code-execution surfaces (arbitrary GDScript `eval`-style tools,
unrestricted filesystem access). `godot-mcp-server` is built with the opposite default: a small,
explicit, auditable set of operations, no arbitrary code execution, and a trust boundary you can
actually reason about.

If you're a solo dev who wants AI-assisted Godot workflows without handing an LLM a shell into
your project, this is for you.

## Architecture

Two tiers, because Godot has two surfaces worth touching:

```
AI client (MCP)
      |
 MCP server (Go) — you control the tool list
      |
      +-- Headless CLI tier --- scoped file ops via `godot --headless --script`, no eval
      |
      +-- TCP runtime tier ---- localhost-only socket, live game/editor interaction
                |
          Godot project (scenes, scripts, running game)
```

- **Headless CLI tier** — reads/edits scene trees, scripts, and resources by invoking Godot
  headless with a single fixed operations script. Structured JSON in, structured JSON out. No
  temp-script generation, no `eval`.
- **TCP runtime tier** — two independent mechanisms, both `-mode advanced` only:
  `launch_project`/`read_runtime_output`/`stop_runtime` launches a Godot process this server owns
  and captures its stdout/stderr directly (OS pipes — no autoload involved, and only ever works for
  a process this server itself started); `discover_runtime_instances`/`read_runtime_scene_tree`/
  `read_runtime_node_property` talks to an autoload script inside the target project over a
  localhost-only socket for live scene tree/property reads, which works against a process started
  *any* way (including the editor's own Play button) as long as the autoload is installed — see
  "Enabling the TCP runtime tier" below.

Every tool is an explicit, parameterized operation. There is no generic "run this code" tool, and
there isn't going to be one — see [SECURITY.md](./SECURITY.md).

## Status

Early development. Not yet released. Follow [FEATURES.md](./FEATURES.md) for what's planned and
what's shipped.

## Installation

Download the binary for your platform from the Releases page, or:

```bash
go install github.com/jhovanic/godot-mcp-server/cmd/godot-mcp-server@latest
```

## Usage

Point your MCP client (Claude Code, Claude Desktop, etc.) at the binary. Example config:

```json
{
  "mcpServers": {
    "godot": {
      "command": "godot-mcp-server",
      "args": ["--project", "/path/to/your/godot/project"]
    }
  }
}
```

By default only read tools are exposed. Pass `-mode read-write` (as an extra arg) to also expose
scoped write tools like `set_node_property`, `add_node`/`remove_node`/`reparent_node`
(create/delete/move a scene node, or instance another `.tscn` as a child), `set_script_export`
(also handles `@onready var`), `set_script_signal`, `set_script_identity` (a script's
`class_name`/`extends`), and `write_text_resource` (create/overwrite a `.tres` resource from a
built-in class), or `-mode advanced` to additionally expose `set_function_body` — the one tool
that lets the AI client author or replace executable GDScript logic — `write_text_resource`'s
`script_path` option, which instantiates a project script to construct a custom `Resource`
subclass, and the whole TCP runtime tier (`launch_project`/`read_runtime_output`/`stop_runtime`,
`discover_runtime_instances`/`read_runtime_scene_tree`/`read_runtime_node_property` — see below);
read [SECURITY.md](./SECURITY.md) before enabling it.
Full configuration reference is TBD as the tool surface stabilizes.

```json
{
  "mcpServers": {
    "godot": {
      "command": "godot-mcp-server",
      "args": [
        "--project", "/path/to/your/godot/project",
        "--mode", "read-write"
      ]
    }
  }
}
```

## Enabling the TCP runtime tier

`launch_project`/`read_runtime_output`/`stop_runtime` work on any project unmodified. The
live-state tools (`discover_runtime_instances`/`read_runtime_scene_tree`/
`read_runtime_node_property`) need one extra step: copy this repo's
[`scripts/mcp_runtime_autoload.gd`](./scripts/mcp_runtime_autoload.gd) into your project and
register it as an autoload in `project.godot`:

```ini
[autoload]

McpRuntime="*res://mcp_runtime_autoload.gd"
```

godot-mcp-server never writes this file or edits `project.godot` for you — this is the one capability
in this server that requires an explicit, visible change to your own project, not something a tool
call does on your behalf. The autoload binds the first free port in a fixed range (default
`9080`-`9089`, matching this server's own `-runtime-port-range` default) on `127.0.0.1` only. If
you change one side's range, change the other to match — an editor-launched session never receives
a runtime-negotiated port from this server, so both sides only agree by convention.

## Security

Read [SECURITY.md](./SECURITY.md) before pointing this at a project you care about. Short version:
scoped operations only, path-allowlisted to the project root, TCP tier bound to localhost, no
arbitrary code execution, and that's a permanent design constraint, not a v1 limitation.

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). Priority order for this project is **security, then
features, then quality-of-life** — contributions are evaluated against that order.

## License

MIT — see [LICENSE](./LICENSE).
