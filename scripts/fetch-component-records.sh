#!/bin/bash
# fetch-component-records.sh
#   List AppStudio Components cluster-wide and output each as one compact JSON line
#   to STDOUT (NDJSON-style, one record per line). Only components created or updated
#   within the last COMPONENT_RECENT_HOURS are emitted (default 4 hours). The script
#   reads only from the cluster via kubectl/oc; no stdin.
#
#   This script is part of the Tekton/Konflux to Segment pipeline:
#   { ...; fetch-namespace-records.sh; fetch-component-records.sh; } |
#   get-konflux-public-info.sh tekton-to-segment.sh | ...
#
#   Environment:
#     COMPONENT_RECENT_HOURS  Time window in hours (default: 4). Only components
#                             whose effective timestamp (creation or last update)
#                             is within this window are output.
#     COMPONENT_NOW_ISO       Optional. RFC3339 timestamp used as "now" for
#                             computing the window. Used by tests for
#                             deterministic filtering. If unset, system time is used.
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

COMPONENT_RECENT_HOURS="${COMPONENT_RECENT_HOURS:-4}"
if [[ -n "${COMPONENT_NOW_ISO:-}" ]]; then
	NOW="$COMPONENT_NOW_ISO"
else
	NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
fi
CUTOFF=$(date -u -d "${NOW} - ${COMPONENT_RECENT_HOURS} hours" +%Y-%m-%dT%H:%M:%SZ)

KUBE_ERR=$(mktemp)
trap 'rm -f "$KUBE_ERR"' EXIT
set +e
"$KUBECTL" get components.appstudio.redhat.com -A -o json 2>"$KUBE_ERR" | jq -c --arg cutoff "$CUTOFF" '
  .items[]? |
  (([.metadata.creationTimestamp] + [.metadata.managedFields[]?.time // empty] | map(select(. != null)) | max) // .metadata.creationTimestamp) as $eff |
  select($eff != null and ($eff | fromdateiso8601) >= ($cutoff | fromdateiso8601)) |
  .
'
ret_kubectl=${PIPESTATUS[0]} ret_jq=${PIPESTATUS[1]}
set -e
if [[ $ret_kubectl -ne 0 ]]; then
	echo "ERROR: $(cat "$KUBE_ERR")" >&2
	exit 1
fi
if [[ $ret_jq -ne 0 ]]; then
	echo "ERROR: jq failed processing component list" >&2
	exit 1
fi
if [[ -s "$KUBE_ERR" ]]; then
	echo "WARNING: $(cat "$KUBE_ERR")" >&2
fi
