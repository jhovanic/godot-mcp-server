# Security

`godot-mcp-server` exists because the alternative Godot MCP servers didn't offer a trust model
their authors could fully account for. This document states that model explicitly, so it can be
audited rather than assumed — and so it stays honest as the tool grows.

## Design priority

**Security first, then features, then quality-of-life.** Every change to this project is evaluated
in that order. A feature that widens the trust boundary does not ship just because it's useful.

## What this server will never do

These are permanent constraints, not v1 limitations:

- **No arbitrary code execution tool.** There is no "run this GDScript" / "eval this" tool, and
  requests to add one will be declined. This is the single biggest risk surface in comparable
  tools and the primary reason this project exists.
- **No unscoped filesystem access.** Every file operation validates that its target path resolves
  inside the configured project root before touching disk. Path traversal (`../`) is rejected
  outright, not sanitized-and-allowed.
- **No non-local network exposure by default.** The TCP runtime tier binds to `127.0.0.1` only.
  It is not designed to be exposed beyond loopback, and doing so is out of scope for this project.

## What this server does instead

- A **fixed, explicit set of parameterized operations** — read scene tree, edit a specific node
  property, read/write a specific script region, etc. New capabilities are added as new
  parameterized operations, not as more general execution primitives.
- **Read and write tools are distinguished, and separately gated.** Read-only inspection tools
  carry materially less risk than write/edit tools, and are treated differently in review. This is
  also enforced mechanically: the server defaults to `-mode read-only`, in which write tools are
  never registered with the MCP client at all — not merely rejected if called. Widening to
  `-mode read-write` is an explicit, per-run opt-in the operator makes at startup; it is never
  implicit and there is no way to enable it from inside an MCP session.
- **Every tool invocation is logged** — the operation, its parameters, and its result — so there's
  an audit trail independent of the AI client's own logs. Every entry always goes to stderr, and
  by default is *also* written to `logs/<session>.txt` next to the running binary, so a human has
  a durable file to review later rather than depending on whatever the MCP client host does with
  stderr. This is a best-effort convenience layered on top of the stderr guarantee, not a
  replacement for it: a filesystem problem writing that file (e.g. a read-only install location)
  logs a warning and the server keeps running on stderr alone, it never fails startup. An
  additional custom path can be set via `-audit-log`.

## Threat model summary

| Actor | Assumed trust | What they can affect |
|---|---|---|
| The AI client (e.g. Claude Code) | Untrusted — may be manipulated by content it reads (files, web pages, etc.) | Only the operations explicitly exposed as tools, scoped to the project root |
| A process on the local machine | Trusted (same-user assumption) | Can reach the TCP tier on localhost — this is a same-machine trust boundary, not a network one |
| A remote attacker | Untrusted, no direct access | No path to this server unless the operator deliberately exposes the TCP tier beyond loopback (unsupported) |

## Reporting a vulnerability

Please report vulnerabilities privately via this repository's Security tab → "Report a vulnerability" button (GitHub Private Vulnerability Reporting). This opens a private, structured report visible only to you and the maintainers — do not open a public issue for a security finding, since public issues in this repo are, by definition, readable by everyone before a fix ships.

What happens after you submit:

Maintainers are notified and the report is triaged (accepted, more questions, or declined).
If accepted, we collaborate privately on a fix — often via a temporary private fork attached to the report.
Once a patch is released, the advisory is published, crediting the reporter (unless anonymity is requested), and a CVE is requested if warranted.

If you're unable to use GitHub's reporting flow for any reason, open a regular issue asking for an alternative contact method — without describing the vulnerability itself — and a maintainer will follow up privately.

## Won't-fix / won't-add

Requests that would move this project off its stated model will be declined, including:

- A general-purpose script execution tool
- Disabling path validation, even behind a flag, without a strong isolated justification
- Making the TCP tier bind beyond localhost by default

If a real use case needs something in this category, the right shape is usually an **explicit,
off-by-default, clearly labeled "advanced" tool** with its own documented risk — not a loosening
of the defaults.
