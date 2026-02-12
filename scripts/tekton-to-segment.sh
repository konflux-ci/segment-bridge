#!/bin/bash
# tekton-to-segment.sh
#   Transform PipelineRun records from Tekton Results into anonymous Segment events.
#   NO PII is collected - only execution metrics.
#
#   For each PipelineRun, two events are emitted (retroactively):
#     1. "PipelineRun Started"   - timestamped at status.startTime
#     2. "PipelineRun Completed" - timestamped at status.completionTime
#
#   This script is part of the Tekton Results bridge pipeline:
#   fetch-tekton-records.sh | tekton-to-segment.sh | segment-mass-uploader.sh
#
#   Privacy: Namespace names are hashed with SHA256(namespace:cluster_id) to prevent
#   identification while still allowing correlation within a cluster.
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

# transform_record: Transform a single PipelineRun JSON into two Segment events
#   (Started + Completed), both generated retroactively from completed data.
# Arguments:
#   $1 - PipelineRun JSON record
#   $2 - Pre-computed namespace hash
# Output:
#   Two Segment event JSON lines to stdout (one per event)
transform_record() {
  local record="$1"
  local ns_hash="$2"

  echo "$record" | jq -c --arg ns_hash "$ns_hash" '
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
    {
      type: "track",
      anonymousId: "anonymous",
      context: {
        library: {
          name: "segment-bridge",
          version: "2.0.0"
        }
      }
    } as $base |

    # Common properties shared by both events
    {
      namespaceHash: $ns_hash,
      taskCount: $taskCount,
      hasPipelineLabel: (.metadata.labels["tekton.dev/pipeline"] != null),
      isKonfluxBuild: (.metadata.labels["appstudio.openshift.io/application"] != null)
    } as $commonProps |

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
        endTime: .status.completionTime,
        durationSeconds: $duration,
        status: ($cond.reason // "Unknown"),
        succeeded: ($cond.status == "True")
      })
    })
  '
}

# Process each PipelineRun JSON record from stdin
# We use a while loop because jq doesn't have native SHA256, so we compute
# the namespace hash in bash for each record.
while IFS= read -r record; do
  # Skip empty lines
  [[ -z "$record" ]] && continue

  # Extract namespace and compute SHA256 hash
  ns=$(echo "$record" | jq -r '.metadata.namespace // "unknown"')
  ns_hash=$(hash_namespace "$ns")

  # Transform to Segment events (outputs two lines: Started + Completed)
  transform_record "$record" "$ns_hash"
done
