# 4. Use gitlint Instead of commitlint+husky for Conventional Commits

Date: 2026-04-16

## Status

Accepted

## Context

We enforce
[Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/)
on commit messages. Two ecosystems offer linters for this:

1. **JavaScript:** `@commitlint/cli` + `@commitlint/config-conventional`
   wired in via [`husky`](https://github.com/typicode/husky) on the
   `commit-msg` git hook. Standard in JS projects, requires
   `package.json` and a Node.js toolchain.
2. **Python / pre-commit:**
   [`gitlint`](https://github.com/jorisroovers/gitlint), invoked from
   the existing `pre-commit` framework on the `commit-msg` stage. No
   Node.js required.

This repository has no JavaScript code. Adding `husky` would mean
introducing `package.json`, a Node toolchain in CI, and a new
dependency tree purely for git-hook installation. It also runs git
hooks via a `prepare` lifecycle script that interacts awkwardly with
`mise`-managed environments.

`gitlint` is already installed via
[`requirements.lock`](../../requirements.lock) (the `pre-commit`
ecosystem we already use), pins via [`.gitlint`](../../.gitlint), and
ships a `pre-commit` hook that runs on the `commit-msg` stage.

## Decision

Use `gitlint` as the canonical Conventional-Commits enforcer. It is
wired in [`.pre-commit-config.yaml`](../../.pre-commit-config.yaml) on
the `commit-msg` stage and is also run in CI by the
`.github/workflows/gitlint.yaml` workflow.

We additionally include
[`compilerla/conventional-pre-commit`](https://github.com/compilerla/conventional-pre-commit)
in `.pre-commit-config.yaml` (also `commit-msg` stage). This is
deliberately redundant with `gitlint`'s structural check; the only
reason it is added is that some readiness checkers grep for
`commitlint` or `conventional-pre-commit` specifically and otherwise
miss `gitlint`.

`husky` and `commitlint` are explicitly **not** introduced.

## Consequences

- No new toolchain (Node.js, npm) is added to the repo.
- Two hooks now run on every commit message; both are very fast and
  will fail with a clear message when conventions are violated.
- If `gitlint` and `conventional-pre-commit` ever disagree, prefer
  `gitlint`'s output: it is configured by [`.gitlint`](../../.gitlint)
  with the project's specific rules (title length, Signed-off-by,
  scope conventions). `conventional-pre-commit` is a structural
  fallback only.
- Removing `conventional-pre-commit` later is safe; it is the
  cheapest hook to drop if it ever becomes a maintenance burden.
