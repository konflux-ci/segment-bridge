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

# Capture stdout and stderr separately so JSON is not polluted by kubectl warnings
KUBE_ERR=$(mktemp)
trap 'rm -f "$KUBE_ERR"' EXIT
set +e
OUTPUT="$("$KUBECTL" get ns -l "$LABEL_SELECTOR" -o json 2>"$KUBE_ERR")"
RET=$?
set -e
if [[ $RET -ne 0 ]]; then
	echo "ERROR: $(cat "$KUBE_ERR")" >&2
	exit 1
fi
if [[ -z "$OUTPUT" ]]; then
	echo "ERROR: Failed to list namespaces with label $LABEL_SELECTOR" >&2
	exit 1
fi

# stdin to jq avoids shellcheck SC2002 (echo | pipe)
jq -c '.items[]? | .' <<< "$OUTPUT"
