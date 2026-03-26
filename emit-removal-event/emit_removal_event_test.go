package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scriptPath = "../scripts/emit-removal-event.sh"

func TestEmitRemovalEvent(t *testing.T) {
	output, err := testfixture.RunRepoScript(scriptPath, nil, []string{
		"CLUSTER_ID=test-cluster",
		"REMOVAL_CR_UID=test-uid-12345678",
		"REMOVAL_TIMESTAMP=2026-03-04T10:00:00Z",
		"PATH=" + os.Getenv("PATH"),
	})
	require.NoError(t, err, "Script execution failed")

	var event map[string]interface{}
	require.NoError(t, json.Unmarshal(output, &event), "Output is not valid JSON")

	assert.Equal(t, "track", event["type"])
	assert.Equal(t, "anonymous", event["anonymousId"])
	assert.Equal(t, "Operator Removal Started", event["event"])
	assert.Equal(t, "test-uid-12345678-removal", event["messageId"])
	assert.Equal(t, "2026-03-04T10:00:00Z", event["timestamp"])

	ctx, ok := event["context"].(map[string]interface{})
	require.True(t, ok, "context should be an object")
	lib, ok := ctx["library"].(map[string]interface{})
	require.True(t, ok, "context.library should be an object")
	assert.Equal(t, "segment-bridge", lib["name"])
	assert.Equal(t, "2.0.0", lib["version"])
	device, ok := ctx["device"].(map[string]interface{})
	require.True(t, ok, "context.device should be an object")
	assert.Equal(t, "f069097ced1b", device["id"])

	props, ok := event["properties"].(map[string]interface{})
	require.True(t, ok, "properties should be an object")
	assert.Equal(t, "f069097ced1b", props["clusterIdHash"])
}

func TestEmitRemovalEventNoClusterID(t *testing.T) {
	output, err := testfixture.RunRepoScript(scriptPath, nil, []string{
		"REMOVAL_CR_UID=uid-no-cluster",
		"REMOVAL_TIMESTAMP=2026-01-01T00:00:00Z",
		"PATH=" + os.Getenv("PATH"),
	})
	require.NoError(t, err, "Script execution failed")

	var event map[string]interface{}
	require.NoError(t, json.Unmarshal(output, &event), "Output is not valid JSON")

	assert.Equal(t, "Operator Removal Started", event["event"])
	assert.Equal(t, "uid-no-cluster-removal", event["messageId"])

	ctx, ok := event["context"].(map[string]interface{})
	require.True(t, ok)
	_, hasDevice := ctx["device"]
	assert.False(t, hasDevice, "No device when CLUSTER_ID is empty")

	props, ok := event["properties"].(map[string]interface{})
	require.True(t, ok)
	_, hasCluster := props["clusterIdHash"]
	assert.False(t, hasCluster, "No clusterIdHash when CLUSTER_ID is empty")
}

func TestEmitRemovalEventNoWriteKey(t *testing.T) {
	output, err := testfixture.RunRepoScript(scriptPath, nil, []string{
		"CLUSTER_ID=test-cluster",
		"REMOVAL_CR_UID=uid-no-key",
		"REMOVAL_TIMESTAMP=2026-06-15T12:00:00Z",
		"PATH=" + os.Getenv("PATH"),
	})
	require.NoError(t, err, "Script should exit 0 when SEGMENT_WRITE_KEY is empty")

	var event map[string]interface{}
	require.NoError(t, json.Unmarshal(output, &event))
	assert.Equal(t, "Operator Removal Started", event["event"])
}
