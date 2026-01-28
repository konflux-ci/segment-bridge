#!/bin/bash
# fetch-tekton-records.sh
#   Fetch PipelineRun records from Tekton Results API.
#   Outputs decoded PipelineRun JSON objects to STDOUT (one per line).
#   Filters out TaskRuns - only PipelineRuns are returned.
#
#   This script is part of the Tekton Results bridge pipeline:
#   fetch-tekton-records.sh | tekton-to-segment.sh | segment-mass-uploader.sh
#
set -o pipefail -o errexit -o nounset

# ======= Parameters ======
# The following variables can be set from outside the script by setting
# similarly named environment variables.
#
# The Tekton Results API address (gRPC)
TEKTON_RESULTS_API_ADDR="${TEKTON_RESULTS_API_ADDR:-localhost:50051}"
#
# Authentication token for Tekton Results API
# If not set, will attempt to read from K8s service account token
TEKTON_RESULTS_TOKEN="${TEKTON_RESULTS_TOKEN:-}"
#
# Kubernetes namespace to fetch PipelineRuns from
TEKTON_NAMESPACE="${TEKTON_NAMESPACE:-}"
#
# Maximum number of records to fetch
TEKTON_LIMIT="${TEKTON_LIMIT:-100}"
#
# Path to K8s service account token (used if TEKTON_RESULTS_TOKEN is not set)
SA_TOKEN_PATH="${SA_TOKEN_PATH:-/var/run/secrets/kubernetes.io/serviceaccount/token}"
#
# === End of parameters ===

# get_token: Retrieve authentication token
# Priority:
#   1. TEKTON_RESULTS_TOKEN environment variable
#   2. Service account token mounted at SA_TOKEN_PATH
get_token() {
  if [[ -n "$TEKTON_RESULTS_TOKEN" ]]; then
    echo "$TEKTON_RESULTS_TOKEN"
    return 0
  fi

  if [[ -f "$SA_TOKEN_PATH" ]]; then
    cat "$SA_TOKEN_PATH"
    return 0
  fi

  echo "ERROR: No authentication token available." >&2
  echo "" >&2
  echo "For Kubernetes pods:" >&2
  echo "  Ensure service account token is mounted" >&2
  echo "" >&2
  echo "For local/CI execution:" >&2
  echo "  export TEKTON_RESULTS_TOKEN=\$(kubectl create token default -n default)" >&2
  return 1
}

# Validate required parameters
if [[ -z "$TEKTON_NAMESPACE" ]]; then
  echo "ERROR: TEKTON_NAMESPACE is required" >&2
  echo "Usage: TEKTON_NAMESPACE=<namespace> $0" >&2
  exit 1
fi

# Get authentication token
TOKEN=$(get_token) || exit 1

# Build tkn-results command
TKN_RESULTS_CMD=(
  tkn-results
  records
  list
  "${TEKTON_NAMESPACE}/results/-"
  --addr "$TEKTON_RESULTS_API_ADDR"
  --insecure
  -o json
  --limit "$TEKTON_LIMIT"
)

if [[ -n "$TOKEN" ]]; then
  TKN_RESULTS_CMD+=(--authtoken "$TOKEN")
fi

# Fetch records from Tekton Results and process with jq:
# - Filter for PipelineRun records only (data.type == "tekton.dev/v1.PipelineRun")
# - Base64 decode the payload (data.value)
# - Output one PipelineRun JSON per line
"${TKN_RESULTS_CMD[@]}" 2>&1 | jq -c '
  .records[]?
  | select(.data.type == "tekton.dev/v1.PipelineRun")
  | .data.value
  | @base64d
  | fromjson
'
