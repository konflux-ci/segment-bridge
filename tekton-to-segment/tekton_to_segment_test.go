package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	scriptPath   = "../scripts/tekton-to-segment.sh"
	inputPath    = "sample/input.json"
	expectedPath = "sample/expected.json"
	clusterIDEnv = "test-cluster"
)

func TestTektonToSegment(t *testing.T) {
	require.NoError(t, os.Setenv("CLUSTER_ID", clusterIDEnv), "Failed to set CLUSTER_ID")
	require.NoError(t, os.Setenv("KONFLUX_VERSION", "1.2.3"), "Failed to set KONFLUX_VERSION")
	require.NoError(t, os.Setenv("KUBERNETES_VERSION", "1.30"), "Failed to set KUBERNETES_VERSION")
	require.NoError(t, os.Setenv("HEARTBEAT_TIMESTAMP", "2026-03-04T08:00:00Z"), "Failed to set HEARTBEAT_TIMESTAMP")

	expectedBytes, err := os.ReadFile(expectedPath)
	require.NoError(t, err, "Failed to read expected output file")
	expectedLines := trimNonEmptyLines(string(expectedBytes))
	require.NotEmpty(t, expectedLines, "Expected output must not be empty")

	output, err := testfixture.RunScriptWithInputFile(inputPath, scriptPath)
	require.NoError(t, err, "Script execution failed")

	actualLines := trimNonEmptyLines(string(output))
	assert.Equal(t, len(expectedLines), len(actualLines),
		"Output line count mismatch: expected %d, got %d", len(expectedLines), len(actualLines))

	expectedObjs := parseSegmentEventLines(t, expectedLines)
	actualObjs := parseSegmentEventLines(t, actualLines)

	actualByMessageID := indexSegmentEventsByMessageID(t, actualObjs)
	for i, exp := range expectedObjs {
		mid, ok := messageIDString(exp)
		require.True(t, ok, "expected line %d: messageId is not a non-empty string", i+1)
		act, ok := actualByMessageID[mid]
		require.True(t, ok, "actual output missing messageId %q", mid)
		assert.Equal(t, exp, act, "Event %q mismatch", mid)
	}
}

func messageIDString(obj map[string]interface{}) (string, bool) {
	mid, ok := obj["messageId"].(string)
	return mid, ok && mid != ""
}

// indexSegmentEventsByMessageID maps messageId -> event. Fails the test if a messageId repeats.
func indexSegmentEventsByMessageID(t *testing.T, events []map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	out := make(map[string]map[string]interface{}, len(events))
	for i, ev := range events {
		mid, ok := messageIDString(ev)
		require.True(t, ok, "actual line %d: messageId is not a non-empty string", i+1)
		_, dup := out[mid]
		require.False(t, dup, "duplicate messageId in actual output: %q", mid)
		out[mid] = ev
	}
	return out
}

func parseSegmentEventLines(t *testing.T, lines []string) []map[string]interface{} {
	t.Helper()
	out := make([]map[string]interface{}, 0, len(lines))
	for i, line := range lines {
		var obj map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &obj), "line %d is not valid JSON", i+1)
		out = append(out, obj)
	}
	return out
}

// trimNonEmptyLines splits on newlines and returns non-empty trimmed lines.
func trimNonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}
