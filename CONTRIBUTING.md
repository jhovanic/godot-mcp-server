# Contributing

Thanks for considering a contribution. Read this before opening a PR — the acceptance bar here is
deliberately narrower than "does it work."

## Priority order

This project is built in this order, and PRs are reviewed against it:

1. **Security** — the no-eval, path-allowlisted, localhost-only model in `SECURITY.md`
2. **Features** — new capabilities that extend the *scoped* operation set
3. **Quality-of-life** — everything else

A convenient feature that weakens #1 will not be merged as-is. It may come back as an explicit,
off-by-default "advanced" tool with its own documented risk section — see `SECURITY.md`'s
won't-fix list for what that boundary looks like in practice.

## Before opening a PR

- Discuss non-trivial changes in an issue first, especially anything touching tool registration,
  path validation, or the TCP tier's binding/exposure.
- Run `gofmt` and `golangci-lint run` clean.
- Add table-driven tests for new tool handlers. Path-related code needs explicit traversal-attempt
  test cases, not just the happy path.
- Update `SECURITY.md` if your change affects the threat model (new tool category, new I/O
  surface, anything that changes what an untrusted AI client could reach).

## Adding a new tool

A new tool must be:

- **Parameterized and scoped** — a specific operation with a specific, validated input shape. Not
  a general execution primitive.
- **Logged** — operation, params, and result recorded via the existing logging path.
- **Documented** — what it does, what it can't do, and any risk notes, in the tool's own
  registration and in the README's tool list if it's user-facing.

If your use case genuinely needs something broader than a scoped operation, open an issue
describing the *need* before writing the code — there's often a scoped version of the same
capability that doesn't require a broader primitive.

## What won't be merged

See `SECURITY.md`'s "Won't-fix / won't-add" section. In short: arbitrary code execution tools,
removal of path validation, and non-loopback binding by default are off the table regardless of
convenience gained.

## Code style

- Standard Go project layout and idioms.
- Prefer typed structs over `map[string]interface{}` for tool parameters.
- Keep the headless-CLI-tier GDScript entry point thin — logic and validation belong in Go, not in
  the `.gd` script.

## Release process

Releases are cut via `goreleaser` on tag push. GitHub Actions steps must reference third-party
actions by pinned commit SHA, not by mutable tag. See `CLAUDE.md` for current CI conventions.

## Questions

Open an issue. If it's a security question specifically, see the reporting process in
`SECURITY.md` rather than a public issue.
