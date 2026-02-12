# Dockerfile
#
# Container image for the Tekton Results to Segment bridge.
# Fetches PipelineRun metrics from Tekton Results API, transforms them to
# anonymous Segment events, and uploads to Segment (directly or via proxy).
#
# Build:
#   podman build -t segment-bridge .
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
FROM registry.access.redhat.com/ubi9/go-toolset:1.25.5-1770596585 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /build

# Copy Go module files and download dependencies (supports Cachi2 prefetching)
COPY --chown=1001:0 go.mod go.sum ./
RUN go mod download

# Copy the tools.go build-tag file so `go build` can resolve the dependency
COPY --chown=1001:0 tools.go ./

# Build tkn-results binary for the target platform (set automatically by buildah
# in multi-platform builds; defaults to linux/amd64 for single-platform builds)
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /build/tkn-results github.com/tektoncd/results/cmd/tkn-results

# Second stage: Create the final container image
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

ARG VERSION=0.1.0
ARG RELEASE=1

LABEL \
    com.redhat.component="segment-bridge-container" \
    name="segment-bridge" \
    version="${VERSION}" \
    release="${RELEASE}" \
    vendor="Red Hat, Inc." \
    url="https://github.com/konflux-ci/segment-bridge" \
    distribution-scope="public" \
    description="Tekton Results to Segment bridge for anonymous PipelineRun telemetry" \
    io.k8s.description="Tekton Results to Segment bridge for anonymous PipelineRun telemetry" \
    io.k8s.display-name="Segment Bridge" \
    io.openshift.tags="tekton,results,pipelinerun,metrics,konflux,segment,telemetry" \
    summary="This image contains tools and scripts for fetching anonymous \
PipelineRun execution metrics from Tekton Results API and sending them to Segment."

# Install runtime dependencies.
# bash and curl (curl-minimal) are already included in ubi-minimal.
# In hermetic builds, jq is supplied by Cachi2 RPM prefetching (see rpms.in.yaml).
RUN microdnf install -y --nodocs jq \
    && microdnf clean all \
    && rm -rf /var/cache/yum

# Copy the tkn-results binary from the builder stage
COPY --from=builder --chown=root:root --chmod=755 /build/tkn-results /usr/local/bin/tkn-results

COPY --chown=root:root --chmod=755 \
    scripts/fetch-tekton-records.sh \
    scripts/tekton-to-segment.sh \
    scripts/segment-uploader.sh \
    scripts/segment-mass-uploader.sh \
    scripts/mk-segment-batch-payload.sh \
    scripts/tekton-main-job.sh \
    /usr/local/bin/

ENV TEKTON_RESULTS_API_ADDR="localhost:50051"
ENV TEKTON_NAMESPACE=""
ENV TEKTON_LIMIT="100"

# Cluster ID for namespace hashing (anonymization)
# Should be set by the CronJob/Operator to the cluster's unique ID
ENV CLUSTER_ID="anonymous"

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
