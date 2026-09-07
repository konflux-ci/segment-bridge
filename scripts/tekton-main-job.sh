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
#     fetch-application-records.sh - List AppStudio Applications (cluster-wide, time window)
#     fetch-release-records.sh - List AppStudio Releases (cluster-wide, time window)
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
#   Per-source toggles (default enabled). Only the literal value "false"
#   (case-insensitive) disables a source; any other value fails open.
#     FETCH_PIPELINERUNS   - fetch-tekton-records.sh
#     FETCH_OPERATOR       - fetch-konflux-op-records.sh
#     FETCH_NAMESPACES     - fetch-namespace-records.sh
#     FETCH_COMPONENTS     - fetch-component-records.sh
#     FETCH_APPLICATIONS   - fetch-application-records.sh
#     FETCH_RELEASES       - fetch-release-records.sh
#     EMIT_HEARTBEAT       - logged here; applied by tekton-to-segment.sh
#
set -o pipefail -o errexit -o nounset -o xtrace

# Add script file directory to PATH so we can use other scripts in the same
# directory
SELFDIR="$(dirname "$0")"
PATH="$SELFDIR:${PATH#"$SELFDIR":}"

# lib/ is not symlinked in tests; resolve the real script path to find it.
# shellcheck source-path=SCRIPTDIR
source "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/lib/toggle.sh"

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

_fetch_pipelineruns=$(resolve_toggle FETCH_PIPELINERUNS)
_fetch_operator=$(resolve_toggle FETCH_OPERATOR)
_fetch_namespaces=$(resolve_toggle FETCH_NAMESPACES)
_fetch_components=$(resolve_toggle FETCH_COMPONENTS)
_fetch_applications=$(resolve_toggle FETCH_APPLICATIONS)
_fetch_releases=$(resolve_toggle FETCH_RELEASES)
# Logged here for operators; enforced by tekton-to-segment.sh in the pipe below.
_emit_heartbeat=$(resolve_toggle EMIT_HEARTBEAT)

echo "Effective telemetry toggles: FETCH_PIPELINERUNS=${_fetch_pipelineruns} FETCH_OPERATOR=${_fetch_operator} FETCH_NAMESPACES=${_fetch_namespaces} FETCH_COMPONENTS=${_fetch_components} FETCH_APPLICATIONS=${_fetch_applications} FETCH_RELEASES=${_fetch_releases} EMIT_HEARTBEAT=${_emit_heartbeat}" >&2

# Fetch sources are best-effort: a failing data source must not prevent the
# remaining sources from running or abort the pipeline.  The brace group runs
# in a subshell (left side of a pipe) so `set +e` is scoped automatically.
{ set +e
  if [[ "${_fetch_pipelineruns}" == true ]]; then fetch-tekton-records.sh; fi
  if [[ "${_fetch_operator}" == true ]]; then fetch-konflux-op-records.sh; fi
  if [[ "${_fetch_namespaces}" == true ]]; then fetch-namespace-records.sh; fi
  if [[ "${_fetch_components}" == true ]]; then fetch-component-records.sh; fi
  if [[ "${_fetch_applications}" == true ]]; then fetch-application-records.sh; fi
  if [[ "${_fetch_releases}" == true ]]; then fetch-release-records.sh; fi
  true
} \
  | get-konflux-public-info.sh tekton-to-segment.sh \
  | segment_sink
