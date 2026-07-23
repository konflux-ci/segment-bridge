#!/bin/bash
# fetch-tekton-records.sh
#   Fetch PipelineRun records from Tekton Results HTTP REST API.
#   Outputs decoded PipelineRun JSON objects to STDOUT (one per line).
#   Filters out TaskRuns - only PipelineRuns are returned.
#
#   Records are fetched in descending create_time order so that
#   TEKTON_LIMIT returns the N most recent records, not an arbitrary page.
#
#   Pagination follows next_page_token to catch up when more records exist
#   than fit in a single page. TEKTON_MAX_PAGES guards against runaway
#   pagination when the API keeps returning nextPageToken indefinitely.
#
#   This script is part of the Tekton Results bridge pipeline:
#   fetch-tekton-records.sh | tekton-to-segment.sh | segment-mass-uploader.sh
#
set -o pipefail -o errexit -o nounset

SELFDIR="$(cd "$(dirname "$0")" && pwd)"

# ======= Parameters ======
# The following variables can be set from outside the script by setting
# similarly named environment variables.
#
# Base URL of the Tekton Results HTTP REST API.
# Include the scheme (http:// or https://). If omitted, https:// is assumed.
# Examples: https://tekton-results-api:8443, http://localhost:8080
TEKTON_RESULTS_API_ADDR="${TEKTON_RESULTS_API_ADDR:-https://localhost:8443}"
#
# Authentication token for Tekton Results API
# If not set, will attempt to read from K8s service account token
TEKTON_RESULTS_TOKEN="${TEKTON_RESULTS_TOKEN:-}"
#
# Kubernetes namespace to fetch PipelineRuns from.
# Use "-" (the Tekton Results API wildcard) to query across all namespaces.
# Defaults to "-" (all namespaces) when unset.
TEKTON_NAMESPACE="${TEKTON_NAMESPACE:--}"
#
# Maximum number of records to fetch per page
TEKTON_LIMIT="${TEKTON_LIMIT:-100}"
#
# Maximum number of pages to fetch before stopping. Guards against runaway
# pagination when the API keeps returning nextPageToken indefinitely.
TEKTON_MAX_PAGES="${TEKTON_MAX_PAGES:-100}"
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

TOKEN=$(get_token) || exit 1

# Prepend https:// when the address has no scheme (backward compat).
API_BASE="$TEKTON_RESULTS_API_ADDR"
if [[ "$API_BASE" != http://* ]] && [[ "$API_BASE" != https://* ]]; then
  API_BASE="https://${API_BASE}"
fi

RECORDS_URL="${API_BASE}/apis/results.tekton.dev/v1alpha2/parents/${TEKTON_NAMESPACE}/results/-/records"

PAGE_TOKEN=""
PAGE_COUNT=0
HIT_MAX_PAGES=false

while true; do
  if [[ "$PAGE_COUNT" -ge "$TEKTON_MAX_PAGES" ]]; then
    echo "WARN fetch-tekton-records.sh: reached max page limit (${TEKTON_MAX_PAGES}); stopping pagination" >&2
    HIT_MAX_PAGES=true
    break
  fi

  PAGE_COUNT=$((PAGE_COUNT + 1))

  URL="${RECORDS_URL}?page_size=${TEKTON_LIMIT}&order_by=create_time%20desc"
  if [[ -n "$PAGE_TOKEN" ]]; then
    # Pagination tokens are opaque and frequently base64-encoded (may
    # contain +, /, =), which are not safe unescaped in a URL query string.
    PAGE_TOKEN_ENC=$(jq -rn --arg t "$PAGE_TOKEN" '$t|@uri')
    URL="${URL}&page_token=${PAGE_TOKEN_ENC}"
  fi

  if ! RESPONSE=$(curl -sSk --fail -H "Authorization: Bearer $TOKEN" "$URL"); then
    echo "ERROR fetch-tekton-records.sh: Tekton Results API request failed on page ${PAGE_COUNT}" >&2
    exit 1
  fi

  echo "$RESPONSE" \
    | jq -c -f "$SELFDIR/jq/filter-pipelineruns.jq"

  # Follow pagination via next_page_token.
  PAGE_TOKEN=$(echo "$RESPONSE" | jq -r '.nextPageToken // empty')
  if [[ -z "$PAGE_TOKEN" ]]; then
    break
  fi
done

if [[ "$PAGE_COUNT" -gt 1 ]] && [[ "$HIT_MAX_PAGES" != "true" ]]; then
  echo "WARN segment-bridge: paging to catch up — processed records across $PAGE_COUNT pages (limit=$TEKTON_LIMIT)" >&2
fi
