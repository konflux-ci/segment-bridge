# AGENTS.md — AI coding agent context for segment-bridge

This file is the single source of truth for **all** AI coding agents working on
this repository (Cursor, Claude Code, Copilot, Codex, etc.). Agent-specific
files like `CLAUDE.md` may exist as thin complements; this file is canonical.

## What this repo does

Shell + Go pipeline that fetches anonymous Tekton PipelineRun telemetry from
[Konflux](https://konflux-ci.dev/) clusters and uploads it to
[Segment](https://segment.com/) (and downstream analytics such as Amplitude).

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
| `fetch-*/` | Go test packages for each fetch script |
| `fetch-konflux-op-records/` | Go tests for `fetch-konflux-op-records.sh` |
| `get-konflux-public-info/` | Go tests for `get-konflux-public-info.sh` |
| `tekton-main-job/` | Go tests for the `tekton-main-job.sh` orchestrator |
| `tekton-to-segment/` | Go tests + sample fixtures for the transform step |
| `segment/` | Go tests for the uploader |
| `tekton-e2e/` | End-to-end tests (build tag `e2e`) |
| `containerfixture/` | Go test helper: run scripts inside containers |
| `testfixture/` | Go test helper: manage kwok clusters for tests |
| `webfixture/` | Go test helper: HTTP server fixture for upload tests |
| `stats/` | Go utility: simple statistics helpers used by tests |
| `kwok/` | Kwok Dockerfile + manifests for local K8s simulation |
| `config/` | Kubernetes Kustomize manifests for deployment |
| `schema/` | JSON Schema definitions for Segment analytics events |
| `data/` | Static data files (e.g. CA trust bundles) |
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

### Pre-commit is mandatory

Run `make pre-commit` before pushing. CI runs the same checks (yamllint,
shellcheck, gitlint, golangci-lint, go-mod-tidy, gitleaks, markdownlint,
actionlint).

## Commit message format

Conventional Commits enforced by gitlint (see `.gitlint`). Every commit must
satisfy:

- **Title:** `type(scope): description` — type is one of: fix, feat, chore,
  docs, style, refactor, perf, test, revert, ci, build.
- **Title length:** ≤72 characters.
- **Blank line** after title.
- **Body line length:** ≤72 characters (wrap long lines).
- **Signed-off-by:** body must contain
  `Signed-off-by: Full Name <email>`.

Example:

```text
feat(KFLUXVNGD-785): add fetch script

Add script to fetch Konflux operator CR from the
cluster, with kwok tests.

Signed-off-by: Your Name <your-email@example.com>
```

**Validation:** run `gitlint --commits HEAD~1..HEAD` before pushing.

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
- **Code coverage:** new and changed code must meet project coverage
  thresholds. See `codecov.yml` and the CI workflows that upload coverage
  (`unit_tests.yaml`, `shell_coverage.yaml`, `e2e_tests.yaml`) for the
  authoritative configuration — do not hardcode threshold percentages in
  documentation.

## Key environment variables

| Variable | Default | Used by |
|----------|---------|---------|
| `TEKTON_RESULTS_API_ADDR` | `https://localhost:8443` | `fetch-tekton-records.sh` |
| `TEKTON_NAMESPACE` | `-` (all) | `fetch-tekton-records.sh` |
| `TEKTON_RESULTS_TOKEN` | SA token file | `fetch-tekton-records.sh` |
| `SEGMENT_BATCH_API` | `https://api.segment.io/v1/batch` | `segment-uploader.sh` |
| `SEGMENT_WRITE_KEY` | *(none)* | `tekton-main-job.sh` |
| `CURL_NETRC` | `$HOME/.netrc` | `segment-uploader.sh` |
| `CLUSTER_ID` | `anonymous` | `tekton-to-segment.sh` |
| `KUBECTL` | auto-detect | All `fetch-*.sh` / `get-konflux-public-info.sh` |
| `NAMESPACE_RECENT_HOURS` | `4` | `fetch-namespace-records.sh` |
| `COMPONENT_RECENT_HOURS` | `4` | `fetch-component-records.sh` |
| `TEKTON_LIMIT` | `100` | `fetch-tekton-records.sh` |
| `TEKTON_MAX_PAGES` | `100` | `fetch-tekton-records.sh` |
| `TEKTON_CURSOR` | *(none)* | `fetch-tekton-records.sh` |
| `SEGMENT_RETRIES` | `3` | `segment-uploader.sh` |
| `SEGMENT_BRIDGE_TEST_IMAGE` | *(none)* | Go tests |
| `SEGMENT_BRIDGE_TEST_CONTAINER_RUNTIME` | auto (`podman`→`docker`) | Go tests |

## Toolchain

Pinned in `mise.toml`: Go (version must match `go.mod`), kubectl, oc,
Python 3.11. Use `mise exec -- <cmd>` or `make` targets (which wrap mise).
Always check `mise.toml` and `go.mod` for the authoritative Go version —
do not rely on any version number written in documentation.

## Pattern references (skills)

For common change types, follow the canonical skill under `skills/`:

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
  `gitlint --commits origin/main..HEAD`.
- **Do not** add new sample/fixture YAML directories without adding them to
  `.yamllint.yaml`'s `ignore` list.
- **Do not** commit `.env` files, secrets, or credentials.

## Yamllint: avoiding failures

The project uses yamllint (config in `.yamllint.yaml`). If you add a new sample
or fixture directory containing YAML with long lines or non-standard formatting,
add it to the `ignore` list in `.yamllint.yaml`. Do not reformat fixture YAML —
keep test data realistic.

## Code review expectations

- 2 approvals required per PR.
- All comments must be addressed (fixed or replied).
- PRs should be atomic and focused — split large changes.
- All new functionality must have unit tests.
- PRs must be open for at least 1 workday across all team time zones.
