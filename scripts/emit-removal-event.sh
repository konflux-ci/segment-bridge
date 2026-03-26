#!/bin/bash
# emit-removal-event.sh
#   Emit an "Operator Removal Started" Segment event when the Konflux CR is deleted.
#   Called by the Konflux operator via a one-shot Job before tearing down the bridge.
#
#   Required env vars:
#     REMOVAL_CR_UID       - UID of the deleted Konflux CR
#     REMOVAL_TIMESTAMP    - Deletion timestamp (RFC3339)
#   Optional env vars:
#     CLUSTER_ID           - Cluster identifier (if not set, reads kube-system namespace UID)
#     SEGMENT_WRITE_KEY    - Segment write key (if empty, prints event to stdout and exits 0)
#     SEGMENT_BATCH_API    - Segment batch API URL (default: https://api.segment.io/v1/batch)
#
set -o pipefail -o errexit -o nounset

SELFDIR="$(cd "$(dirname "$0")" && pwd)"
PATH="$SELFDIR:${PATH#"$SELFDIR":}"

CLUSTER_ID="${CLUSTER_ID:-}"
if [[ -z "$CLUSTER_ID" ]]; then
    KUBECTL="${KUBECTL:-kubectl}"
    if command -v "$KUBECTL" &>/dev/null; then
        CLUSTER_ID="$("$KUBECTL" get namespace kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null || echo "")"
    fi
fi

CLUSTER_ID_HASH=""
if [[ -n "$CLUSTER_ID" ]]; then
    CLUSTER_ID_HASH="$(echo -n "$CLUSTER_ID" | sha256sum | cut -c1-12)"
fi

EVENT=$(jq -n -c \
    --arg cr_uid "${REMOVAL_CR_UID:-}" \
    --arg timestamp "${REMOVAL_TIMESTAMP:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" \
    --arg cluster_id_hash "$CLUSTER_ID_HASH" \
    '{
        type: "track",
        anonymousId: "anonymous",
        messageId: ($cr_uid + "-removal"),
        timestamp: $timestamp,
        event: "Operator Removal Started",
        context: (
            {library: {name: "segment-bridge", version: "2.0.0"}}
            + (if $cluster_id_hash != "" then {device: {id: $cluster_id_hash}} else {} end)
        ),
        properties: (
            (if $cluster_id_hash != "" then {clusterIdHash: $cluster_id_hash} else {} end)
        )
    }')

if [[ -z "${SEGMENT_WRITE_KEY:-}" ]]; then
    echo "$EVENT"
    exit 0
fi

SEGMENT_BATCH_API="${SEGMENT_BATCH_API:-https://api.segment.io/v1/batch}"

PAYLOAD=$(echo "$EVENT" | jq -c '{batch: [.]}')

curl -sf --retry 3 --retry-delay 2 \
    -X POST "$SEGMENT_BATCH_API" \
    -H "Content-Type: application/json" \
    -u "${SEGMENT_WRITE_KEY}:" \
    -d "$PAYLOAD"

echo "Removal event uploaded successfully" >&2
