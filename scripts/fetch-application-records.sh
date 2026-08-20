#!/bin/bash
# fetch-application-records.sh
#   List AppStudio Applications cluster-wide and output each as one compact JSON
#   line to STDOUT (NDJSON-style, one record per line). Only applications created
#   or updated within the last APPLICATION_RECENT_HOURS are emitted (default 4
#   hours). The script reads only from the cluster via kubectl/oc; no stdin.
#
#   This script is part of the Tekton/Konflux to Segment pipeline:
#   { ...; fetch-application-records.sh; } |
#   get-konflux-public-info.sh tekton-to-segment.sh | ...
#
#   Environment:
#     APPLICATION_RECENT_HOURS  Time window in hours (default: 4). Only
#                               applications whose effective timestamp (creation
#                               or last update) is within this window are output.
#     APPLICATION_NOW_ISO       Optional. RFC3339 timestamp used as "now" for
#                               computing the window. Used by tests for
#                               deterministic filtering. If unset, system time
#                               is used.
#
#   If the Application API is not registered (CRD absent), kubectl/oc get fails
#   with messages that vary by client version or wording. We treat several
#   patterns as "API missing" (see is_application_api_missing_error), exit 0
#   with a WARNING on stderr, and print nothing to stdout so the pipeline is not
#   aborted. Errors that look like RBAC (forbidden/unauthorized) always fail.
#
set -o pipefail -o errexit -o nounset

# is_application_api_missing_error: true when stderr suggests the Application
# kind is unknown / not served, not auth failure.
is_application_api_missing_error() {
	local err="$1"
	if grep -qiE 'forbidden|unauthorized' <<< "$err"; then
		return 1
	fi
	# "doesn't have a resource type … applications" and variants
	if grep -qiE 'doesn.t have a resource type|does not have a resource type|do not have a resource type' <<< "$err" &&
		grep -qiE 'applications|applications\.appstudio\.redhat\.com' <<< "$err"; then
		return 0
	fi
	# Fully-qualified resource name in "unknown / not found" style errors
	if grep -qiF 'applications.appstudio.redhat.com' <<< "$err" &&
		grep -qiE 'not[[:space:]]+found|could[[:space:]]+not[[:space:]]+find|unknown|unrecognized|not[[:space:]]+served|no[[:space:]]+matches' <<< "$err"; then
		return 0
	fi
	# Discovery / OpenShift-style phrasing (kind name near "no matches")
	if grep -qiE 'no matches for kind.{1,96}application' <<< "$err"; then
		return 0
	fi
	return 1
}

# Prefer kubectl over oc when both exist. Override with KUBECTL=name.
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

APPLICATION_RECENT_HOURS="${APPLICATION_RECENT_HOURS:-4}"
if [[ -n "${APPLICATION_NOW_ISO:-}" ]]; then
	NOW="$APPLICATION_NOW_ISO"
else
	NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
fi
CUTOFF=$(date -u -d "${NOW} - ${APPLICATION_RECENT_HOURS} hours" +%Y-%m-%dT%H:%M:%SZ)

KUBE_ERR=$(mktemp)
trap 'rm -f "$KUBE_ERR"' EXIT
set +e
"$KUBECTL" get applications.appstudio.redhat.com -A -o json 2>"$KUBE_ERR" | jq -c --arg cutoff "$CUTOFF" '
  .items[]? |
  (([.metadata.creationTimestamp] + [.metadata.managedFields[]?.time // empty] | map(select(. != null)) | max) // .metadata.creationTimestamp) as $eff |
  select($eff != null and ($eff | fromdateiso8601) >= ($cutoff | fromdateiso8601)) |
  .
'
ret_kubectl=${PIPESTATUS[0]} ret_jq=${PIPESTATUS[1]}
set -e
if [[ $ret_kubectl -ne 0 ]]; then
	err=$(cat "$KUBE_ERR")
	if is_application_api_missing_error "$err"; then
		echo "WARNING: AppStudio Application API not registered; skipping application fetch" >&2
		exit 0
	fi
	echo "ERROR: $err" >&2
	exit 1
fi
if [[ $ret_jq -ne 0 ]]; then
	echo "ERROR: jq failed processing application list" >&2
	exit 1
fi
if [[ -s "$KUBE_ERR" ]]; then
	echo "WARNING: $(cat "$KUBE_ERR")" >&2
fi
