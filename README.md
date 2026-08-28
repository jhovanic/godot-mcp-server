# godot-mcp-server

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
- **TCP runtime tier** — an autoload script inside the target project listens on a localhost-only
  socket for read/interact commands against a *running* game or editor session.

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
(also handles `@onready var`), `set_script_signal`, and `set_script_identity` (a script's
`class_name`/`extends`), or `-mode advanced` to additionally expose `set_function_body` — the one
tool that lets the AI client author or replace executable GDScript logic; read
[SECURITY.md](./SECURITY.md) before enabling it.
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

## Security

Read [SECURITY.md](./SECURITY.md) before pointing this at a project you care about. Short version:
scoped operations only, path-allowlisted to the project root, TCP tier bound to localhost, no
arbitrary code execution, and that's a permanent design constraint, not a v1 limitation.

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). Priority order for this project is **security, then
features, then quality-of-life** — contributions are evaluated against that order.

## License

MIT — see [LICENSE](./LICENSE).
