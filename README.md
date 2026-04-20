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
See the [`Dockerfile`](Dockerfile) for the image layout and typical environment
variables.

## Deployment

Kubernetes manifests live under [`config/`](config/) (Kustomize base). The CronJob
uses the published image default entrypoint (no `command` override), so the
Tekton pipeline runs automatically.

### `segment-bridge-config` Secret

The CronJob injects every key from a Secret named `segment-bridge-config`
as environment variables (`envFrom` / `secretRef`). The Secret is marked
`optional: true`, so the pod starts even when the Secret is absent — but
Segment uploads are skipped unless `SEGMENT_WRITE_KEY` is provided.

Create the Secret in the `segment-bridge` namespace:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: segment-bridge-config
  namespace: segment-bridge
stringData:
  SEGMENT_WRITE_KEY: "<your-segment-write-key>"
  # Add optional keys below as needed.
```

#### Required keys

| Key | Description |
|-----|-------------|
| `SEGMENT_WRITE_KEY` | Segment source write key. `tekton-main-job.sh` converts it into a `.netrc` file for the upload stage. When unset, fetch and transform still run but events are discarded. |

#### Optional keys

| Key | Default | Description |
|-----|---------|-------------|
| `TEKTON_RESULTS_API_ADDR` | `localhost:50051` | gRPC address of the Tekton Results API. |
| `TEKTON_RESULTS_TOKEN` | SA token file | Bearer token for the Tekton Results API. Falls back to the projected service-account token at `/var/run/secrets/kubernetes.io/serviceaccount/token`. |
| `TEKTON_NAMESPACE` | `-` (all) | Kubernetes namespace to query for PipelineRuns. `-` is the Tekton Results wildcard (all namespaces). |
| `TEKTON_LIMIT` | `100` | Maximum number of Tekton Results records to fetch per run. |
| `SEGMENT_BATCH_API` | `https://api.segment.io/v1/batch` | Segment batch endpoint URL. Change this when routing through a proxy. |
| `SEGMENT_RETRIES` | `3` | Number of retry attempts for each batch upload call. |
| `CURL_NETRC` | auto-generated | Path to a `.netrc` file for upload authentication. Normally auto-generated from `SEGMENT_WRITE_KEY`; set this only when mounting a pre-built `.netrc` (e.g. for proxy auth). |
| `CLUSTER_ID` | auto-detected | Cluster identifier used for namespace hashing (anonymization). Auto-detected from the `konflux-public-info` ConfigMap or the `kube-system` namespace UID. Override only for non-Kubernetes environments. |
| `NAMESPACE_RECENT_HOURS` | `4` | Time window (hours) for `fetch-namespace-records.sh`. Only namespaces created or updated within this window are emitted. |
| `COMPONENT_RECENT_HOURS` | `4` | Time window (hours) for `fetch-component-records.sh`. Only Components created or updated within this window are emitted. |
| `KUBECTL` | auto-detect | Override the Kubernetes CLI binary (`kubectl` or `oc`). When unset, scripts prefer `kubectl` if available. |

[1]: https://app.segment.com

Segment has a [built-in mechanism for removing duplicate events][ES1]. This
means we can safely resend the same event multiple times to increase delivery
reliability. The mechanism uses the `messageId` [common message field][ES2].

Segment also has a [*batch* call][ES3] that allows sending multiple events in
one request. There is a limit of 500KB per call; individual event JSON records
should not exceed 32KB.

The uploader splits the stream into ~500KB chunks and retries failed batch
calls (configurable, default three attempts).

[ES1]: https://segment.com/blog/exactly-once-delivery/
[ES2]: https://segment.com/docs/connections/spec/common/
[ES3]: https://segment.com/docs/connections/sources/catalog/libraries/server/http-api/#batch

## Contributing

Please refer to the [contribution guide](./CONTRIBUTING.md).
