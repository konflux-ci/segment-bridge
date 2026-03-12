#!/bin/bash
# fetch-konflux-op-records.sh
#   Fetch the cluster-scoped Konflux CR named "konflux" from the cluster.
#   Outputs one compact JSON line to STDOUT (NDJSON-style, one record per line).
#
#   This script is part of the Tekton/Konflux to Segment pipeline:
#   { fetch-tekton-records.sh; fetch-konflux-op-records.sh; } | tekton-to-segment.sh | segment-mass-uploader.sh
#
#   "op" = operator (Konflux operator CR).
#
set -o pipefail -o errexit -o nounset

KUBECTL=""
if command -v oc &>/dev/null; then
	KUBECTL=oc
elif command -v kubectl &>/dev/null; then
	KUBECTL=kubectl
else
	echo "ERROR: oc or kubectl required but not found in PATH" >&2
	exit 1
fi

# Capture exit code without triggering errexit: assignment from failing command exits before RET is set.
set +e
OUTPUT="$("$KUBECTL" get konflux konflux -o json 2>&1)"
RET=$?
set -e
if [[ $RET -ne 0 ]]; then
	if echo "$OUTPUT" | grep -q "Error from server (NotFound)"; then
		echo "ERROR: Konflux resource 'konflux' not found" >&2
	else
		echo "ERROR: $OUTPUT" >&2
	fi
	exit 1
fi
if [[ -z "$OUTPUT" ]]; then
	echo "ERROR: Failed to get Konflux resource 'konflux'" >&2
	exit 1
fi

echo "$OUTPUT" | jq -c '.'
