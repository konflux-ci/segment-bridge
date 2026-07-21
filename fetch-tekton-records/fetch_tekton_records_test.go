package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fetchTektonScript = "../scripts/fetch-tekton-records.sh"

func TestMain(m *testing.M) {
	os.Setenv(testfixture.EnvTestImage, "")
	os.Exit(m.Run())
}

// startMockResultsAPIWithCapture starts an HTTP server that serves the given
// fixture and captures the last request via a pointer-to-pointer so the caller
// sees the value set by the handler goroutine.
func startMockResultsAPIWithCapture(t *testing.T, fixtureFile string) (*httptest.Server, **http.Request) {
	t.Helper()
	absFixture, err := filepath.Abs(fixtureFile)
	require.NoError(t, err)

	var mu sync.Mutex
	var captured *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = r
		mu.Unlock()
		data, err := os.ReadFile(absFixture)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	t.Cleanup(server.Close)
	return server, &captured
}

// runFetchTekton runs fetch-tekton-records.sh with the given env overrides.
func runFetchTekton(t *testing.T, extraEnv map[string]string) ([]byte, error) {
	t.Helper()
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	return testfixture.RunRepoScript(fetchTektonScript, nil, env)
}

func runFetchTektonWithStderr(t *testing.T, extraEnv map[string]string) ([]byte, []byte, error) {
	t.Helper()
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	return testfixture.RunRepoScriptWithStderr(fetchTektonScript, nil, env)
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
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	out, err := runFetchTekton(t, map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": server.URL,
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

	require.NotNil(t, *captured, "expected an HTTP request to be made")
	assert.Equal(t, "Bearer test-token", (*captured).Header.Get("Authorization"))
}

// TestFetchTektonRecordsOrderBy verifies that the HTTP request includes
// the order_by=create_time desc query parameter.
func TestFetchTektonRecordsOrderBy(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	_, err := runFetchTekton(t, map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": server.URL,
	})
	require.NoError(t, err)

	require.NotNil(t, *captured)
	q := (*captured).URL.Query()
	assert.Equal(t, "create_time desc", q.Get("order_by"),
		"order_by must be create_time desc")
}

// TestFetchTektonRecordsPageSize verifies that the page_size query parameter
// matches TEKTON_LIMIT.
func TestFetchTektonRecordsPageSize(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	_, err := runFetchTekton(t, map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": server.URL,
		"TEKTON_LIMIT":            "42",
	})
	require.NoError(t, err)

	require.NotNil(t, *captured)
	q := (*captured).URL.Query()
	assert.Equal(t, "42", q.Get("page_size"),
		"page_size must match TEKTON_LIMIT")
}

// TestFetchTektonRecordsAPIPath verifies the Tekton Results REST API path.
func TestFetchTektonRecordsAPIPath(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	_, err := runFetchTekton(t, map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": server.URL,
		"TEKTON_NAMESPACE":        "my-ns",
	})
	require.NoError(t, err)

	require.NotNil(t, *captured)
	assert.Contains(t, (*captured).URL.Path,
		"/apis/results.tekton.dev/v1alpha2/parents/my-ns/results/-/records")
}

// TestFetchTektonRecordsWildcardNamespace verifies that the default namespace
// wildcard "-" is used in the API path.
func TestFetchTektonRecordsWildcardNamespace(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	_, err := runFetchTekton(t, map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": server.URL,
	})
	require.NoError(t, err)

	require.NotNil(t, *captured)
	assert.Contains(t, (*captured).URL.Path,
		"/apis/results.tekton.dev/v1alpha2/parents/-/results/-/records")
}

// TestFetchTektonRecordsEmpty verifies that an empty records list produces no
// output lines.
func TestFetchTektonRecordsEmpty(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-empty.json")

	out, err := runFetchTekton(t, map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": server.URL,
	})
	require.NoError(t, err)

	assert.Empty(t, strings.TrimSpace(string(out)), "empty record list should produce no output")
}

func TestFetchTektonRecordsSATokenFallback(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("sa-test-token"), 0o600))

	out, err := runFetchTekton(t, map[string]string{
		"TEKTON_RESULTS_TOKEN":    "",
		"SA_TOKEN_PATH":           tokenFile,
		"TEKTON_RESULTS_API_ADDR": server.URL,
	})
	require.NoError(t, err)

	lines := nonEmptyLines(out)
	assert.Len(t, lines, 2, "SA token path: expected 2 PipelineRun lines, got %d", len(lines))

	require.NotNil(t, *captured)
	assert.Equal(t, "Bearer sa-test-token", (*captured).Header.Get("Authorization"))
}

func TestFetchTektonRecordsNoToken(t *testing.T) {
	_, stderr, err := runFetchTektonWithStderr(t, map[string]string{
		"TEKTON_RESULTS_TOKEN":    "",
		"SA_TOKEN_PATH":           "/nonexistent/path/token",
		"TEKTON_RESULTS_API_ADDR": "http://unused:1234",
	})
	require.Error(t, err)
	assert.Contains(t, string(stderr), "No authentication token available")
}

// TestFetchTektonRecordsSchemePrepend verifies that a bare host:port address
// (no scheme) gets https:// prepended automatically by the script, and that
// the resulting request successfully reaches an HTTPS server.
func TestFetchTektonRecordsSchemePrepend(t *testing.T) {
	absFixture, err := filepath.Abs("testdata/records-pipelineruns.json")
	require.NoError(t, err)

	var mu sync.Mutex
	var captured *http.Request
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = r
		mu.Unlock()
		data, err := os.ReadFile(absFixture)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	t.Cleanup(tlsServer.Close)

	// Strip the https:// scheme so the script must prepend it back.
	bareAddr := strings.TrimPrefix(tlsServer.URL, "https://")

	_, err = runFetchTekton(t, map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": bareAddr,
	})
	require.NoError(t, err)

	require.NotNil(t, captured, "expected the script to prepend https:// and reach the mock server")
	assert.Contains(t, captured.URL.Path, "/apis/results.tekton.dev/")
}
