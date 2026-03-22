#!/bin/bash
# tekton-to-segment.sh
#   Transform PipelineRun records from Tekton Results into anonymous Segment events.
#   NO PII is collected - only execution metrics.
#
#   For each PipelineRun, two events are emitted (retroactively):
#     1. "PipelineRun Started"   - timestamped at status.startTime
#     2. "PipelineRun Completed" - timestamped at status.completionTime
#
#   For each Namespace (Konflux tenant namespaces from fetch-namespace-records.sh),
#   one event is emitted:
#     "Namespace Created" - timestamped at metadata.creationTimestamp
#
#   For each Component (from fetch-component-records.sh or inline records), one event:
#     "Component Created" - timestamped at metadata.creationTimestamp
#
#   This script is part of the Tekton Results bridge pipeline:
#   ... | tekton-to-segment.sh | segment-mass-uploader.sh
#
#   Privacy: Namespace names are hashed with SHA256(namespace:cluster_id) to prevent
#   identification while still allowing correlation within a cluster.
#   Component and application names use SHA256(name:namespace:cluster_id) (12 hex chars).
#
set -o pipefail -o errexit -o nounset

# ======= Parameters ======
# The following variables can be set from outside the script by setting
# similarly named environment variables.
#
# Cluster ID used for namespace hashing (anonymization).
# On OpenShift: typically set to ClusterVersion UID
# On vanilla K8s: typically set to kube-system namespace UID
# The CronJob/Operator is responsible for setting this value.
CLUSTER_ID="${CLUSTER_ID:-anonymous}"
#
# Optional Konflux public info (e.g. from get-konflux-public-info.sh).
# When CLUSTER_ID is non-empty, clusterIdHash is added to properties and
# context.device.id is set to the same value. When KONFLUX_VERSION and/or
# KUBERNETES_VERSION are set, those are added to properties.
# get-konflux-public-info.sh may leave them unset when the configmap/namespace is
# missing or not accessible.
# KONFLUX_VERSION="${KONFLUX_VERSION:-}"
# KUBERNETES_VERSION="${KUBERNETES_VERSION:-}"
#
# === End of parameters ===

# hash_namespace: Compute SHA256 hash of namespace:cluster_id (first 12 chars)
# This provides anonymization while preserving correlation within a cluster.
# Arguments:
#   $1 - namespace name
# Output:
#   12-character hex hash to stdout
hash_namespace() {
  local ns="$1"
  echo -n "${ns}:${CLUSTER_ID}" | sha256sum | cut -c1-12
}

# hash_cluster_id: Compute SHA256 hash of CLUSTER_ID (first 12 chars).
# Used to add an anonymized cluster identifier to Segment events when Konflux info is present.
hash_cluster_id() {
  echo -n "$CLUSTER_ID" | sha256sum | cut -c1-12
}

# hash_component_identity: SHA256(componentName:namespace:cluster_id), first 12 hex chars.
hash_component_identity() {
  local name="$1"
  local ns="$2"
  echo -n "${name}:${ns}:${CLUSTER_ID}" | sha256sum | cut -c1-12
}

# hash_application_in_namespace: SHA256(application:namespace:cluster_id), first 12 hex chars.
hash_application_in_namespace() {
  local application="$1"
  local ns="$2"
  echo -n "${application}:${ns}:${CLUSTER_ID}" | sha256sum | cut -c1-12
}

# transform_konflux_record: Transform a single Konflux CR JSON into two Segment events
#   (Operator Deployment Started + Completed).
# Arguments:
#   $1 - Konflux CR JSON record
#   $2 - Pre-computed namespace hash
#   $3 - Pre-computed cluster ID hash (empty when Konflux info not added)
# Output:
#   Two Segment event JSON lines to stdout (one per event)
transform_konflux_record() {
  local record="$1"
  local ns_hash="$2"
  local cluster_id_hash="$3"

  echo "$record" | jq -c --arg ns_hash "$ns_hash" \
    --arg cluster_id_hash "$cluster_id_hash" \
    --arg konflux_version "${KONFLUX_VERSION:-}" \
    --arg kubernetes_version "${KUBERNETES_VERSION:-}" '
    # Ready condition (type=="Ready", status=="True")
    ((.status.conditions // []) | map(select(.type == "Ready" and .status == "True")) | .[0]) as $ready |

    (.metadata.creationTimestamp) as $startTime |
    ($ready.lastTransitionTime) as $completionTime |

    # Duration in seconds (null if timestamps missing)
    (
      if $startTime and $completionTime then
        (($completionTime | fromdateiso8601) - ($startTime | fromdateiso8601))
      else
        null
      end
    ) as $duration |

    ({
      type: "track",
      anonymousId: "anonymous",
      context: (
        {
          library: {
            name: "segment-bridge",
            version: "2.0.0"
          }
        } + (if $cluster_id_hash != "" then {device: {id: $cluster_id_hash}} else {} end)
      )
    }) as $base |

    (if $cluster_id_hash != "" then {clusterIdHash: $cluster_id_hash} else {} end) as $clusterProp |
    (if $konflux_version != "" then {konfluxVersion: $konflux_version} else {} end) as $konfluxProp |
    (if $kubernetes_version != "" then {kubernetesVersion: $kubernetes_version} else {} end) as $k8sProp |

    ({
      namespaceHash: $ns_hash
    } + $clusterProp + $konfluxProp + $k8sProp) as $commonProps |

    # Event 1: Operator Deployment Started
    ($base + {
      messageId: (.metadata.uid + "-started"),
      timestamp: $startTime,
      event: "Operator Deployment Started",
      properties: $commonProps
    }),

    # Event 2: Operator Deployment Completed
    ($base + {
      messageId: (.metadata.uid + "-completed"),
      timestamp: $completionTime,
      event: "Operator Deployment Completed",
      properties: ($commonProps + {
        startTime: $startTime,
        completionTime: $completionTime,
        durationSeconds: $duration,
        status: ($ready.reason // "Unknown")
      })
    })
  '
}

# transform_record: Transform a single PipelineRun JSON into two Segment events
#   (Started + Completed), both generated retroactively from completed data.
# Arguments:
#   $1 - PipelineRun JSON record
#   $2 - Pre-computed namespace hash
#   $3 - Pre-computed cluster ID hash (empty when Konflux info not added)
# Output:
#   Two Segment event JSON lines to stdout (one per event)
transform_record() {
  local record="$1"
  local ns_hash="$2"
  local cluster_id_hash="$3"

  echo "$record" | jq -c --arg ns_hash "$ns_hash" \
    --arg cluster_id_hash "$cluster_id_hash" \
    --arg konflux_version "${KONFLUX_VERSION:-}" \
    --arg kubernetes_version "${KUBERNETES_VERSION:-}" '
    # Extract completion status from conditions array
    ((.status.conditions // []) | map(select(.type == "Succeeded")) | .[0]) as $cond |

    # Calculate duration in seconds (null if timestamps missing)
    (
      if .status.completionTime and .status.startTime then
        ((.status.completionTime | fromdateiso8601) - (.status.startTime | fromdateiso8601))
      else
        null
      end
    ) as $duration |

    # Count child tasks/taskruns
    ((.status.childReferences // []) | length) as $taskCount |

    # Common base fields shared by both events
    ({
      type: "track",
      anonymousId: "anonymous",
      context: (
        {
          library: {
            name: "segment-bridge",
            version: "2.0.0"
          }
        } + (if $cluster_id_hash != "" then {device: {id: $cluster_id_hash}} else {} end)
      )
    }) as $base |

    # Optional Konflux public info (only when env vars set)
    (if $cluster_id_hash != "" then {clusterIdHash: $cluster_id_hash} else {} end) as $clusterProp |
    (if $konflux_version != "" then {konfluxVersion: $konflux_version} else {} end) as $konfluxProp |
    (if $kubernetes_version != "" then {kubernetesVersion: $kubernetes_version} else {} end) as $k8sProp |

    # Common properties shared by both events
    ({
      namespaceHash: $ns_hash,
      taskCount: $taskCount,
      hasPipelineLabel: (.metadata.labels["tekton.dev/pipeline"] != null),
      pipelineType: .metadata.labels["pipelines.appstudio.openshift.io/type"]
    } + $clusterProp + $konfluxProp + $k8sProp) as $commonProps |

    # Event 1: PipelineRun Started
    ($base + {
      messageId: (.metadata.uid + "-started"),
      timestamp: .status.startTime,
      event: "PipelineRun Started",
      properties: $commonProps
    }),

    # Event 2: PipelineRun Completed
    ($base + {
      messageId: (.metadata.uid + "-completed"),
      timestamp: .status.completionTime,
      event: "PipelineRun Completed",
      properties: ($commonProps + {
        startTime: .status.startTime,
        completionTime: .status.completionTime,
        durationSeconds: $duration,
        status: ($cond.reason // "Unknown")
      })
    })
  '
}

# transform_namespace_record: Transform a single Namespace JSON into one Segment event
#   (Namespace Created). Timestamp is metadata.creationTimestamp.
# Arguments:
#   $1 - Namespace JSON record
#   $2 - Pre-computed namespace hash (from metadata.name + CLUSTER_ID)
#   $3 - Pre-computed cluster ID hash (empty when Konflux info not added)
# Output:
#   One Segment event JSON line to stdout
transform_namespace_record() {
  local record="$1"
  local ns_hash="$2"
  local cluster_id_hash="$3"

  echo "$record" | jq -c --arg ns_hash "$ns_hash" \
    --arg cluster_id_hash "$cluster_id_hash" \
    --arg konflux_version "${KONFLUX_VERSION:-}" \
    --arg kubernetes_version "${KUBERNETES_VERSION:-}" '
    ({
      type: "track",
      anonymousId: "anonymous",
      context: (
        {
          library: {
            name: "segment-bridge",
            version: "2.0.0"
          }
        } + (if $cluster_id_hash != "" then {device: {id: $cluster_id_hash}} else {} end)
      )
    }) as $base |
    (if $cluster_id_hash != "" then {clusterIdHash: $cluster_id_hash} else {} end) as $clusterProp |
    (if $konflux_version != "" then {konfluxVersion: $konflux_version} else {} end) as $konfluxProp |
    (if $kubernetes_version != "" then {kubernetesVersion: $kubernetes_version} else {} end) as $k8sProp |
    ({
      namespaceHash: $ns_hash
    } + $clusterProp + $konfluxProp + $k8sProp) as $props |
    $base + {
      messageId: (.metadata.uid + "-namespace-created"),
      timestamp: .metadata.creationTimestamp,
      event: "Namespace Created",
      properties: $props
    }
  '
}

# transform_component_record: Transform a single Component JSON into one Segment event
#   (Component Created). Timestamp is metadata.creationTimestamp.
# Arguments:
#   $1 - Component JSON record
#   $2 - Pre-computed namespace hash (metadata.namespace + CLUSTER_ID)
#   $3 - Pre-computed component hash (name:namespace:CLUSTER_ID)
#   $4 - Pre-computed application hash (spec.application:namespace:CLUSTER_ID)
#   $5 - Pre-computed cluster ID hash (empty when Konflux info not added)
transform_component_record() {
  local record="$1"
  local ns_hash="$2"
  local component_hash="$3"
  local application_hash="$4"
  local cluster_id_hash="$5"

  echo "$record" | jq -c --arg ns_hash "$ns_hash" \
    --arg component_hash "$component_hash" \
    --arg application_hash "$application_hash" \
    --arg cluster_id_hash "$cluster_id_hash" \
    --arg konflux_version "${KONFLUX_VERSION:-}" \
    --arg kubernetes_version "${KUBERNETES_VERSION:-}" '
    ({
      type: "track",
      anonymousId: "anonymous",
      context: (
        {
          library: {
            name: "segment-bridge",
            version: "2.0.0"
          }
        } + (if $cluster_id_hash != "" then {device: {id: $cluster_id_hash}} else {} end)
      )
    }) as $base |
    (if $cluster_id_hash != "" then {clusterIdHash: $cluster_id_hash} else {} end) as $clusterProp |
    (if $konflux_version != "" then {konfluxVersion: $konflux_version} else {} end) as $konfluxProp |
    (if $kubernetes_version != "" then {kubernetesVersion: $kubernetes_version} else {} end) as $k8sProp |
    ({
      namespaceHash: $ns_hash,
      componentHash: $component_hash,
      applicationHash: $application_hash
    } + $clusterProp + $konfluxProp + $k8sProp) as $props |
    $base + {
      messageId: (.metadata.uid + "-component-created"),
      timestamp: .metadata.creationTimestamp,
      event: "Component Created",
      properties: $props
    }
  '
}

# Precompute cluster ID hash when Konflux info will be added (so we never send raw cluster ID)
cluster_id_hash=""
if [[ -n "${CLUSTER_ID:-}" ]]; then
  cluster_id_hash=$(hash_cluster_id)
fi

# Process each PipelineRun JSON record from stdin
# We use a while loop because jq doesn't have native SHA256, so we compute
# the namespace hash in bash for each record.
while IFS= read -r record; do
  # Skip empty lines
  [[ -z "$record" ]] && continue

  # Process only PipelineRun and Konflux resources
  kind=$(echo "$record" | jq -r '.kind // ""')
  if [[ "$kind" == "Konflux" ]]; then
    ns=$(echo "$record" | jq -r '.metadata.namespace // "unknown"')
    ns_hash=$(hash_namespace "$ns")
    transform_konflux_record "$record" "$ns_hash" "$cluster_id_hash"
    continue
  fi
  if [[ "$kind" == "Namespace" ]]; then
    # Hash by namespace name (same as PipelineRun namespace hashing)
    ns=$(echo "$record" | jq -r '.metadata.name // "unknown"')
    ns_hash=$(hash_namespace "$ns")
    transform_namespace_record "$record" "$ns_hash" "$cluster_id_hash"
    continue
  fi
  if [[ "$kind" == "Component" ]]; then
    ns=$(echo "$record" | jq -r '.metadata.namespace // "unknown"')
    comp_name=$(echo "$record" | jq -r '.metadata.name // "unknown"')
    application=$(echo "$record" | jq -r '.spec.application // ""')
    ns_hash=$(hash_namespace "$ns")
    component_hash=$(hash_component_identity "$comp_name" "$ns")
    application_hash=$(hash_application_in_namespace "$application" "$ns")
    transform_component_record "$record" "$ns_hash" "$component_hash" \
      "$application_hash" "$cluster_id_hash"
    continue
  fi
  [[ "$kind" != "PipelineRun" ]] && continue

  # Extract namespace and compute SHA256 hash
  ns=$(echo "$record" | jq -r '.metadata.namespace // "unknown"')
  ns_hash=$(hash_namespace "$ns")

  # Transform to Segment events (outputs two lines: Started + Completed)
  transform_record "$record" "$ns_hash" "$cluster_id_hash"
done
