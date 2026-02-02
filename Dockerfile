# Dockerfile.tekton
#
# Container image for fetching PipelineRun metrics from Tekton Results API.
# This is part of the new Tekton Results bridge (replacing the Splunk-based approach).
#
# Build:
#   podman build -f Dockerfile.tekton -t tekton-results-bridge .
#
# Usage:
#   podman run --rm \
#     -e TEKTON_RESULTS_API_ADDR=tekton-results-api-service:8080 \
#     -e TEKTON_NAMESPACE=default \
#     -e TEKTON_RESULTS_TOKEN="$(kubectl create token default -n default)" \
#     tekton-results-bridge
#

# First stage: Build the tkn-results binary
FROM registry.access.redhat.com/ubi9/go-toolset:9.5-1739801907 AS builder
WORKDIR /build
# Build tkn-results binary for linux/amd64
ENV GOOS=linux
ENV GOARCH=amd64
RUN go build -o /build/tkn-results github.com/tektoncd/results/cmd/tkn-results@latest

# Second stage: Create the final container image
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

LABEL \
    description="Tooling for fetching anonymous PipelineRun metrics from Tekton Results" \
    io.k8s.description="Tooling for fetching anonymous PipelineRun metrics from Tekton Results" \
    io.k8s.display-name="Tekton Results Bridge" \
    io.openshift.tags="tekton,results,pipelinerun,metrics,konflux" \
    summary="This image contains tools and scripts for fetching anonymous \
PipelineRun execution metrics from Tekton Results API and sending them to Segment."

RUN microdnf install -y --nodocs \
        jq \
        bash \
    && microdnf clean all \
    && rm -rf /var/cache/yum

# Copy the tkn-results binary from the builder stage
COPY --from=builder --chown=root:root --chmod=755 /build/tkn-results /usr/local/bin/tkn-results

COPY --chown=root:root --chmod=755 scripts/fetch-tekton-records.sh /usr/local/bin/

ENV TEKTON_RESULTS_API_ADDR="localhost:50051"
ENV TEKTON_NAMESPACE=""
ENV TEKTON_LIMIT="100"

USER 1001

ENTRYPOINT ["/usr/local/bin/fetch-tekton-records.sh"]
