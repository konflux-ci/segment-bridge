#!/bin/bash
# fetch-namespace-records.sh
#   List Konflux tenant namespaces (label konflux-ci.dev/type=tenant) and output
#   each as one compact JSON line to STDOUT (NDJSON-style, one record per line).
#   Only namespaces created or updated within the last NAMESPACE_RECENT_HOURS are
#   emitted (default 4 hours). The script reads only from the cluster via kubectl;
#   no stdin.
#
#   This script is part of the Tekton/Konflux to Segment pipeline:
#   { ...; fetch-namespace-records.sh; } | get-konflux-public-info.sh tekton-to-segment.sh | ...
#
#   Label selector is hardcoded; no env var (tenant + managed-tenant namespaces
#   both carry konflux-ci.dev/type=tenant).
#
#   Environment:
#     NAMESPACE_RECENT_HOURS  Time window in hours (default: 4). Only namespaces
#                             whose effective timestamp (creation or last update)
#                             is within this window are output.
#     NAMESPACE_NOW_ISO       Optional. RFC3339 timestamp used as "now" for
#                             computing the window. Used by tests for
#                             deterministic filtering. If unset, system time is used.
#
set -o pipefail -o errexit -o nounset

# Prefer kubectl over oc when both exist (see fetch-konflux-op-records.sh). Override with KUBECTL=name.
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

# Hardcoded selector: only Konflux tenant namespaces
LABEL_SELECTOR='konflux-ci.dev/type=tenant'

# Time window: only emit namespaces created/updated within this many hours.
NAMESPACE_RECENT_HOURS="${NAMESPACE_RECENT_HOURS:-4}"
if [[ -n "${NAMESPACE_NOW_ISO:-}" ]]; then
	NOW="$NAMESPACE_NOW_ISO"
else
	NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
fi
CUTOFF=$(date -u -d "${NOW} - ${NAMESPACE_RECENT_HOURS} hours" +%Y-%m-%dT%H:%M:%SZ)

# Stream kubectl stdout into jq; stderr kept separate for clean NDJSON and for warnings.
# Filter: effective time = max(creationTimestamp, managedFields[].time); keep if >= CUTOFF.
KUBE_ERR=$(mktemp)
trap 'rm -f "$KUBE_ERR"' EXIT
# Capture both PIPESTATUS values in one assignment (each command resets PIPESTATUS)
set +e
"$KUBECTL" get ns -l "$LABEL_SELECTOR" -o json 2>"$KUBE_ERR" | jq -c --arg cutoff "$CUTOFF" '
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
	echo "ERROR: jq failed processing namespace list" >&2
	exit 1
fi
# Surface kubectl/oc warnings to stderr when command succeeded (no pollution of stdout)
if [[ -s "$KUBE_ERR" ]]; then
	echo "WARNING: $(cat "$KUBE_ERR")" >&2
fi
