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
#     tekton-to-segment.sh      - Transform to anonymous Segment events
#     segment-mass-uploader.sh  - Batch and upload to Segment API
#
#   Authentication:
#     If SEGMENT_WRITE_KEY is set, a temporary .netrc file is generated and
#     passed to the upload scripts via CURL_NETRC. This keeps auth concerns
#     in the orchestration layer rather than in individual scripts.
#
set -o pipefail -o errexit -o nounset -o xtrace

# Add script file directory to PATH so we can use other scripts in the same
# directory
SELFDIR="$(dirname "$0")"
PATH="$SELFDIR:${PATH#"$SELFDIR":}"

# Generate a temporary .netrc file from SEGMENT_WRITE_KEY if provided.
# The segment-uploader.sh script uses CURL_NETRC for authentication, so we
# convert the write key into .netrc format here.
if [[ -n "${SEGMENT_WRITE_KEY:-}" ]]; then
  TMPNETRC=$(mktemp)
  trap 'rm -f "$TMPNETRC"' EXIT
  # Extract hostname from SEGMENT_BATCH_API for the .netrc machine field
  SEGMENT_HOST=$(echo "${SEGMENT_BATCH_API:-https://api.segment.io/v1/batch}" \
    | sed -E 's|https?://([^/]+).*|\1|')
  # Segment uses HTTP Basic Auth: write key as login, empty password
  printf 'machine %s login %s password ""\n' "$SEGMENT_HOST" "$SEGMENT_WRITE_KEY" > "$TMPNETRC"
  chmod 600 "$TMPNETRC"
  export CURL_NETRC="$TMPNETRC"
fi

fetch-tekton-records.sh | tekton-to-segment.sh | segment-mass-uploader.sh
