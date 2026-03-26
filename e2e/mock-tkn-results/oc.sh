#!/bin/bash
# Mock oc/kubectl for e2e tests
# Responds to the specific commands used by the Tekton pipeline scripts

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

case "$*" in
  *"get namespace kube-system"*"-o jsonpath={.metadata.uid}")
    # Return kube-system UID for cluster ID
    if [[ -f "$DIR/kube-system-uid" ]]; then
      cat "$DIR/kube-system-uid"
    else
      # Default to CLUSTER_ID env var
      printf '%s' "${CLUSTER_ID:-}"
    fi
    ;;
    
  *"get configmap konflux-public-info"*"-n konflux-info"*"-o json")
    # Return Konflux public info configmap
    if [[ -f "$DIR/configmap-konflux-public-info.json" ]]; then
      cat "$DIR/configmap-konflux-public-info.json"
    else
      echo '{"data":{"info.json":"{\"konfluxVersion\":\"test\",\"kubernetesVersion\":\"test\"}"}}'
    fi
    ;;
    
  *"get"*"konfluxes.konflux.konflux-ci.dev/konflux"*"-o json" | *"get konfluxes"*"-o json")
    # Return Konflux CR (operator deployment)
    if [[ -f "$DIR/FAIL_KONFLUX" ]]; then
      echo "mock oc: simulated konflux operator fetch failure" >&2
      exit 1
    fi
    if [[ -f "$DIR/konflux-cr.json" ]]; then
      cat "$DIR/konflux-cr.json"
    else
      echo "{}"
    fi
    ;;
    
  *"get ns"*"-l konflux-ci.dev/type=tenant"*"-o json")
    # Return tenant namespace list for KPI events
    if [[ -f "$DIR/namespaces.json" ]]; then
      cat "$DIR/namespaces.json"
    else
      echo '{"items":[]}'
    fi
    ;;
    
  *"get components.appstudio.redhat.com"*"-A"*"-o json")
    # Return component list for KPI events
    if [[ -f "$DIR/components.json" ]]; then
      cat "$DIR/components.json"
    else
      echo '{"items":[]}'
    fi
    ;;
    
  *)
    echo "mock oc: unexpected command: $*" >&2
    exit 1
    ;;
esac
