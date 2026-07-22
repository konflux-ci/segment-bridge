#!/bin/bash
# fetch-tekton-records.sh
#   Fetch PipelineRun records from Tekton Results HTTP REST API.
#   Outputs decoded PipelineRun JSON objects to STDOUT (one per line).
#   Filters out TaskRuns - only PipelineRuns are returned.
#
#   Records are fetched in descending create_time order so that
#   TEKTON_LIMIT returns the N most recent records, not an arbitrary page.
#
#   When a cursor is available (via TEKTON_CURSOR env var or ConfigMap),
#   only records newer than the cursor are emitted. The cursor persisted to
#   the ConfigMap is max(create_time) minus 1 second (see write_cursor
#   below) to create a deliberate small overlap window that protects against
#   a createTime tie-break race; overlapping records are safely reprocessed
#   since Segment dedups via messageId. Pagination follows next_page_token
#   to catch up when more records exist than fit in a single page.
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
# Override cursor value directly (skips ConfigMap read).
# Set to a create_time timestamp (e.g. "2024-01-01T12:00:00Z") to skip
# records already processed. When unset, read from ConfigMap if available.
TEKTON_CURSOR="${TEKTON_CURSOR:-}"
#
# ConfigMap name where the cursor is persisted between CronJob runs.
TEKTON_CURSOR_CONFIGMAP="${TEKTON_CURSOR_CONFIGMAP:-segment-bridge-cursor}"
#
# Namespace of the cursor ConfigMap.
TEKTON_CURSOR_NAMESPACE="${TEKTON_CURSOR_NAMESPACE:-segment-bridge}"
#
# === End of parameters ===

# Detect kubectl/oc for cursor ConfigMap access (optional).
# Cursor persistence is best-effort: scripts work without kubectl.
# Set KUBECTL="" explicitly to disable auto-detection.
if [[ -z "${KUBECTL+x}" ]]; then
  if command -v kubectl &>/dev/null; then
    KUBECTL=kubectl
  elif command -v oc &>/dev/null; then
    KUBECTL=oc
  else
    KUBECTL=""
  fi
fi
KUBECTL="${KUBECTL:-}"

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

# read_cursor: return the cursor timestamp (empty string if unavailable).
read_cursor() {
  if [[ -n "$TEKTON_CURSOR" ]]; then
    echo "$TEKTON_CURSOR"
    return 0
  fi
  if [[ -n "$KUBECTL" ]]; then
    local cursor
    if cursor=$($KUBECTL get configmap "$TEKTON_CURSOR_CONFIGMAP" \
      -n "$TEKTON_CURSOR_NAMESPACE" \
      -o jsonpath='{.data.last_processed_create_time}' 2>/dev/null); then
      echo "$cursor"
    else
      echo "fetch-tekton-records.sh: could not read cursor ConfigMap (cold start assumed)" >&2
      echo ""
    fi
    return 0
  fi
  echo ""
}

# write_cursor: persist the new cursor to the ConfigMap (best-effort).
#
# The persisted cursor is backed off by 1 second from the true observed
# max(createTime) to guard against a tie-break race: if two PipelineRuns
# share the exact same createTime and one of them is not yet visible to the
# Tekton Results API when this run executes, a strict cursor at the true max
# would permanently exclude that record once it later appears (the jq filter
# only keeps createTime > cursor). Backing off by 1 second creates a
# deliberate small overlap window on the next run — any records reprocessed
# in that window are re-sent to Segment, which already dedups via
# messageId, so the only cost is a bit of extra bandwidth.
write_cursor() {
  local new_cursor="$1"
  if [[ -z "$new_cursor" ]] || [[ -z "$KUBECTL" ]]; then
    return 0
  fi
  # Falls back to the un-backed-off cursor if `date` can't compute the
  # offset. Prefer GNU date (-d), then BSD date (-j/-v; macOS). Cursor
  # persistence is best-effort and must never abort the whole script.
  local backed_off_cursor
  if ! backed_off_cursor=$(date -u -d "${new_cursor} - 1 second" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null) &&
     ! backed_off_cursor=$(date -u -j -f "%Y-%m-%dT%H:%M:%SZ" -v-1S "$new_cursor" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null); then
    echo "fetch-tekton-records.sh: could not compute cursor overlap offset (date unsupported); using exact cursor" >&2
    backed_off_cursor="$new_cursor"
  fi
  if ! $KUBECTL create configmap "$TEKTON_CURSOR_CONFIGMAP" \
    -n "$TEKTON_CURSOR_NAMESPACE" \
    --from-literal=last_processed_create_time="$backed_off_cursor" \
    --dry-run=client -o yaml \
    | $KUBECTL label --local -f - app.kubernetes.io/name=segment-bridge -o yaml \
    | $KUBECTL apply -f - 2>/dev/null; then
    echo "fetch-tekton-records.sh: could not persist cursor ConfigMap (will retry next run)" >&2
  fi
}

TOKEN=$(get_token) || exit 1

# Prepend https:// when the address has no scheme (backward compat).
API_BASE="$TEKTON_RESULTS_API_ADDR"
if [[ "$API_BASE" != http://* ]] && [[ "$API_BASE" != https://* ]]; then
  API_BASE="https://${API_BASE}"
fi

RECORDS_URL="${API_BASE}/apis/results.tekton.dev/v1alpha2/parents/${TEKTON_NAMESPACE}/results/-/records"

CURSOR=$(read_cursor)
PAGE_TOKEN=""
MAX_CREATE_TIME=""
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
    # Records already emitted to stdout may be re-fetched on the next run
    # because write_cursor is intentionally skipped; Segment dedups via messageId.
    exit 1
  fi

  # Track max createTime for cursor advancement.
  PAGE_MAX=$(echo "$RESPONSE" \
    | jq -r '[.records[]?.createTime // empty] | max // empty')
  if [[ -n "$PAGE_MAX" ]]; then
    if [[ -z "$MAX_CREATE_TIME" ]] || [[ "$PAGE_MAX" > "$MAX_CREATE_TIME" ]]; then
      MAX_CREATE_TIME="$PAGE_MAX"
    fi
  fi

  # Output PipelineRuns, filtering by cursor when set.
  echo "$RESPONSE" \
    | jq -c --arg cursor "${CURSOR}" -f "$SELFDIR/jq/filter-pipelineruns.jq"

  # When cursor is set, stop once we reach already-processed records.
  if [[ -n "$CURSOR" ]]; then
    HIT_CURSOR=$(echo "$RESPONSE" | jq --arg cursor "$CURSOR" \
      'any(.records[]?; .createTime != null and .createTime <= $cursor)')
    if [[ "$HIT_CURSOR" == "true" ]]; then
      break
    fi
  fi

  # Follow pagination via next_page_token.
  PAGE_TOKEN=$(echo "$RESPONSE" | jq -r '.nextPageToken // empty')
  if [[ -z "$PAGE_TOKEN" ]]; then
    break
  fi
done

if [[ "$PAGE_COUNT" -gt 1 ]] && [[ "$HIT_MAX_PAGES" != "true" ]]; then
  echo "WARN segment-bridge: paging to catch up — processed records across $PAGE_COUNT pages (limit=$TEKTON_LIMIT)" >&2
fi

write_cursor "$MAX_CREATE_TIME"
