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

# Prefer kubectl over oc when both exist: kwok tests use a minimal kubeconfig that
# matches upstream kubectl; oc may not handle it the same way. Override with KUBECTL=name.
if [[ -n "${KUBECTL:-}" ]]; then
	if ! command -v "$KUBECTL" &>/dev/null; then
		echo "ERROR: KUBECTL=$KUBECTL not found in PATH" >&2
		exit 1
	fi
elif command -v kubectl &>/dev/null; then
	KUBECTL=kubectl
elif command -v oc &>/dev/null; then
	KUBECTL=oc
else
	echo "ERROR: oc or kubectl required but not found in PATH" >&2
	exit 1
fi

# Capture exit code without triggering errexit: assignment from failing command exits before RET is set.
set +e
# Use plural GVR so kubectl resolves the CRD reliably (singular alias can vary by version).
OUTPUT="$("$KUBECTL" get konfluxes.konflux.konflux-ci.dev/konflux -o json 2>&1)"
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
