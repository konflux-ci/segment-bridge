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

	expectedBytes, err := os.ReadFile(expectedPath)
	require.NoError(t, err, "Failed to read expected output file")
	expectedLines := trimNonEmptyLines(string(expectedBytes))
	require.NotEmpty(t, expectedLines, "Expected output must not be empty")

	output, err := testfixture.RunScriptWithInputFile(inputPath, scriptPath)
	require.NoError(t, err, "Script execution failed")

	actualLines := trimNonEmptyLines(string(output))
	assert.Equal(t, len(expectedLines), len(actualLines),
		"Output line count mismatch: expected %d, got %d", len(expectedLines), len(actualLines))

	for i := 0; i < len(expectedLines) && i < len(actualLines); i++ {
		var expectedObj, actualObj map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(expectedLines[i]), &expectedObj), "Expected line %d is not valid JSON", i+1)
		require.NoError(t, json.Unmarshal([]byte(actualLines[i]), &actualObj), "Actual line %d is not valid JSON", i+1)
		assert.Equal(t, expectedObj, actualObj, "Line %d content mismatch", i+1)
	}
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
