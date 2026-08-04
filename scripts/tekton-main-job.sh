#!/bin/bash
# tekton-main-job.sh
#   Orchestrate the Tekton Results to Segment pipeline.
#   Combines scripts into a single pipeline:
#     Tekton Results API → Transform → Segment
#
#   This script is the entry point for the segment-bridge container when
#   processing Tekton PipelineRun data.
#
#   Pipeline flow:
#     fetch-tekton-records.sh   - Query Tekton Results API for PipelineRuns
#     fetch-konflux-op-records.sh - Fetch cluster Konflux CR (operator)
#     fetch-namespace-records.sh - List Konflux tenant namespaces (labeled)
#     fetch-component-records.sh - List AppStudio Components (cluster-wide, time window)
#     (fetch outputs concatenated) → get-konflux-public-info.sh → tekton-to-segment.sh
#     segment-mass-uploader.sh  - Batch and upload to Segment API
#
#   Authentication:
#     If SEGMENT_WRITE_KEY is set, a temporary .netrc file is generated and
#     passed to the upload scripts via CURL_NETRC. This keeps auth concerns
#     in the orchestration layer rather than in individual scripts.
#
#   When SEGMENT_WRITE_KEY is not set the fetch and transform stages still
#   run (useful for debugging) but segment_sink drains the output instead of
#   uploading, so the job exits 0 instead of crashing.
#
set -o pipefail -o errexit -o nounset -o xtrace

# Add script file directory to PATH so we can use other scripts in the same
# directory
SELFDIR="$(dirname "$0")"
PATH="$SELFDIR:${PATH#"$SELFDIR":}"

# Parse SEGMENT_BRIDGE_SKIP_SOURCES into a lookup-friendly string.
# Comma-separated logical names (whitespace around tokens is trimmed):
#   tekton-records, konflux-op-records, namespace-records, component-records
# When a name appears in the list, run_source skips that fetch script.
_skip_sources=","
if [[ -n "${SEGMENT_BRIDGE_SKIP_SOURCES:-}" ]]; then
  IFS=',' read -ra _skip_arr <<< "${SEGMENT_BRIDGE_SKIP_SOURCES}"
  for _entry in "${_skip_arr[@]}"; do
    _entry="${_entry#"${_entry%%[![:space:]]*}"}"
    _entry="${_entry%"${_entry##*[![:space:]]}"}"
    [[ -n "$_entry" ]] && _skip_sources="${_skip_sources}${_entry},"
  done
fi
run_source() {
  local name="$1"; shift
  if [[ "$_skip_sources" == *",$name,"* ]]; then
    echo "skipping $name (SEGMENT_BRIDGE_SKIP_SOURCES)" >&2
    return 0
  fi
  "$@"
}

# Generate a temporary .netrc file from SEGMENT_WRITE_KEY if provided.
# The segment-uploader.sh script uses CURL_NETRC for authentication, so we
# convert the write key into .netrc format here.
if [[ -n "${SEGMENT_WRITE_KEY:-}" ]]; then
  TMPNETRC=$(mktemp)
  trap 'rm -f "$TMPNETRC"' EXIT
  # Extract hostname from SEGMENT_BATCH_API for the .netrc machine field
  SEGMENT_HOST=$(echo "${SEGMENT_BATCH_API:-https://api.segment.io/v1/batch}" | sed -E 's|https?://([^/]+).*|\1|')
  # Segment uses HTTP Basic Auth: write key as login, empty password
  printf 'machine %s login %s password ""\n' "$SEGMENT_HOST" "$SEGMENT_WRITE_KEY" > "$TMPNETRC"
  chmod 600 "$TMPNETRC"
  export CURL_NETRC="$TMPNETRC"
  segment_sink() { segment-mass-uploader.sh; }
else
  echo "No SEGMENT_WRITE_KEY configured; skipping upload to Segment" >&2
  segment_sink() { cat > /dev/null; }
fi

# Fetch sources are best-effort: a failing data source must not prevent the
# remaining sources from running or abort the pipeline.  The brace group runs
# in a subshell (left side of a pipe) so `set +e` is scoped automatically.
{ set +e
  run_source tekton-records fetch-tekton-records.sh
  run_source konflux-op-records fetch-konflux-op-records.sh
  run_source namespace-records fetch-namespace-records.sh
  run_source component-records fetch-component-records.sh
  true
} \
  | get-konflux-public-info.sh tekton-to-segment.sh \
  | segment_sink
