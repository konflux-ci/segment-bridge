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
	t.Setenv("CLUSTER_ID", clusterIDEnv)
	t.Setenv("KONFLUX_VERSION", "1.2.3")
	t.Setenv("KUBERNETES_VERSION", "1.30")
	t.Setenv("HEARTBEAT_TIMESTAMP", "2026-03-04T08:00:00Z")
	t.Setenv("EMIT_HEARTBEAT", "")

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

// TestTektonToSegment_EmptyLines verifies that blank lines in the input are
// silently skipped (the "[[ -z "$record" ]] && continue" guard) and that the
// heartbeat event is still emitted at the end.
func TestTektonToSegment_EmptyLines(t *testing.T) {
	t.Setenv("CLUSTER_ID", "test-cluster")
	t.Setenv("HEARTBEAT_TIMESTAMP", "2026-03-04T08:00:00Z")
	t.Setenv("KONFLUX_VERSION", "")
	t.Setenv("KUBERNETES_VERSION", "")
	t.Setenv("EMIT_HEARTBEAT", "")

	output, err := testfixture.RunScriptWithInputFile("testdata/blank-lines.ndjson", scriptPath)
	require.NoError(t, err, "script must exit 0 when input contains only blank lines")

	lines := trimNonEmptyLines(string(output))
	require.Len(t, lines, 1, "expected exactly one output line (heartbeat only)")

	var event map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &event), "output must be valid JSON")
	assert.Equal(t, "Segment Bridge Heartbeat", event["event"])
}

func runToSegmentWithStderr(t *testing.T, inputPath string) (stdout, stderr string, err error) {
	t.Helper()
	f, err := os.Open(inputPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	out, errb, runErr := testfixture.RunRepoScriptWithStderr(scriptPath, f, nil)
	return string(out), string(errb), runErr
}

func TestEmitHeartbeatToggle(t *testing.T) {
	const blankInput = "testdata/blank-lines.ndjson"

	tests := []struct {
		name          string
		emitHeartbeat string
		wantHeartbeat bool
		wantStderr    []string
	}{
		{
			name:          "default enabled",
			wantHeartbeat: true,
			wantStderr:    []string{"Effective telemetry toggles: EMIT_HEARTBEAT=true"},
		},
		{
			name:          "heartbeat off",
			emitHeartbeat: "false",
			wantHeartbeat: false,
			wantStderr:    []string{"Effective telemetry toggles: EMIT_HEARTBEAT=false"},
		},
		{
			name:          "case-insensitive false",
			emitHeartbeat: "FALSE",
			wantHeartbeat: false,
			wantStderr:    []string{"Effective telemetry toggles: EMIT_HEARTBEAT=false"},
		},
		{
			name:          "invalid value fail-open",
			emitHeartbeat: "nope",
			wantHeartbeat: true,
			wantStderr: []string{
				"WARNING: unrecognized value 'nope' for EMIT_HEARTBEAT; treating as enabled",
				"Effective telemetry toggles: EMIT_HEARTBEAT=true",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLUSTER_ID", "test-cluster")
			t.Setenv("HEARTBEAT_TIMESTAMP", "2026-03-04T08:00:00Z")
			t.Setenv("KONFLUX_VERSION", "")
			t.Setenv("KUBERNETES_VERSION", "")
			// Always set so a host EMIT_HEARTBEAT=false cannot leak into
			// the default-enabled case. Empty is treated as enabled.
			t.Setenv("EMIT_HEARTBEAT", tc.emitHeartbeat)

			stdout, stderr, err := runToSegmentWithStderr(t, blankInput)
			require.NoError(t, err, "script must exit 0; stderr:\n%s", stderr)

			lines := trimNonEmptyLines(stdout)
			if tc.wantHeartbeat {
				require.Len(t, lines, 1, "expected exactly one output line (heartbeat)")
				var event map[string]interface{}
				require.NoError(t, json.Unmarshal([]byte(lines[0]), &event))
				assert.Equal(t, "Segment Bridge Heartbeat", event["event"])
			} else {
				assert.Empty(t, lines, "heartbeat must not be emitted")
			}
			for _, want := range tc.wantStderr {
				assert.Contains(t, stderr, want)
			}
		})
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
