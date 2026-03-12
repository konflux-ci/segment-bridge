#!/bin/bash
# fetch-namespace-records.sh
#   List Konflux tenant namespaces (label konflux-ci.dev/type=tenant) and output
#   each as one compact JSON line to STDOUT (NDJSON-style, one record per line).
#
#   This script is part of the Tekton/Konflux to Segment pipeline:
#   { ...; fetch-namespace-records.sh; } | get-konflux-public-info.sh tekton-to-segment.sh | ...
#
#   Label selector is hardcoded; no env var (tenant + managed-tenant namespaces
#   both carry konflux-ci.dev/type=tenant).
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

# Hardcoded selector: only Konflux tenant namespaces
LABEL_SELECTOR='konflux-ci.dev/type=tenant'

# Stream kubectl stdout into jq; stderr kept separate for clean NDJSON and for warnings.
KUBE_ERR=$(mktemp)
trap 'rm -f "$KUBE_ERR"' EXIT
# Capture both PIPESTATUS values in one assignment (each command resets PIPESTATUS)
set +e
"$KUBECTL" get ns -l "$LABEL_SELECTOR" -o json 2>"$KUBE_ERR" | jq -c '.items[]? | .'
ret_kubectl=${PIPESTATUS[0]} ret_jq=${PIPESTATUS[1]}
set -e
if [[ $ret_kubectl -ne 0 ]]; then
	echo "ERROR: $(cat "$KUBE_ERR")" >&2
	exit 1
fi
if [[ $ret_jq -ne 0 ]]; then
	echo "ERROR: jq failed processing namespace list" >&2
	exit 1
fi
# Surface kubectl/oc warnings to stderr when command succeeded (no pollution of stdout)
if [[ -s "$KUBE_ERR" ]]; then
	echo "WARNING: $(cat "$KUBE_ERR")" >&2
fi
