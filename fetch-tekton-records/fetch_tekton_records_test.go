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
func baseEnv(serverURL string) map[string]string {
	return map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": serverURL,
	}
}

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
