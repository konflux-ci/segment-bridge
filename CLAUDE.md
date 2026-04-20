# CLAUDE.md — AI assistant context for segment-bridge

## What this repo does

Shell + Go pipeline that fetches anonymous Tekton PipelineRun telemetry from
Konflux clusters and uploads it to Segment. See [CONTRIBUTING.md](CONTRIBUTING.md)
for full setup, testing, and review guidelines.

## Build & test commands

```bash
make setup          # install toolchain via mise, run pre-commit
make test           # go test ./... with pinned Go
make lint           # golangci-lint run
make pre-commit     # yamllint, shellcheck, gitlint, go-mod-tidy, golangci-lint

# Image build (requires podman login to redhat.com for base image)
podman build -t segment-bridge .

# Run tests against the built image (integration-style)
SEGMENT_BRIDGE_TEST_IMAGE=segment-bridge:test go test ./...
```

## Non-obvious conventions

- **Kwok, not Kind.** Local K8s simulation uses [kwok](https://kwok.sigs.k8s.io/)
  (Kubernetes Without Kubelet). It is extremely lightweight and used by all
  script tests via `containerfixture`. Do not introduce Kind clusters.

- **NDJSON on stdout.** Every `fetch-*.sh` script emits one compact JSON object
  per line to stdout. Diagnostic messages go to stderr. Downstream scripts
  (`get-konflux-public-info.sh`, `tekton-to-segment.sh`) consume this stream.

- **`set +e` scoping in the orchestrator.** `tekton-main-job.sh` wraps fetch
  calls in `{ set +e; ...; true; }` so a failing data source does not abort
  the pipeline. Individual scripts still use `set -o errexit`.

- **`kubectl` preferred over `oc`.** When both exist, scripts choose `kubectl`
  (kwok kubeconfigs work better with upstream kubectl). Override: `KUBECTL=oc`.

- **Conventional Commits.** Format: `type(JIRA-ID): description` with
  `Signed-off-by`. Enforced by gitlint pre-commit hook.

- **Pre-commit is mandatory.** Run `make pre-commit` before pushing. CI runs
  the same checks (yamllint, shellcheck, gitlint, golangci-lint, go-mod-tidy).

## Key environment variables

| Variable | Default | Used by |
|---|---|---|
| `TEKTON_RESULTS_API_ADDR` | `localhost:50051` | `fetch-tekton-records.sh` |
| `TEKTON_NAMESPACE` | `-` (all) | `fetch-tekton-records.sh` |
| `TEKTON_RESULTS_TOKEN` | SA token file | `fetch-tekton-records.sh` |
| `SEGMENT_BATCH_API` | `https://api.segment.io/v1/batch` | `segment-uploader.sh` |
| `SEGMENT_WRITE_KEY` | *(none)* | `tekton-main-job.sh` — generates `.netrc` |
| `CURL_NETRC` | `$HOME/.netrc` | `segment-uploader.sh` |
| `CLUSTER_ID` | `anonymous` | `tekton-to-segment.sh` — namespace hashing |
| `KUBECTL` | auto-detect | All `fetch-*.sh` / `get-konflux-public-info.sh` |
| `NAMESPACE_RECENT_HOURS` | `4` | `fetch-namespace-records.sh` |
| `COMPONENT_RECENT_HOURS` | `4` | `fetch-component-records.sh` |
| `SEGMENT_BRIDGE_TEST_IMAGE` | *(none)* | Go tests — run scripts inside image |
| `SEGMENT_BRIDGE_TEST_CONTAINER_RUNTIME` | auto (`podman`→`docker`) | Go tests |

## Toolchain

Pinned in `mise.toml`: Go, kubectl, oc, Python 3.11. Use `mise exec -- <cmd>`
or `make` targets (which wrap mise). Go version must match `go.mod`.
