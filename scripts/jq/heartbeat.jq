# heartbeat.jq
# Emit a Segment Bridge Heartbeat event so Segment can confirm the cluster
# is alive even when no PipelineRun records were processed.
#
# Required --arg parameters (pass with jq -n):
#   cluster_id_hash  - pre-computed cluster ID hash (empty string when unused)
#   timestamp        - ISO-8601 UTC timestamp for the heartbeat event
#   konflux_version  - Konflux version string (empty string when unknown)
#   kubernetes_version - Kubernetes version string (empty string when unknown)

{
  type: "track",
  anonymousId: "anonymous",
  messageId: (if $cluster_id_hash != "" then ($cluster_id_hash + "-heartbeat-" + $timestamp) else ("heartbeat-" + $timestamp) end),
  timestamp: $timestamp,
  event: "Segment Bridge Heartbeat",
  context: (
    {library: {name: "segment-bridge", version: "2.0.0"}}
    + (if $cluster_id_hash != "" then {device: {id: $cluster_id_hash}} else {} end)
  ),
  properties: (
    (if $cluster_id_hash != "" then {clusterIdHash: $cluster_id_hash} else {} end)
    + (if $konflux_version != "" then {konfluxVersion: $konflux_version} else {} end)
    + (if $kubernetes_version != "" then {kubernetesVersion: $kubernetes_version} else {} end)
  )
}
