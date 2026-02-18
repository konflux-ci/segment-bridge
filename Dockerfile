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
FROM registry.access.redhat.com/ubi9/go-toolset:9.7-1771271449 AS builder
WORKDIR /build
# Build tkn-results binary for linux/amd64
ENV GOOS=linux
ENV GOARCH=amd64
RUN go build -o /build/tkn-results github.com/tektoncd/results/cmd/tkn-results@latest

# Second stage: Create the final container image
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
        curl \
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
