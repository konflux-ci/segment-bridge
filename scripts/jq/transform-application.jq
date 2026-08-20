# transform-application.jq
# Transform a single Application JSON into one Segment event (Application Created).
#
# Required --arg parameters:
#   ns_hash          - pre-computed namespace hash
#   application_hash - pre-computed application hash (name:namespace:cluster_id)
#   cluster_id_hash  - pre-computed cluster ID hash (empty string when unused)
#   konflux_version  - Konflux version string (empty string when unknown)
#   kubernetes_version - Kubernetes version string (empty string when unknown)

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
  applicationHash: $application_hash
} + $clusterProp + $konfluxProp + $k8sProp) as $props |
$base + {
  messageId: (.metadata.uid + "-application-created"),
  timestamp: .metadata.creationTimestamp,
  event: "Application Created",
  properties: $props
}
