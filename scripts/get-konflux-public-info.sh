#!/bin/bash
# get-konflux-public-info.sh
#   Wrapper that sets CLUSTER_ID, KONFLUX_VERSION, KUBERNETES_VERSION from cluster
#   resources and runs the given command with those env vars.
#   Requires oc or kubectl and jq on PATH, and KUBECONFIG (or default) set.
#
set -o pipefail -o errexit -o nounset

if [[ $# -eq 0 ]]; then
	echo "usage: $0 <command> [args...]" >&2
	exit 1
fi

KUBECTL=""
if command -v oc &>/dev/null; then
	KUBECTL=oc
elif command -v kubectl &>/dev/null; then
	KUBECTL=kubectl
else
	echo "get-konflux-public-info.sh: need oc or kubectl in PATH" >&2
	exit 1
fi

CLUSTER_ID="$($KUBECTL get namespace kube-system -o jsonpath='{.metadata.uid}')"
export CLUSTER_ID

INFO_JSON="$($KUBECTL get configmap konflux-public-info -n konflux-info -o json | jq -r '.data["info.json"]')"
KONFLUX_VERSION="$(printf '%s' "$INFO_JSON" | jq -r '.konfluxVersion')"
KUBERNETES_VERSION="$(printf '%s' "$INFO_JSON" | jq -r '.kubernetesVersion')"
export KONFLUX_VERSION KUBERNETES_VERSION

exec "$@"
