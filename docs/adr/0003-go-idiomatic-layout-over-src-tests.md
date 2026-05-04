# 3. Use Go-Idiomatic Layout Instead of src/ and tests/

Date: 2026-04-16

## Status

Accepted

## Context

Some readiness checkers (notably AgentReady, see
[`.agentready/report-20260416-182353.md`](../../.agentready/report-20260416-182353.md))
score repositories higher when source code lives under `src/` and
tests under a top-level `tests/` directory. That convention is
idiomatic in Python and some JavaScript ecosystems.

Go's idioms are different:

- A repository is one or more Go modules; packages map directly to
  directories under the module root.
- Tests are written in the same package as the code they exercise and
  live in `_test.go` files in the same directory. `go test ./...`
  walks the tree.
- Public import paths follow directory paths verbatim. Moving a
  package changes its import path and breaks every consumer.

This repo has external Go importers using paths like:

- `github.com/redhat-appstudio/segment-bridge.git/segment`
- `github.com/redhat-appstudio/segment-bridge.git/tekton-to-segment`
- `github.com/redhat-appstudio/segment-bridge.git/scripts`

It also has shell scripts under [`scripts/`](../../scripts/) that are
copied into the production container image at fixed paths
(`/usr/local/bin/*.sh`); the [`Dockerfile`](../../Dockerfile) and
[`config/base/cronjob.yaml`](../../config/base/cronjob.yaml) reference
those paths.

## Decision

Keep the existing flat, Go-idiomatic layout. Do not introduce a
top-level `src/` or `tests/` directory. Tests remain colocated with
the code they exercise as `*_test.go` files in the same package.

When AgentReady (or a similar checker) flags this as a missing
directory, treat it as a known false negative for Go and refer back
to this ADR rather than reorganizing the tree.

## Consequences

- Existing importers and the Konflux pipeline continue to work
  unchanged.
- The AgentReady "Standard Project Layouts" check stays at 0/100; the
  cost is roughly 10 score points and is accepted.
- New contributors familiar with Python-style layouts may be initially
  surprised; this ADR plus the
  [`AGENTS.md`](../../README.md) hand-off (when added) document the
  convention.
