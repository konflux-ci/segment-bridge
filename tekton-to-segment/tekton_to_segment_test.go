package main

import (
	"encoding/json"
	"os"
	"sort"
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
	sortSegmentEvents(expectedObjs)
	sortSegmentEvents(actualObjs)

	for i := 0; i < len(expectedObjs) && i < len(actualObjs); i++ {
		assert.Equal(t, expectedObjs[i], actualObjs[i], "Event %d mismatch after canonical sort", i+1)
	}
}

// segmentEventSortKey orders paired "…-started" before "…-completed" for the same resource
// (jq versions may emit the two comma-separated outputs in either order).
func segmentEventSortKey(obj map[string]interface{}) string {
	mid, _ := obj["messageId"].(string)
	switch {
	case strings.HasSuffix(mid, "-started"):
		return strings.TrimSuffix(mid, "-started") + "\x00\x00"
	case strings.HasSuffix(mid, "-completed"):
		return strings.TrimSuffix(mid, "-completed") + "\x00\x01"
	default:
		return mid + "\x00\x02"
	}
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

func sortSegmentEvents(events []map[string]interface{}) {
	sort.SliceStable(events, func(i, j int) bool {
		return segmentEventSortKey(events[i]) < segmentEventSortKey(events[j])
	})
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
