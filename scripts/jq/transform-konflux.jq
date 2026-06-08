# transform-konflux.jq
# Transform a single Konflux CR JSON into two Segment events
# (Operator Deployment Started + Completed).
#
# Required --arg parameters:
#   ns_hash          - pre-computed namespace hash
#   cluster_id_hash  - pre-computed cluster ID hash (empty string when unused)
#   konflux_version  - Konflux version string (empty string when unknown)
#   kubernetes_version - Kubernetes version string (empty string when unknown)

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
