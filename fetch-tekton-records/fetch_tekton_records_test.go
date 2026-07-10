package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// paginatedHandler routes requests to different fixture files based on the
// page_token query parameter.  The empty string key maps to the first-page
// fixture (no page_token).
type paginatedHandler struct {
	mu       sync.Mutex
	pages    map[string]string // page_token → absolute fixture path
	requests []*http.Request
}

func (h *paginatedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.requests = append(h.requests, r)
	h.mu.Unlock()

	pageToken := r.URL.Query().Get("page_token")
	fixture, ok := h.pages[pageToken]
	if !ok {
		http.Error(w, "unexpected page_token: "+pageToken, http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (h *paginatedHandler) getRequests() []*http.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]*http.Request, len(h.requests))
	copy(cp, h.requests)
	return cp
}

// startPaginatedMockAPI starts an HTTP server that serves different fixtures
// per page_token value.  Use "" as the key for the first page (no token).
func startPaginatedMockAPI(t *testing.T, pages map[string]string) (*httptest.Server, *paginatedHandler) {
	t.Helper()
	absPages := make(map[string]string, len(pages))
	for token, file := range pages {
		abs, err := filepath.Abs(file)
		require.NoError(t, err)
		absPages[token] = abs
	}
	handler := &paginatedHandler{pages: absPages}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, handler
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

// pipelineRunNames extracts metadata.name from NDJSON PipelineRun lines.
func pipelineRunNames(t *testing.T, output []byte) []string {
	t.Helper()
	var names []string
	for _, line := range nonEmptyLines(output) {
		var obj map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &obj))
		meta, _ := obj["metadata"].(map[string]interface{})
		name, _ := meta["name"].(string)
		names = append(names, name)
	}
	return names
}

// baseEnv returns the minimal env overrides to hit a test server.
// KUBECTL="" disables auto-detection so tests don't try to reach a real cluster.
func baseEnv(serverURL string) map[string]string {
	return map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": serverURL,
		"KUBECTL":                 "",
	}
}

// ---------------------------------------------------------------------------
// Original happy-path tests (adapted from PR 1)
// ---------------------------------------------------------------------------

func TestFetchTektonRecords(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	out, err := runFetchTekton(t, baseEnv(server.URL))
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

func TestFetchTektonRecordsOrderBy(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	_, err := runFetchTekton(t, baseEnv(server.URL))
	require.NoError(t, err)

	require.NotNil(t, *captured)
	q := (*captured).URL.Query()
	assert.Equal(t, "create_time desc", q.Get("order_by"),
		"order_by must be create_time desc")
}

func TestFetchTektonRecordsPageSize(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	env := baseEnv(server.URL)
	env["TEKTON_LIMIT"] = "42"
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	require.NotNil(t, *captured)
	q := (*captured).URL.Query()
	assert.Equal(t, "42", q.Get("page_size"),
		"page_size must match TEKTON_LIMIT")
}

func TestFetchTektonRecordsAPIPath(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	env := baseEnv(server.URL)
	env["TEKTON_NAMESPACE"] = "my-ns"
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	require.NotNil(t, *captured)
	assert.Contains(t, (*captured).URL.Path,
		"/apis/results.tekton.dev/v1alpha2/parents/my-ns/results/-/records")
}

func TestFetchTektonRecordsWildcardNamespace(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	_, err := runFetchTekton(t, baseEnv(server.URL))
	require.NoError(t, err)

	require.NotNil(t, *captured)
	assert.Contains(t, (*captured).URL.Path,
		"/apis/results.tekton.dev/v1alpha2/parents/-/results/-/records")
}

func TestFetchTektonRecordsEmpty(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-empty.json")

	out, err := runFetchTekton(t, baseEnv(server.URL))
	require.NoError(t, err)

	assert.Empty(t, strings.TrimSpace(string(out)), "empty record list should produce no output")
}

func TestFetchTektonRecordsSATokenFallback(t *testing.T) {
	server, captured := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("sa-test-token"), 0o600))

	env := baseEnv(server.URL)
	env["TEKTON_RESULTS_TOKEN"] = ""
	env["SA_TOKEN_PATH"] = tokenFile
	out, err := runFetchTekton(t, env)
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

// ---------------------------------------------------------------------------
// Pagination tests
// ---------------------------------------------------------------------------

func TestFetchTektonRecordsPagination(t *testing.T) {
	server, handler := startPaginatedMockAPI(t, map[string]string{
		"":           "testdata/records-page1.json",
		"page2token": "testdata/records-page2.json",
	})

	out, err := runFetchTekton(t, baseEnv(server.URL))
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-1", "pr-2"}, names,
		"pagination must return PipelineRuns from both pages")

	reqs := handler.getRequests()
	require.Len(t, reqs, 2, "expected 2 HTTP requests (2 pages)")
	assert.Empty(t, reqs[0].URL.Query().Get("page_token"),
		"first request must not have page_token")
	assert.Equal(t, "page2token", reqs[1].URL.Query().Get("page_token"),
		"second request must carry page_token from first response")
}

func TestFetchTektonRecordsPaginationPreservesQueryParams(t *testing.T) {
	server, handler := startPaginatedMockAPI(t, map[string]string{
		"":           "testdata/records-page1.json",
		"page2token": "testdata/records-page2.json",
	})

	env := baseEnv(server.URL)
	env["TEKTON_LIMIT"] = "50"
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	reqs := handler.getRequests()
	require.Len(t, reqs, 2)
	for i, r := range reqs {
		q := r.URL.Query()
		assert.Equal(t, "50", q.Get("page_size"),
			"request %d: page_size must persist across pages", i)
		assert.Equal(t, "create_time desc", q.Get("order_by"),
			"request %d: order_by must persist across pages", i)
	}
}

// TestFetchTektonRecordsPaginationURLUnsafeToken verifies that a
// nextPageToken containing URL-unsafe characters (+, /, =, as commonly
// produced by base64-encoded opaque tokens) survives the round trip: the
// script must URL-encode the token before appending it to the request URL,
// so the server-side decoded value matches the original token exactly.
func TestFetchTektonRecordsPaginationURLUnsafeToken(t *testing.T) {
	server, handler := startPaginatedMockAPI(t, map[string]string{
		"":             "testdata/records-page1-b64token.json",
		"abc+def/ghi=": "testdata/records-page2.json",
	})

	out, err := runFetchTekton(t, baseEnv(server.URL))
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-1", "pr-2"}, names,
		"pagination must follow a URL-unsafe token across both pages")

	reqs := handler.getRequests()
	require.Len(t, reqs, 2, "expected 2 HTTP requests (2 pages)")
	assert.Equal(t, "abc+def/ghi=", reqs[1].URL.Query().Get("page_token"),
		"decoded page_token on the wire must match the original unencoded token")
}

func TestFetchTektonRecordsPaginationWarnLog(t *testing.T) {
	server, _ := startPaginatedMockAPI(t, map[string]string{
		"":           "testdata/records-page1.json",
		"page2token": "testdata/records-page2.json",
	})

	_, stderr, err := runFetchTektonWithStderr(t, baseEnv(server.URL))
	require.NoError(t, err)

	assert.Contains(t, string(stderr), "WARN segment-bridge: paging to catch up",
		"multi-page fetch must emit WARN on stderr")
	assert.Contains(t, string(stderr), "2 pages",
		"WARN must report page count")
}

func TestFetchTektonRecordsSinglePageNoWarnLog(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	_, stderr, err := runFetchTektonWithStderr(t, baseEnv(server.URL))
	require.NoError(t, err)

	assert.NotContains(t, string(stderr), "WARN",
		"single-page fetch must not emit WARN")
}

// ---------------------------------------------------------------------------
// Cursor filtering tests
// ---------------------------------------------------------------------------

func TestFetchTektonRecordsCursorFilters(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-cursor-mixed.json")

	env := baseEnv(server.URL)
	env["TEKTON_CURSOR"] = "2024-01-01T12:00:00Z"
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-new"}, names,
		"cursor must filter out records with createTime <= cursor")
}

func TestFetchTektonRecordsCursorExactMatch(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-cursor-mixed.json")

	env := baseEnv(server.URL)
	env["TEKTON_CURSOR"] = "2024-01-01T14:00:00Z"
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	lines := nonEmptyLines(out)
	assert.Empty(t, lines,
		"cursor equal to newest createTime must produce no output")
}

func TestFetchTektonRecordsColdStart(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-cursor-mixed.json")

	out, err := runFetchTekton(t, baseEnv(server.URL))
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-new", "pr-old", "pr-oldest"}, names,
		"cold start (no cursor) must emit all PipelineRuns")
}

// ---------------------------------------------------------------------------
// Cursor + pagination combined tests
// ---------------------------------------------------------------------------

func TestFetchTektonRecordsCursorStopsPagination(t *testing.T) {
	server, handler := startPaginatedMockAPI(t, map[string]string{
		"":           "testdata/records-page1.json",
		"page2token": "testdata/records-page2.json",
	})

	env := baseEnv(server.URL)
	env["TEKTON_CURSOR"] = "2024-01-01T11:30:00Z"
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-1"}, names,
		"page 1 records are newer than cursor, page 1 has nextPageToken "+
			"but no record <= cursor yet, so page 2 is fetched; "+
			"page 2 record (11:00) <= cursor stops pagination and is filtered out")

	reqs := handler.getRequests()
	assert.Len(t, reqs, 2,
		"page 2 is needed because page 1 has no record with createTime <= cursor")
}

func TestFetchTektonRecordsCursorHitsOnFirstPage(t *testing.T) {
	server, handler := startPaginatedMockAPI(t, map[string]string{
		"":           "testdata/records-cursor-mixed-page1.json",
		"page2token": "testdata/records-page2.json",
	})

	env := baseEnv(server.URL)
	env["TEKTON_CURSOR"] = "2024-01-01T12:00:00Z"
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-new"}, names,
		"cursor hit on first page stops pagination early")

	reqs := handler.getRequests()
	assert.Len(t, reqs, 1,
		"must not fetch page 2 when cursor is reached on page 1")
}

// ---------------------------------------------------------------------------
// Cursor persistence tests (via kubectl stub)
// ---------------------------------------------------------------------------

func TestFetchTektonRecordsCursorReadFromConfigMap(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-cursor-mixed.json")

	stubDir := t.TempDir()
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
case "$1" in
  get) echo -n "2024-01-01T12:00:00Z" ;;
  create) cat <<'YAML'
apiVersion: v1
kind: ConfigMap
data:
  last_processed_create_time: "ignored"
YAML
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-new"}, names,
		"cursor read from ConfigMap stub must filter old records")
}

func TestFetchTektonRecordsCursorWriteToConfigMap(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	logFile := filepath.Join(stubDir, "kubectl.log")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
echo "$@" >> "`+logFile+`"
case "$1" in
  get) exit 1 ;;
  create)
    for arg in "$@"; do
      case "$arg" in
        --from-literal=last_processed_create_time=*)
          VALUE="${arg#--from-literal=last_processed_create_time=}"
          printf 'apiVersion: v1\nkind: ConfigMap\ndata:\n  last_processed_create_time: "%s"\n' "$VALUE"
          ;;
      esac
    done
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	logStr := string(logData)

	assert.Contains(t, logStr, "last_processed_create_time=2024-01-01T11:05:29Z",
		"cursor must be advanced to max(createTime) minus 1 second (tie-break overlap backoff)")
}

func TestFetchTektonRecordsCursorWriteNoop(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-empty.json")

	stubDir := t.TempDir()
	logFile := filepath.Join(stubDir, "kubectl.log")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
echo "$@" >> "`+logFile+`"
case "$1" in
  get) exit 1 ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	lines := nonEmptyLines(logData)

	for _, line := range lines {
		assert.NotContains(t, line, "create configmap",
			"empty response must not attempt to write cursor")
	}
}

func TestFetchTektonRecordsNoKubectl(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	env := baseEnv(server.URL)
	env["KUBECTL"] = ""
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	lines := nonEmptyLines(out)
	assert.Len(t, lines, 2,
		"without kubectl, cursor is disabled — all records output")
}

// ---------------------------------------------------------------------------
// Cursor tie-break overlap backoff (fix #3)
// ---------------------------------------------------------------------------

// TestFetchTektonRecordsCursorBackoffPreventsDataLoss simulates two
// consecutive runs: the first persists a cursor backed off by 1 second from
// the true max(createTime), and the second (using that backed-off cursor via
// TEKTON_CURSOR) must still include the record that carried the original,
// pre-backoff max createTime. This proves the 1-second overlap window
// prevents the tie-break record from being permanently dropped.
func TestFetchTektonRecordsCursorBackoffPreventsDataLoss(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	logFile := filepath.Join(stubDir, "kubectl.log")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
echo "$@" >> "`+logFile+`"
case "$1" in
  get) exit 1 ;;
  create)
    for arg in "$@"; do
      case "$arg" in
        --from-literal=last_processed_create_time=*)
          VALUE="${arg#--from-literal=last_processed_create_time=}"
          printf 'apiVersion: v1\nkind: ConfigMap\ndata:\n  last_processed_create_time: "%s"\n' "$VALUE"
          ;;
      esac
    done
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	// Run 1 (cold start): the true max createTime in this fixture is
	// pr-2's 2024-01-01T11:05:30Z. The persisted cursor must be backed off
	// by 1 second to 2024-01-01T11:05:29Z.
	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	require.Contains(t, string(logData), "last_processed_create_time=2024-01-01T11:05:29Z",
		"run 1 must persist a cursor backed off by 1 second from the true max")

	// Run 2: use the backed-off cursor persisted by run 1. pr-2, whose
	// createTime equals the *original* (pre-backoff) max, must still be
	// emitted — it is not permanently excluded despite the cursor having
	// already "passed" its timestamp once.
	env2 := baseEnv(server.URL)
	env2["TEKTON_CURSOR"] = "2024-01-01T11:05:29Z"
	out2, err := runFetchTekton(t, env2)
	require.NoError(t, err)

	names := pipelineRunNames(t, out2)
	assert.Contains(t, names, "pr-2",
		"record at the original (pre-backoff) max createTime must not be dropped by the overlap window")
}

// ---------------------------------------------------------------------------
// Best-effort kubectl failure diagnostics (fix #5)
// ---------------------------------------------------------------------------

func TestFetchTektonRecordsCursorReadFailureLogsDiagnostic(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
case "$1" in
  get) exit 1 ;;
  create) cat <<'YAML'
apiVersion: v1
kind: ConfigMap
data:
  last_processed_create_time: "ignored"
YAML
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, stderr, err := runFetchTektonWithStderr(t, env)
	require.NoError(t, err, "read_cursor failure must remain best-effort and non-fatal")

	assert.Contains(t, string(stderr), "could not read cursor ConfigMap",
		"read_cursor failure must be logged to stderr")
}

func TestFetchTektonRecordsCursorWriteFailureLogsDiagnostic(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
case "$1" in
  get) exit 1 ;;
  create) cat <<'YAML'
apiVersion: v1
kind: ConfigMap
data:
  last_processed_create_time: "ignored"
YAML
    ;;
  label) cat ;;
  apply) exit 1 ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, stderr, err := runFetchTektonWithStderr(t, env)
	require.NoError(t, err, "write_cursor failure must remain best-effort and non-fatal")

	assert.Contains(t, string(stderr), "could not persist cursor ConfigMap",
		"write_cursor failure must be logged to stderr")
}

// ---------------------------------------------------------------------------
// Cursor ConfigMap labels (fix #6)
// ---------------------------------------------------------------------------

// TestFetchTektonRecordsCursorWriteIncludesLabel verifies that write_cursor
// pipes the generated ConfigMap through "kubectl label --local" before
// applying it, so the ConfigMap actually sent to the cluster carries the
// standard app.kubernetes.io/name=segment-bridge label used by every other
// manifest under config/base/.
func TestFetchTektonRecordsCursorWriteIncludesLabel(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	appliedFile := filepath.Join(stubDir, "applied.yaml")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
case "$1" in
  get) exit 1 ;;
  create) cat <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: segment-bridge-cursor
data:
  last_processed_create_time: "ignored"
YAML
    ;;
  label)
    cat
    echo "  app.kubernetes.io/name: segment-bridge"
    ;;
  apply) cat > "`+appliedFile+`" ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	applied, err := os.ReadFile(appliedFile)
	require.NoError(t, err)
	assert.Contains(t, string(applied), "app.kubernetes.io/name: segment-bridge",
		"the ConfigMap applied to the cluster must carry the standard segment-bridge label")
}

// ---------------------------------------------------------------------------
// Max pages pagination guard tests
// ---------------------------------------------------------------------------

// TestFetchTektonRecordsMaxPagesGuard verifies that the script stops
// fetching after TEKTON_MAX_PAGES pages even when the API keeps returning
// a nextPageToken. The fixture records-page1.json always carries a
// nextPageToken, creating an infinite pagination loop that only the guard
// can break.
func TestFetchTektonRecordsMaxPagesGuard(t *testing.T) {
	absFixture, err := filepath.Abs("testdata/records-page1.json")
	require.NoError(t, err)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		data, err := os.ReadFile(absFixture)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	t.Cleanup(server.Close)

	env := baseEnv(server.URL)
	env["TEKTON_MAX_PAGES"] = "2"
	_, stderr, err := runFetchTektonWithStderr(t, env)
	require.NoError(t, err)

	assert.Equal(t, int32(2), atomic.LoadInt32(&requestCount),
		"script must stop after TEKTON_MAX_PAGES requests")
	assert.Contains(t, string(stderr), "WARN fetch-tekton-records.sh: reached max page limit",
		"max pages guard must emit WARN on stderr")
	assert.Contains(t, string(stderr), "(2)",
		"WARN must include the limit value")
}

// ---------------------------------------------------------------------------
// HTTP error handling tests (curl --fail)
// ---------------------------------------------------------------------------

// TestFetchTektonRecordsHTTPError verifies that an HTTP error response
// (e.g. 500) causes the script to exit non-zero with a clear ERROR
// diagnostic on stderr, thanks to curl --fail.
func TestFetchTektonRecordsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	_, stderr, err := runFetchTektonWithStderr(t, baseEnv(server.URL))
	require.Error(t, err, "HTTP 500 must cause script to exit non-zero")
	assert.Contains(t, string(stderr),
		"ERROR fetch-tekton-records.sh: Tekton Results API request failed",
		"HTTP error must produce ERROR diagnostic on stderr")
}

// TestFetchTektonRecordsHTTPForbidden verifies that HTTP 403 also triggers
// curl --fail and the script's error handling.
func TestFetchTektonRecordsHTTPForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	_, stderr, err := runFetchTektonWithStderr(t, baseEnv(server.URL))
	require.Error(t, err, "HTTP 403 must cause script to exit non-zero")
	assert.Contains(t, string(stderr),
		"ERROR fetch-tekton-records.sh: Tekton Results API request failed",
		"HTTP 403 must produce ERROR diagnostic on stderr")
}
