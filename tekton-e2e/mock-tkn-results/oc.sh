#!/bin/bash
# Mock oc/kubectl for e2e tests
# Responds to the specific commands used by the Tekton pipeline scripts

case "$*" in
  "get namespace kube-system -o jsonpath={.metadata.uid}")
    echo "${CLUSTER_ID:-test-cluster-default}"
    ;;
  "get configmap konflux-public-info -n konflux-info -o json")
    echo '{"data":{"info.json":"{\"konfluxVersion\":\"1.0.0\",\"kubernetesVersion\":\"1.28.0\"}"}}'
    ;;
  "get konfluxes.konflux.konflux-ci.dev/konflux -o json")
    cat <<'EOF'
{
  "apiVersion": "konflux.konflux-ci.dev/v1alpha1",
  "kind": "Konflux",
  "metadata": {
    "name": "konflux",
    "creationTimestamp": "2024-01-01T00:00:00Z"
  },
  "status": {
    "conditions": [
      {
        "type": "Ready",
        "status": "True",
        "lastTransitionTime": "2024-01-01T00:05:00Z",
        "reason": "AllComponentsReady"
      }
    ]
  }
}
EOF
    ;;
  "get ns -l konflux-ci.dev/type=tenant -o json")
    echo '{"items":[]}'
    ;;
  "get components.appstudio.redhat.com -A -o json")
    echo '{"items":[]}'
    ;;
  *)
    echo "mock oc: unexpected command: $*" >&2
    exit 1
    ;;
esac
