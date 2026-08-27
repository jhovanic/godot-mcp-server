# CLAUDE.md

Context for Claude (or any AI assistant) working in this repository.

## What this project is

A self-hosted MCP server for Godot, written in Go, whose entire premise is a smaller and more
auditable trust surface than existing third-party Godot MCP servers. See `README.md` for the
architecture and `SECURITY.md` for the threat model — read `SECURITY.md` before touching anything
in the tool-registration or file-access code paths.

## Priority order (applies to every change you propose)

1. **Security** — does this preserve the no-eval, path-allowlisted, localhost-only model?
2. **Features** — does this extend the *scoped* operation set without widening the trust boundary?
3. **Quality-of-life** — everything else (better errors, config ergonomics, install experience).

If a change trades a lower priority for a higher one — e.g., a feature that's more convenient but
loosens path validation — flag that trade-off explicitly rather than resolving it silently.

## Architecture

- `cmd/godot-mcp-server/` — entrypoint, MCP server wiring (stdio transport via
  `github.com/modelcontextprotocol/go-sdk`)
- Headless CLI tier — invokes `godot --headless --script <operations-script>.gd` with structured
  JSON params for scoped scene/script/resource operations. No arbitrary GDScript is ever
  constructed from user/AI input; only fixed, parameterized operations.
- TCP runtime tier — talks to an autoload script inside the target Godot project over a
  localhost-only TCP socket, for live game/editor state.
- Tool definitions live in a single reviewable location (allowlist) — do not scatter ad hoc tool
  registration across the codebase.

## Hard constraints — do not violate without an explicit, separate conversation with the maintainer

- Never add a tool that executes arbitrary code (GDScript, shell, or otherwise) supplied at
  runtime.
- Never remove or bypass path-root validation on file operations.
- Never bind the TCP tier to anything other than `127.0.0.1` / loopback by default.
- Never add a tool that isn't logged (operation + params + result).

## Conventions

- Standard Go project layout; `gofmt` and `golangci-lint` clean before commit.
- Prefer explicit, typed structs for tool params over generic `map[string]interface{}` where
  practical — this is part of enforcing the allowlist at compile time, not just at runtime.
- Table-driven tests for tool handlers; path-validation logic needs explicit traversal-attempt
  test cases (`../`, absolute paths outside root, symlink edge cases).
- Test are written before the code implementation of a feature.
- Commit messages: concise, imperative mood (`Add scoped node-property write tool`, not `Added...`).

## Build / test / lint

```bash
go build ./...
go vet ./...
go test ./...
golangci-lint run   # not installed in every dev environment; CI always runs it
```

The module targets the Go version pinned in `go.mod`. `internal/headless` and the GDScript in
`scripts/godot_operations.gd` have both pure unit tests (no Godot needed — path validation,
`parseResponse`'s stdout parsing) and real end-to-end integration tests
(`internal/headless/integration_test.go`) that run a real Godot binary against the fixture project
in `internal/headless/testdata/fixture_project/`. The integration tests skip themselves (`go test
./...` still passes) unless a Godot binary is available via `GODOT_BIN` or `PATH` — set one of
those locally to run them. CI installs a pinned, checksum-verified Godot and always runs them; see
`ci.yml`'s `GODOT_VERSION`/`GODOT_SHA512`, bumped together from the release's own
`SHA512-SUMS.txt`.

## CI / releases

GitHub Actions workflows must pin third-party actions by commit SHA, not by tag (`@<sha> #
vX.Y.Z` comment for readability), and use least-privilege `permissions:` blocks. Releases are
built via `goreleaser` on tag push (`v*`). See `CONTRIBUTING.md` for the fuller release process
once it exists.

## When in doubt

If a task in this repo isn't clearly a features-tier or QoL-tier change, treat it as
security-relevant and ask before proceeding.
