# transform-record.jq
# Transform a single PipelineRun JSON into two Segment events
# (PipelineRun Started + Completed), both generated retroactively.
#
# Required --arg parameters:
#   ns_hash          - pre-computed namespace hash
#   cluster_id_hash  - pre-computed cluster ID hash (empty string when unused)
#   konflux_version  - Konflux version string (empty string when unknown)
#   kubernetes_version - Kubernetes version string (empty string when unknown)

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
