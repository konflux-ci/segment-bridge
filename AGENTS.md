# AGENTS.md — AI coding agent context for segment-bridge

This file is the single source of truth for **all** AI coding agents working on
this repository (Cursor, Claude Code, Copilot, Codex, etc.).

## What this repo does

Shell + Go pipeline that fetches anonymous Tekton PipelineRun telemetry from
[Konflux](https://konflux-ci.dev/) clusters and uploads it to
[Segment](https://segment.com/) (and downstream analytics such as Amplitude).

See [CONTRIBUTING.md](CONTRIBUTING.md) for full setup, testing, and review
guidelines.

The container entrypoint (`scripts/tekton-main-job.sh`, installed to
`/usr/local/bin/tekton-main-job.sh` in the image) orchestrates:

1. Fetch PipelineRun records and related cluster context (Tekton Results API,
   Kubernetes API).
2. Enrich with public Konflux metadata.
3. Map to Segment batch events via jq transforms.
4. Upload in ~500 KB chunks with deduplication via `messageId`.

## Build and test commands

```bash
make setup          # install toolchain via mise, run pre-commit
make test           # go test -race ./... with pinned Go
make lint           # golangci-lint run
make pre-commit     # yamllint, shellcheck, gitlint, go-mod-tidy, golangci-lint

# Image build (requires podman login to redhat.com for base image)
podman build -t segment-bridge .

# Run tests against the built image (integration-style)
SEGMENT_BRIDGE_TEST_IMAGE=segment-bridge:test go test ./...
```

### Single-file verification

Lint and type-check a single file (fast, no full build):

```bash
golangci-lint run ./path/to/file.go
shellcheck path/to/file.sh
yamllint path/to/file.yaml
```

## Repository layout

| Path | Purpose |
|------|---------|
| `scripts/` | Shell scripts: fetch, transform, upload pipeline |
| `scripts/jq/` | jq transforms mapping NDJSON to Segment events |
| `segment/`, `tekton-to-segment/`, … | Go test package per script (one dir mirrors one `scripts/*.sh`) |
| `tekton-e2e/` | End-to-end tests (build tag `e2e`) |
| `*fixture/` | Go test helpers (container runtime, kwok clusters, HTTP mocks) |
| `kwok/` | Kwok Dockerfile + manifests for local K8s simulation |
| `config/` | Kubernetes Kustomize manifests for deployment |
| `schema/` | JSON Schema definitions for Segment analytics events |
| `data/` | Static data files (e.g. CA trust bundles) |
| `stats/` | Go utility: simple statistics helpers used by tests |
| `skills/` | Agent skill files for common change types |
| `docs/adr/` | Architecture Decision Records |

## Non-obvious conventions

### Kwok, not Kind

Local Kubernetes simulation uses [kwok](https://kwok.sigs.k8s.io/) (Kubernetes
Without Kubelet). It is extremely lightweight and used by all script tests via
`containerfixture`. **Do not introduce Kind clusters in tests.** `mise.toml`
installs Kind as a toolchain pin but it is not used in this project's test
suite — all local cluster simulation must go through kwok.

### NDJSON on stdout

Every `fetch-*.sh` script emits one compact JSON object per line to stdout.
Diagnostic messages go to stderr. Downstream scripts (`get-konflux-public-info.sh`,
`tekton-to-segment.sh`) consume this stream.

### `set +e` scoping in the orchestrator

`tekton-main-job.sh` wraps fetch calls in `{ set +e; ...; true; }` so a failing
data source does not abort the pipeline. Individual scripts still use
`set -o errexit`.

### `kubectl` preferred over `oc`

When both exist, scripts choose `kubectl` (kwok kubeconfigs work better with
upstream kubectl). Override: `KUBECTL=oc`.

## Commit message format

Conventional Commits enforced by gitlint (see `.gitlint`). Prefer
`type(JIRA-ID): description` when a ticket applies. Every commit must satisfy:

- **Title:** `type(scope): description` — type is one of: fix, feat, chore,
  docs, style, refactor, perf, test, revert, ci, build.
- **Title length:** ≤72 characters.
- **Blank line** after title.
- **Body line length:** ≤72 characters (wrap long lines).
- **Signed-off-by:** body must contain
  `Signed-off-by: Full Name <email>`.

Example:

```text
feat(PROJ-123): add fetch script

Add script to fetch Konflux operator CR from the
cluster, with kwok tests.

Signed-off-by: Your Name <your-email@example.com>
```

Default branch is `main`; open PRs against `main`.

**Validation:** run `gitlint --commits HEAD~1..HEAD` before pushing (or
`gitlint --commits origin/main..HEAD` for a multi-commit branch).

## Testing conventions

- **Go tests** are colocated with production code (`*_test.go`).
- **Shell scripts** are tested via Go using `containerfixture` + kwok (not
  direct bash tests).
- **TDD approach:** manual run on real cluster once; automated tests on kwok.
- Prefer **real or sample input files** from the repo in tests instead of
  inventing minimal fixtures; export CRDs or data from a live cluster when
  needed.
- **Integration tests:** set `SEGMENT_BRIDGE_TEST_IMAGE` to run scripts inside
  the built container image.
- **E2E tests:** use `-tags=e2e` build tag, located in `tekton-e2e/`.
- **Code coverage:**  New code must not decrease line coverage.

## Key environment variables

| Variable | Default | Used by |
|----------|---------|---------|
| `TEKTON_RESULTS_API_ADDR` | `https://localhost:8443` | `fetch-tekton-records.sh` |
| `TEKTON_NAMESPACE` | `-` (all) | `fetch-tekton-records.sh` |
| `TEKTON_RESULTS_TOKEN` | SA token file | `fetch-tekton-records.sh` |
| `SEGMENT_BATCH_API` | `https://api.segment.io/v1/batch` | `segment-uploader.sh` |
| `CURL_NETRC` | `$HOME/.netrc` | `segment-uploader.sh` |
| `SEGMENT_WRITE_KEY` | *(none)* | `tekton-main-job.sh` — generates `.netrc` |
| `CLUSTER_ID` | `anonymous` | `tekton-to-segment.sh` — namespace hashing |
| `KUBECTL` | auto-detect | All `fetch-*.sh` / `get-konflux-public-info.sh` |
| `TEKTON_CURSOR_CONFIGMAP` | `segment-bridge-cursor` | `fetch-tekton-records.sh` — cursor ConfigMap name |
| `SEGMENT_BRIDGE_TEST_IMAGE` | *(none)* | Go tests — run scripts inside image |

All other env vars are documented in script headers and discoverable via `grep -r 'export\|:-'`.

## Toolchain

Go module: `github.com/redhat-appstudio/segment-bridge.git` (see `go.mod`).

Pinned in `mise.toml`: Go (version must match `go.mod`), kubectl, oc,
Python 3.11. Use `mise exec -- <cmd>` or `make` targets (which wrap mise).
Always check `mise.toml` and `go.mod` for the authoritative Go version:
do not rely on any version number written in documentation.

## Pattern references (skills)

For common change types, follow the canonical skill under `skills/` (also
linked via `.claude/skills/`):

| Change type | Skill |
|-------------|-------|
| New data source / fetch script | `skills/adding-a-data-source/SKILL.md` |
| Segment event mapping change | `skills/updating-segment-event-mappings/SKILL.md` |
| New kwok test fixture | `skills/adding-a-kwok-test-fixture/SKILL.md` |
| CI workflow change | `skills/modifying-ci-workflows/SKILL.md` |
| Architecture Decision Record | `skills/adding-an-adr/SKILL.md` |

## Do not

- **Do not** commit changes to `.vscode/settings.json` (generated by the mise
  plugin).
- **Do not** introduce Kind clusters in tests — use kwok.
- **Do not** push without running `make pre-commit` and
  `gitlint --commits origin/main..HEAD`. If pre-commit fails, fix locally and
  amend — do not add fix-up commits.
- **Do not** add new sample/fixture YAML directories without adding them to
  `.yamllint.yaml`'s `ignore` list.
- **Do not** commit `.env` files, secrets, or credentials.
