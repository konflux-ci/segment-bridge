package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fetchTektonScript = "../scripts/fetch-tekton-records.sh"

func TestMain(m *testing.M) {
	// Host-built mock tkn-results and testdata paths are not bind-mounted when
	// SEGMENT_BRIDGE_TEST_IMAGE is set (CI unit_tests workflow).
	os.Setenv(testfixture.EnvTestImage, "")
	os.Exit(m.Run())
}

// buildMockTknResults compiles the mock-tkn-results binary into a temp dir and
// returns that dir. The binary is placed as "tkn-results" so the script finds
// it on PATH.
func buildMockTknResults(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "tkn-results")
	cmd := exec.Command("go", "build", "-o", out, "../tekton-e2e/mock-tkn-results")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "build mock-tkn-results: %s", data)
	return dir
}

// runFetchTekton runs fetch-tekton-records.sh with the mock tkn-results binary
// on PATH and the given response fixture file. Extra env overrides are appended
// last (last binding wins in exec env on Linux/macOS).
func runFetchTekton(t *testing.T, mockDir, responseFile string, extraEnv map[string]string) ([]byte, error) {
	t.Helper()
	absResponseFile, err := filepath.Abs(responseFile)
	require.NoError(t, err)
	env := os.Environ()
	env = append(env, "PATH="+mockDir+":"+os.Getenv("PATH"))
	env = append(env, "MOCK_TKN_RESULTS_RESPONSE_FILE="+absResponseFile)
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	return testfixture.RunRepoScript(fetchTektonScript, nil, env)
}

// nonEmptyLines splits output into non-empty trimmed lines.
func nonEmptyLines(output []byte) []string {
	var result []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) != "" {
			result = append(result, strings.TrimSpace(line))
		}
	}
	return result
}

// TestFetchTektonRecords verifies the happy path: 2 PipelineRun lines are
// emitted and the interleaved TaskRun record is absent from the output.
func TestFetchTektonRecords(t *testing.T) {
	mockDir := buildMockTknResults(t)

	out, err := runFetchTekton(t, mockDir, "testdata/records-pipelineruns.json", map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)

	lines := nonEmptyLines(out)
	assert.Len(t, lines, 2, "expected 2 PipelineRun lines, got %d", len(lines))
	for i, line := range lines {
		var obj map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &obj), "line %d must be valid JSON", i)
		kind, _ := obj["kind"].(string)
		assert.Equal(t, "PipelineRun", kind, "line %d: expected kind PipelineRun", i)
	}
	combined := strings.Join(lines, "\n")
	assert.NotContains(t, combined, "TaskRun", "TaskRun record must be filtered out")
}

// TestFetchTektonRecordsEmpty verifies that an empty records list produces no
// output lines.
func TestFetchTektonRecordsEmpty(t *testing.T) {
	mockDir := buildMockTknResults(t)

	out, err := runFetchTekton(t, mockDir, "testdata/records-empty.json", map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)

	assert.Empty(t, strings.TrimSpace(string(out)), "empty record list should produce no output")
}

func runFetchTektonWithStderr(t *testing.T, mockDir, responseFile string, extraEnv map[string]string) ([]byte, []byte, error) {
	t.Helper()
	absResponseFile, err := filepath.Abs(responseFile)
	require.NoError(t, err)
	env := os.Environ()
	env = append(env, "PATH="+mockDir+":"+os.Getenv("PATH"))
	env = append(env, "MOCK_TKN_RESULTS_RESPONSE_FILE="+absResponseFile)
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	return testfixture.RunRepoScriptWithStderr(fetchTektonScript, nil, env)
}

func TestFetchTektonRecordsSATokenFallback(t *testing.T) {
	mockDir := buildMockTknResults(t)

	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("sa-test-token"), 0o600))

	out, err := runFetchTekton(t, mockDir, "testdata/records-pipelineruns.json", map[string]string{
		"TEKTON_RESULTS_TOKEN":    "",
		"SA_TOKEN_PATH":           tokenFile,
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)

	lines := nonEmptyLines(out)
	assert.Len(t, lines, 2, "SA token path: expected 2 PipelineRun lines, got %d", len(lines))
}

func TestFetchTektonRecordsNoToken(t *testing.T) {
	mockDir := buildMockTknResults(t)

	_, stderr, err := runFetchTektonWithStderr(t, mockDir, "testdata/records-empty.json", map[string]string{
		"TEKTON_RESULTS_TOKEN": "",
		"SA_TOKEN_PATH":        "/nonexistent/path/token",
	})
	require.Error(t, err)
	assert.Contains(t, string(stderr), "No authentication token available")
}
