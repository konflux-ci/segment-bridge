#!/bin/bash
# fetch-release-records.sh
#   List AppStudio Releases cluster-wide and output each as one compact JSON
#   line to STDOUT (NDJSON-style, one record per line). Only releases created
#   within the last RELEASE_RECENT_HOURS are emitted (default 4 hours).
#   The script reads only from the cluster via kubectl/oc; no stdin.
#
#   This script is part of the Tekton/Konflux to Segment pipeline:
#   { ...; fetch-release-records.sh; } |
#   get-konflux-public-info.sh tekton-to-segment.sh | ...
#
#   Environment:
#     RELEASE_RECENT_HOURS  Time window in hours (default: 4). Only releases
#                           created within this window are output.
#     RELEASE_NOW_ISO       Optional. RFC3339 timestamp used as "now" for
#                           computing the window. Used by tests for
#                           deterministic filtering. If unset, system time
#                           is used.
#
#   If the Release API is not registered (CRD absent), kubectl/oc get fails
#   with messages that vary by client version or wording. We treat several
#   patterns as "API missing" (see is_release_api_missing_error), exit 0
#   with a WARNING on stderr, and print nothing to stdout so the pipeline is
#   not aborted. Errors that look like RBAC (forbidden/unauthorized) always
#   fail.
#
set -o pipefail -o errexit -o nounset

# is_release_api_missing_error: true when stderr suggests the Release kind
# is unknown / not served, not auth failure.
is_release_api_missing_error() {
	local err="$1"
	if grep -qiE 'forbidden|unauthorized' <<< "$err"; then
		return 1
	fi
	# "doesn't have a resource type … releases" and variants
	if grep -qiE 'doesn.t have a resource type|does not have a resource type|do not have a resource type' <<< "$err" &&
		grep -qiE 'releases|releases\.appstudio\.redhat\.com' <<< "$err"; then
		return 0
	fi
	# Fully-qualified resource name in "unknown / not found" style errors
	if grep -qiF 'releases.appstudio.redhat.com' <<< "$err" &&
		grep -qiE 'not[[:space:]]+found|could[[:space:]]+not[[:space:]]+find|unknown|unrecognized|not[[:space:]]+served|no[[:space:]]+matches' <<< "$err"; then
		return 0
	fi
	# Discovery / OpenShift-style phrasing (kind name near "no matches")
	if grep -qiE 'no matches for kind.{1,96}release' <<< "$err"; then
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

RELEASE_RECENT_HOURS="${RELEASE_RECENT_HOURS:-4}"
if [[ -n "${RELEASE_NOW_ISO:-}" ]]; then
	NOW="$RELEASE_NOW_ISO"
else
	NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
fi
CUTOFF=$(date -u -d "${NOW} - ${RELEASE_RECENT_HOURS} hours" +%Y-%m-%dT%H:%M:%SZ)

# Use the raw list API to process one bounded page at a time. kubectl's normal
# JSON printer assembles all pages before writing, which can exceed the pod's
# memory limit. Stage the filtered output so a failed list is never partial.
WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT
PAGE_FILE="$WORK_DIR/page.json"
OUTPUT_FILE="$WORK_DIR/releases.ndjson"
KUBE_ERR="$WORK_DIR/kubectl.err"
: > "$OUTPUT_FILE"
CONTINUE_TOKEN=""
PAGE_NUMBER=0

while true; do
	PAGE_NUMBER=$((PAGE_NUMBER + 1))
	URL='/apis/appstudio.redhat.com/v1alpha1/releases?limit=100'
	if [[ -n "$CONTINUE_TOKEN" ]]; then
		ENCODED_TOKEN=$(jq -nr --arg token "$CONTINUE_TOKEN" '$token | @uri')
		URL+="&continue=${ENCODED_TOKEN}"
	fi

	if ! "$KUBECTL" get --raw "$URL" > "$PAGE_FILE" 2> "$KUBE_ERR"; then
		err=$(cat "$KUBE_ERR")
		# A raw request to an unregistered resource returns a generic 404.
		# On later pages, any error means the snapshot is incomplete.
		if [[ "$PAGE_NUMBER" -eq 1 ]] && { is_release_api_missing_error "$err" ||
			grep -qF 'Error from server (NotFound): the server could not find the requested resource' <<< "$err"; }; then
			echo "WARNING: AppStudio Release API not registered; skipping release fetch" >&2
			exit 0
		fi
		echo "ERROR: $err" >&2
		exit 1
	fi
	if [[ -s "$KUBE_ERR" ]]; then
		echo "WARNING: $(cat "$KUBE_ERR")" >&2
	fi

	# Match kubectl get -o json, which removes managedFields by default.
	# Validate the list before accepting it so malformed pages cannot silently
	# turn into an apparently complete, empty result.
	if ! jq -e '(.items | type == "array") and ((.metadata.continue | type) as $t | $t == "string" or $t == "null")' "$PAGE_FILE" > /dev/null ||
		! jq -c --arg cutoff "$CUTOFF" '
			.items[]? | del(.metadata.managedFields) |
			.metadata.creationTimestamp as $created |
			select($created != null and ($created | fromdateiso8601) >= ($cutoff | fromdateiso8601)) |
			.
		' "$PAGE_FILE" >> "$OUTPUT_FILE"; then
		echo "ERROR: jq failed processing release list" >&2
		exit 1
	fi
	CONTINUE_TOKEN=$(jq -r '.metadata.continue // empty' "$PAGE_FILE")
	if [[ -z "$CONTINUE_TOKEN" ]]; then
		break
	fi
done

cat "$OUTPUT_FILE"
