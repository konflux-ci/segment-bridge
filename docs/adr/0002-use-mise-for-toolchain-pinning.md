# 2. Use mise for Toolchain Pinning

Date: 2026-04-16

## Status

Accepted

## Context

The project mixes several toolchains: Go (production code and tests),
Python (`pre-commit`, `gitlint`), `kubectl` and `oc` (cluster access in
scripts and integration tests), and `kind` (local cluster fixtures).
CI must use the exact same versions as developer machines, otherwise
"works on my laptop" failures appear in pipelines and vice versa.

Options considered:

1. Per-tool managers (`gvm`, `pyenv`, `kubectx` plus manual `oc` and
   `kind` downloads). Five tools, five workflows, no single source of
   truth.
2. Container-only development (require everyone to develop inside the
   image). Slow inner loop; awkward for IDE integration.
3. `asdf` with multiple plugins. Plugin quality varies; community
   support is shrinking.
4. [`mise`](https://mise.jdx.dev/) (formerly `rtx`). Single binary,
   per-repo `mise.toml`, supports Go, Python, kubectl, kind, and `oc`
   via first-party plugins.

## Decision

Use `mise` as the single toolchain version manager. Pin every tool the
repo cares about in [`mise.toml`](../../mise.toml). The
`mise run pre-commit` task installs Python deps from
[`requirements.lock`](../../requirements.lock) into a project-local
`.venv/` and runs `pre-commit run --all-files`.

The [`Makefile`](../../Makefile) targets (`setup`, `test`, `lint`,
`pre-commit`) all delegate to `mise` so there is one place to change
toolchain invocation.

## Consequences

- Contributors install `mise` once, then `make setup` provisions
  everything pinned for this repo.
- CI uses the same versions because workflows call `mise exec --` or
  rely on the pinned `go-version-file: 'go.mod'` matching `mise.toml`.
- New contributors must learn one extra tool, but the `Makefile`
  hides that for the common cases.
- If `mise` is unavailable, contributors can still install the pinned
  versions manually by reading `mise.toml`.
