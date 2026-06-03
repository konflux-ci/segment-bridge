# segment-bridge

Bridge anonymous [Tekton](https://tekton.dev/) PipelineRun telemetry from Konflux
clusters into [Segment][1] (and downstream analytics such as Amplitude).

```mermaid
flowchart TB
    subgraph A["Konflux cluster"]
        A1["Tekton Results API"]
        A2["Kubernetes API"]
    end

    subgraph B["segment-bridge container"]
        B1["fetch-tekton-records.sh"]
        B1b["fetch-konflux-op-records.sh"]
        B1c["fetch-namespace-records.sh"]
        B2["get-konflux-public-info.sh"]
        B3["tekton-to-segment.sh"]
        subgraph B4["segment-mass-uploader.sh"]
            B4C([split])
            B4A([segment-uploader.sh])
            B4B([mk-segment-batch-payload.sh])
            B4C--"Segment events (~490KB batches)"-->B4A
            B4A--"events"-->B4B--"batch payload"-->B4A
        end
        B1 --> B2
        B1b --> B2
        B1c --> B2
        B2 --> B3
        B3 --> B4
    end

    A1 --> B1
    A2 --> B1b
    A2 --> B1c

    G([Segment])
    H[(Amplitude)]
    B4 --> G --> H
```

**Note:** If you cannot see the drawing above in GitHub, make sure you are not
blocking JavaScript from *viewscreen.githubusercontent.com*.

The container entrypoint [`tekton-main-job.sh`](scripts/tekton-main-job.sh)
orchestrates: fetch PipelineRun records and related cluster context, enrich with
public Konflux metadata, map to Segment batch events, then upload in chunks.

## Quick Start

```bash
make setup   # install toolchain (mise), pre-commit hooks
make test    # run all Go tests
make lint    # golangci-lint
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full development guide.

## Installation

**Prerequisites:**

- [mise](https://mise.jdx.dev/) (installs Go, kubectl, oc, Python automatically)
- [Podman](https://podman.io/) (for building/running the container image)
- curl, jq (typically pre-installed on Linux/macOS)

**Setup from a fresh clone:**

```bash
git clone https://github.com/konflux-ci/segment-bridge.git
cd segment-bridge
make setup
```

`make setup` installs all pinned tool versions via mise and configures
pre-commit hooks. Run `make help` to see all available targets.

## Usage

**Build and run the container image locally:**

```bash
# Prepare the OpenShift client tarball (one-time)
./scripts/prepare-oc-client-for-build.sh

# Build the image
podman build -v "$(pwd)/deps:/cachi2/output/deps:Z" -t segment-bridge .

# Run (adjust env vars for your cluster)
podman run --rm \
  -e TEKTON_RESULTS_API_ADDR=tekton-results-api-service:8080 \
  -e TEKTON_NAMESPACE=default \
  -e TEKTON_RESULTS_TOKEN="$(kubectl create token default -n default)" \
  -e SEGMENT_WRITE_KEY=your-write-key \
  segment-bridge
```

See the [`Dockerfile`](Dockerfile) header for additional usage examples and
[CLAUDE.md](CLAUDE.md) for the full environment variable reference.

## Deployment

- Kubernetes manifests: [`config/`](config/) (Kustomize base)
- The CronJob runs the published image entrypoint automatically
- Requires a `segment-bridge-config` Secret with `SEGMENT_WRITE_KEY`
  (Secret is `optional: true` — pod starts without it, but uploads are skipped)
- Segment [deduplicates events][ES1] via `messageId`, so resending is safe
- The uploader splits into ~500 KB [batch calls][ES3] and retries failures

Create the Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: segment-bridge-config
  namespace: segment-bridge
stringData:
  SEGMENT_WRITE_KEY: "<your-segment-write-key>"
```

See [CLAUDE.md](CLAUDE.md) for the full list of environment variables and
their defaults.

[1]: https://app.segment.com
[ES1]: https://segment.com/blog/exactly-once-delivery/
[ES3]: https://segment.com/docs/connections/sources/catalog/libraries/server/http-api/#batch

## Contributing

Please refer to the [contribution guide](./CONTRIBUTING.md).
