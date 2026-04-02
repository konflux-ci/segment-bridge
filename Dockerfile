# Dockerfile
#
# Container image for the Tekton Results to Segment bridge.
# Fetches PipelineRun metrics from Tekton Results API, transforms them to
# anonymous Segment events, and uploads to Segment (directly or via proxy).
#
# Build:
#   podman build -t segment-bridge .
#
# For local builds (without CI), run ./scripts/prepare-oc-client-for-build.sh then
# build with the deps dir mounted where Konflux mounts the prefetch output:
#   podman build -v "$(pwd)/deps:/cachi2/output/deps:Z" -t segment-bridge .
#
# Usage:
#   podman run --rm \
#     -e TEKTON_RESULTS_API_ADDR=tekton-results-api-service:8080 \
#     -e TEKTON_NAMESPACE=default \
#     -e TEKTON_RESULTS_TOKEN="$(kubectl create token default -n default)" \
#     -e SEGMENT_BATCH_API=https://api.segment.io/v1/batch \
#     -e SEGMENT_WRITE_KEY=your-write-key \
#     segment-bridge
#

# First stage: Build the tkn-results binary
FROM registry.access.redhat.com/ubi9/go-toolset:9.7-1775042950 AS builder
ARG TARGETARCH
WORKDIR /build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} GOBIN=/build \
    go install github.com/tektoncd/results/cmd/tkn-results@v0.14.0

# Second stage: Extract OpenShift client (oc + kubectl) from prefetched tarball.
# Konflux mounts the Hermeto prefetch output at /cachi2; the tarball is not in the
# build context, so we must use RUN to read from the mounted path (not COPY).
# Only the extracted binaries are copied to the final image.
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest AS oc-client
ARG TARGETARCH
RUN microdnf install -y --nodocs tar gzip && microdnf clean all && rm -rf /var/cache/yum
RUN cp "/cachi2/output/deps/generic/openshift-client-linux-${TARGETARCH:-amd64}-rhel9.tar.gz" /tmp/oc.tar.gz && \
    tar -xzf /tmp/oc.tar.gz -C /tmp && \
    mv /tmp/oc /tmp/kubectl /usr/local/bin/ && \
    rm /tmp/oc.tar.gz

# Third stage: Create the final container image
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

LABEL \
    description="Tekton Results to Segment bridge for anonymous PipelineRun telemetry" \
    io.k8s.description="Tekton Results to Segment bridge for anonymous PipelineRun telemetry" \
    io.k8s.display-name="Segment Bridge" \
    io.openshift.tags="tekton,results,pipelinerun,metrics,konflux,segment,telemetry" \
    summary="This image contains tools and scripts for fetching anonymous \
PipelineRun execution metrics from Tekton Results API and sending them to Segment."

RUN microdnf install -y --nodocs \
        jq \
        bash \
        curl-minimal \
    && microdnf clean all \
    && rm -rf /var/cache/yum

# OpenShift client binaries (from prefetched tarball, extracted in oc-client stage)
COPY --from=oc-client /usr/local/bin/oc /usr/local/bin/kubectl /usr/local/bin/

# Copy the tkn-results binary from the builder stage
COPY --from=builder --chown=root:root --chmod=755 /build/tkn-results /usr/local/bin/tkn-results

COPY --chown=root:root --chmod=755 \
    scripts/fetch-tekton-records.sh \
    scripts/fetch-konflux-op-records.sh \
    scripts/fetch-namespace-records.sh \
    scripts/fetch-component-records.sh \
    scripts/get-konflux-public-info.sh \
    scripts/tekton-to-segment.sh \
    scripts/segment-uploader.sh \
    scripts/segment-mass-uploader.sh \
    scripts/mk-segment-batch-payload.sh \
    scripts/tekton-main-job.sh \
    /usr/local/bin/

COPY --chown=root:root --chmod=644 LICENSE /licenses/LICENSE

ENV TEKTON_RESULTS_API_ADDR="localhost:50051"
ENV TEKTON_NAMESPACE=""
ENV TEKTON_LIMIT="100"

# CLUSTER_ID is not set in the image: get-konflux-public-info.sh reads kube-system's
# metadata.uid when CLUSTER_ID is unset, then exports it for the pipeline. Set
# CLUSTER_ID in the workload only if you need to override (e.g. non-Kubernetes).
# tekton-to-segment.sh uses CLUSTER_ID="${CLUSTER_ID:-anonymous}" if still unset.

# Segment configuration
# URL can point to Segment directly or to a proxy endpoint
ENV SEGMENT_BATCH_API="https://api.segment.io/v1/batch"
ENV SEGMENT_RETRIES="3"
#
# Authentication: Always required via .netrc file (two deployment modes):
#   1. Direct to Segment: .netrc contains "machine api.segment.io"
#   2. Via proxy: .netrc contains "machine <proxy-host>"
#
# Two options to provide credentials:
#   Option 1: Set SEGMENT_WRITE_KEY - tekton-main-job.sh will generate a temp
#             .netrc file from it automatically.
# ENV SEGMENT_WRITE_KEY=""
#   Option 2: Mount a .netrc file and set CURL_NETRC path directly.
# ENV CURL_NETRC="/usr/local/etc/segment/netrc"

USER 1001

ENTRYPOINT ["/usr/local/bin/tekton-main-job.sh"]
