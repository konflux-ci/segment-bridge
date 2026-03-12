package testfixture

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SegmentEvent represents a single event in the Segment Batch API payload.
type SegmentEvent struct {
	Type        string                 `json:"type"`
	AnonymousID string                 `json:"anonymousId"`
	Event       string                 `json:"event"`
	MessageID   string                 `json:"messageId"`
	Timestamp   string                 `json:"timestamp"`
	Context     map[string]interface{} `json:"context"`
	Properties  map[string]interface{} `json:"properties"`
}

// SegmentBatch represents the top-level payload sent to the Segment Batch API.
type SegmentBatch struct {
	Batch []SegmentEvent `json:"batch"`
}

// SegmentBridgeHeartbeatEvent is the event name emitted by tekton-to-segment.sh
// when no other records were processed (heartbeat only).
const SegmentBridgeHeartbeatEvent = "Segment Bridge Heartbeat"

// ComputeNamespaceHash matches SHA256(namespace:cluster_id) from tekton-to-segment.sh
// (first 12 hex characters).
func ComputeNamespaceHash(namespace, clusterID string) string {
	h := sha256.Sum256([]byte(namespace + ":" + clusterID))
	return fmt.Sprintf("%x", h)[:12]
}

// ComputeClusterIDHash matches hash_cluster_id from tekton-to-segment.sh
// (SHA256 of CLUSTER_ID, first 12 hex characters).
func ComputeClusterIDHash(clusterID string) string {
	h := sha256.Sum256([]byte(clusterID))
	return fmt.Sprintf("%x", h)[:12]
}

// ComputeComponentIdentityHash matches hash_component_identity from tekton-to-segment.sh.
func ComputeComponentIdentityHash(name, ns, clusterID string) string {
	h := sha256.Sum256([]byte(name + ":" + ns + ":" + clusterID))
	return fmt.Sprintf("%x", h)[:12]
}

// ComputeApplicationInNamespaceHash matches hash_application_in_namespace from tekton-to-segment.sh.
func ComputeApplicationInNamespaceHash(application, ns, clusterID string) string {
	h := sha256.Sum256([]byte(application + ":" + ns + ":" + clusterID))
	return fmt.Sprintf("%x", h)[:12]
}

// CollectSegmentEventsFromBodies decodes Segment batch POST bodies into a flat event slice.
func CollectSegmentEventsFromBodies(t *testing.T, bodies []string) []SegmentEvent {
	t.Helper()
	var events []SegmentEvent
	for _, body := range bodies {
		var batch SegmentBatch
		require.NoError(t, json.Unmarshal([]byte(body), &batch),
			"Failed to decode batch payload")
		events = append(events, batch.Batch...)
	}
	return events
}

// FindSegmentEvent returns the event with the given messageId, or nil.
func FindSegmentEvent(events []SegmentEvent, messageID string) *SegmentEvent {
	for i := range events {
		if events[i].MessageID == messageID {
			return &events[i]
		}
	}
	return nil
}

// AssertSegmentTrackEnvelope checks type, anonymousId, and context fields from the
// Segment batch contract (aligned with tekton-to-segment/sample/expected/).
func AssertSegmentTrackEnvelope(t *testing.T, ev *SegmentEvent, clusterID string) {
	t.Helper()
	require.NotNil(t, ev)
	assert.Equal(t, "track", ev.Type)
	assert.Equal(t, "anonymous", ev.AnonymousID)
	lib, ok := ev.Context["library"].(map[string]interface{})
	require.True(t, ok, "context.library missing or wrong type")
	assert.Equal(t, "segment-bridge", lib["name"])
	assert.Equal(t, "2.0.0", lib["version"])
	wantDevice := ComputeClusterIDHash(clusterID)
	dev, ok := ev.Context["device"].(map[string]interface{})
	require.True(t, ok, "context.device missing (clusterId hashing requires device.id)")
	assert.Equal(t, wantDevice, dev["id"])
}

// AssertSegmentHeartbeat finds the single heartbeat event and checks envelope,
// clusterIdHash, and optional Konflux version properties from the public-info path.
func AssertSegmentHeartbeat(t *testing.T, events []SegmentEvent, clusterID, wantKonfluxVersion, wantKubernetesVersion string) {
	t.Helper()
	wantHash := ComputeClusterIDHash(clusterID)
	var heartbeats []SegmentEvent
	for i := range events {
		if events[i].Event == SegmentBridgeHeartbeatEvent {
			heartbeats = append(heartbeats, events[i])
		}
	}
	require.Len(t, heartbeats, 1, "expected exactly one %q event", SegmentBridgeHeartbeatEvent)
	hb := heartbeats[0]
	AssertSegmentTrackEnvelope(t, &hb, clusterID)
	assert.Equal(t, wantHash, hb.Properties["clusterIdHash"])
	if wantKonfluxVersion != "" {
		assert.Equal(t, wantKonfluxVersion, hb.Properties["konfluxVersion"])
	}
	if wantKubernetesVersion != "" {
		assert.Equal(t, wantKubernetesVersion, hb.Properties["kubernetesVersion"])
	}
}

// AssertKonfluxOperatorEvents checks Operator Deployment Started/Completed events.
func AssertKonfluxOperatorEvents(t *testing.T, events []SegmentEvent, crUID, operatorNS, clusterID string) {
	t.Helper()

	nsHash := ComputeNamespaceHash(operatorNS, clusterID)
	started := FindSegmentEvent(events, crUID+"-started")
	completed := FindSegmentEvent(events, crUID+"-completed")
	require.NotNil(t, started)
	require.NotNil(t, completed)
	AssertSegmentTrackEnvelope(t, started, clusterID)
	AssertSegmentTrackEnvelope(t, completed, clusterID)
	assert.Equal(t, "Operator Deployment Started", started.Event)
	assert.Equal(t, "Operator Deployment Completed", completed.Event)
	assert.Equal(t, nsHash, started.Properties["namespaceHash"])
	assert.Equal(t, nsHash, completed.Properties["namespaceHash"])
	h := ComputeClusterIDHash(clusterID)
	assert.Equal(t, h, started.Properties["clusterIdHash"])
	assert.Equal(t, h, completed.Properties["clusterIdHash"])
}

// AssertNamespaceCreatedEvent checks a Namespace Created KPI event.
func AssertNamespaceCreatedEvent(t *testing.T, events []SegmentEvent, nsUID, tenantName, clusterID string) {
	t.Helper()

	ev := FindSegmentEvent(events, nsUID+"-namespace-created")
	require.NotNil(t, ev, "missing Namespace Created for uid %s", nsUID)
	AssertSegmentTrackEnvelope(t, ev, clusterID)
	assert.Equal(t, "Namespace Created", ev.Event)
	assert.Equal(t, ComputeNamespaceHash(tenantName, clusterID), ev.Properties["namespaceHash"])
}

// AssertComponentCreatedEvent checks a Component Created KPI event.
func AssertComponentCreatedEvent(t *testing.T, events []SegmentEvent, compUID, name, ns, application, clusterID string) {
	t.Helper()

	ev := FindSegmentEvent(events, compUID+"-component-created")
	require.NotNil(t, ev, "missing Component Created for uid %s", compUID)
	AssertSegmentTrackEnvelope(t, ev, clusterID)
	assert.Equal(t, "Component Created", ev.Event)
	assert.Equal(t, ComputeNamespaceHash(ns, clusterID), ev.Properties["namespaceHash"])
	assert.Equal(t, ComputeComponentIdentityHash(name, ns, clusterID), ev.Properties["componentHash"])
	assert.Equal(t, ComputeApplicationInNamespaceHash(application, ns, clusterID), ev.Properties["applicationHash"])
}
