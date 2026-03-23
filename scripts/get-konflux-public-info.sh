#!/bin/bash
# get-konflux-public-info.sh
#   Wrapper that sets CLUSTER_ID, KONFLUX_VERSION, KUBERNETES_VERSION from cluster
#   resources and runs the given command with those env vars.
#   Requires oc or kubectl and jq on PATH, and KUBECONFIG (or default) set.
#   When both oc and kubectl exist, kubectl is preferred (kwok tests). Set KUBECTL to override.
#
set -o pipefail -o errexit -o nounset

if [[ $# -eq 0 ]]; then
	echo "usage: $0 <command> [args...]" >&2
	exit 1
fi

# Prefer kubectl over oc when both exist (see fetch-konflux-op-records.sh). Override with KUBECTL=name.
if [[ -n "${KUBECTL:-}" ]]; then
	if ! command -v "$KUBECTL" &>/dev/null; then
		echo "get-konflux-public-info.sh: KUBECTL=$KUBECTL not found in PATH" >&2
		exit 1
	fi
elif command -v kubectl &>/dev/null; then
	KUBECTL=kubectl
elif command -v oc &>/dev/null; then
	KUBECTL=oc
else
	echo "get-konflux-public-info.sh: need oc or kubectl in PATH" >&2
	exit 1
fi

# Optional: CLUSTER_ID from kube-system (do not override if already set).
if [[ -z "${CLUSTER_ID:-}" ]]; then
	set +e
	CLUSTER_ID="$($KUBECTL get namespace kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null)"
	_ret=$?
	set -e
	if [[ $_ret -ne 0 || -z "${CLUSTER_ID:-}" ]]; then
		echo "get-konflux-public-info.sh: could not read kube-system UID (CLUSTER_ID unset)" >&2
	else
		export CLUSTER_ID
	fi
fi

# RBAC: get on configmap/konflux-public-info in namespace konflux-info (granted by Konflux operator).
# Optional: KONFLUX_VERSION and KUBERNETES_VERSION from konflux-public-info ConfigMap.
set +e
INFO_JSON="$($KUBECTL get configmap konflux-public-info -n konflux-info -o json 2>/dev/null | jq -r '.data["info.json"]' 2>/dev/null)"
_ret=$?
set -e
if [[ $_ret -eq 0 && -n "${INFO_JSON:-}" ]]; then
	set +e
	KONFLUX_VERSION="$(printf '%s' "$INFO_JSON" | jq -r '.konfluxVersion' 2>/dev/null)"
	_jq_ret=$?
	if ! KUBERNETES_VERSION="$(printf '%s' "$INFO_JSON" | jq -r '.kubernetesVersion' 2>/dev/null)"; then
		_jq_ret=1
	fi
	set -e
	if [[ $_jq_ret -eq 0 ]]; then
		export KONFLUX_VERSION KUBERNETES_VERSION
	else
		echo "get-konflux-public-info.sh: could not read konflux-public-info (KONFLUX_VERSION/KUBERNETES_VERSION unset)" >&2
	fi
else
	echo "get-konflux-public-info.sh: could not read konflux-public-info (KONFLUX_VERSION/KUBERNETES_VERSION unset)" >&2
fi

exec "$@"
