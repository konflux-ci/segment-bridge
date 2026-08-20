# transform-release.jq
# Transform a single Release JSON into two Segment events
# (Release Created + Release Released), both generated retroactively.
#
# Required --arg parameters:
#   ns_hash           - pre-computed namespace hash
#   release_hash      - pre-computed release hash (name:namespace:cluster_id)
#   snapshot_hash     - pre-computed snapshot hash (spec.snapshot:namespace:cluster_id)
#   release_plan_hash - pre-computed release plan hash
#                       (spec.releasePlan:namespace:cluster_id)
#   cluster_id_hash   - pre-computed cluster ID hash (empty string when unused)
#   konflux_version   - Konflux version string (empty string when unknown)
#   kubernetes_version - Kubernetes version string (empty string when unknown)

# Extract Released condition
((.status.conditions // []) | map(select(.type == "Released")) | .[0]) as $cond |

# Duration in seconds (null if timestamps missing)
(
  if .metadata.creationTimestamp and .status.completionTime then
    ((.status.completionTime | fromdateiso8601) -
     (.metadata.creationTimestamp | fromdateiso8601))
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
  namespaceHash: $ns_hash,
  releaseHash: $release_hash,
  snapshotHash: $snapshot_hash,
  releasePlanHash: $release_plan_hash
} + $clusterProp + $konfluxProp + $k8sProp) as $commonProps |

# Event 1: Release Created
($base + {
  messageId: (.metadata.uid + "-release-created"),
  timestamp: .metadata.creationTimestamp,
  event: "Release Created",
  properties: $commonProps
}),

# Event 2: Release Released (only when completionTime is present)
if .status.completionTime then
  ($base + {
    messageId: (.metadata.uid + "-release-released"),
    timestamp: .status.completionTime,
    event: "Release Released",
    properties: ($commonProps + {
      startTime: .metadata.creationTimestamp,
      completionTime: .status.completionTime,
      durationSeconds: $duration,
      status: ($cond.reason // "Unknown")
    })
  })
else
  empty
end
