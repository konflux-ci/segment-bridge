//go:build e2e

package tektone2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/scripts"
	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/redhat-appstudio/segment-bridge.git/webfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testEnv holds the test environment setup
type testEnv struct {
	t              *testing.T
	tempDir        string
	mockTknResults string
	responseFile   string
	netrcFile      string
}

// mockOCConfig customises the mock oc/kubectl binary written by configureMockOC.
// The zero value produces an empty Konflux CR ({}) and empty tenant/component lists,
// matching the same default as the e2e/ suite.
type mockOCConfig struct {
	// PublicInfoInner is the JSON object stored in info.json of the
	// konflux-public-info ConfigMap. Defaults to test version strings.
	PublicInfoInner string
	// KubeSystemUID, if set, is returned as the kube-system namespace UID
	// instead of $CLUSTER_ID.
	KubeSystemUID string
	// EmptyKubeSystemUID writes an empty kube-system UID file, simulating a
	// missing cluster identity when CLUSTER_ID is also unset.
	EmptyKubeSystemUID bool
	// KonfluxCRJSON is the raw JSON returned for `oc get konfluxes`. Defaults
	// to "{}" (no operator deployment events).
	KonfluxCRJSON string
	// NamespacesJSON is the raw JSON returned for tenant namespace listings.
	// Defaults to {"items":[]}.
	NamespacesJSON string
	// ComponentsJSON is the raw JSON returned for Component listings.
	// Defaults to {"items":[]}.
	ComponentsJSON string
	// FailKonfluxGet makes the Konflux CR fetch exit 1, simulating a
	// fetch-konflux-op-records failure.
	FailKonfluxGet bool
}

// configureMockOC writes the mock oc/kubectl script and supporting config files
// into the test's temporary directory. Must be called before runPipeline.
func (e *testEnv) configureMockOC(cfg mockOCConfig) {
	e.t.Helper()

	inner := cfg.PublicInfoInner
	if inner == "" {
		inner = `{"konfluxVersion":"test","kubernetesVersion":"test"}`
	}
	cmJSON, err := json.Marshal(map[string]map[string]string{
		"data": {"info.json": inner},
	})
	require.NoError(e.t, err)
	require.NoError(e.t, os.WriteFile(filepath.Join(e.tempDir, "configmap-konflux-public-info.json"), cmJSON, 0600))

	konfluxCR := cfg.KonfluxCRJSON
	if konfluxCR == "" {
		konfluxCR = "{}"
	}
	require.NoError(e.t, os.WriteFile(filepath.Join(e.tempDir, "konflux-cr.json"), []byte(konfluxCR), 0600))

	nsList := cfg.NamespacesJSON
	if nsList == "" {
		nsList = `{"items":[]}`
	}
	require.NoError(e.t, os.WriteFile(filepath.Join(e.tempDir, "namespaces.json"), []byte(nsList), 0600))

	compList := cfg.ComponentsJSON
	if compList == "" {
		compList = `{"items":[]}`
	}
	require.NoError(e.t, os.WriteFile(filepath.Join(e.tempDir, "components.json"), []byte(compList), 0600))

	switch {
	case cfg.EmptyKubeSystemUID:
		require.NoError(e.t, os.WriteFile(filepath.Join(e.tempDir, "kube-system-uid"), []byte{}, 0600))
	case cfg.KubeSystemUID != "":
		require.NoError(e.t, os.WriteFile(filepath.Join(e.tempDir, "kube-system-uid"), []byte(cfg.KubeSystemUID), 0600))
	}

	if cfg.FailKonfluxGet {
		require.NoError(e.t, os.WriteFile(filepath.Join(e.tempDir, "FAIL_KONFLUX"), []byte("1"), 0600))
	}

	ocScript := `#!/usr/bin/env bash
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "$*" in
  *"get namespace kube-system"*)
    if [[ -f "$DIR/kube-system-uid" ]]; then
      cat "$DIR/kube-system-uid"
    else
      printf '%s' "${CLUSTER_ID:-}"
    fi
    ;;
  *"get configmap konflux-public-info"*)
    cat "$DIR/configmap-konflux-public-info.json"
    ;;
  *"get"*"konfluxes"*)
    if [[ -f "$DIR/FAIL_KONFLUX" ]]; then
      echo "mock oc: simulated konflux operator fetch failure" >&2
      exit 1
    fi
    cat "$DIR/konflux-cr.json"
    ;;
  *"get ns"*)
    cat "$DIR/namespaces.json"
    ;;
  *"get components.appstudio.redhat.com"*)
    cat "$DIR/components.json"
    ;;
  *)
    echo "mock oc: unexpected: $*" >&2
    exit 1
    ;;
esac
`
	require.NoError(e.t, os.WriteFile(filepath.Join(e.tempDir, "oc"), []byte(ocScript), 0755))
	require.NoError(e.t, os.WriteFile(filepath.Join(e.tempDir, "kubectl"), []byte(ocScript), 0755))
}

// setupTestEnv creates a test environment with the mock tkn-results binary.
// Call configureMockOC before running the pipeline to set up the oc/kubectl mock.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	tempDir := t.TempDir()

	mockTknResults := filepath.Join(tempDir, "tkn-results")
	cmd := exec.Command("go", "build", "-o", mockTknResults, "./mock-tkn-results")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to build mock tkn-results: %s", output)

	responseFile := filepath.Join(tempDir, "response.json")
	netrcFile := filepath.Join(tempDir, "netrc")

	return &testEnv{
		t:              t,
		tempDir:        tempDir,
		mockTknResults: mockTknResults,
		responseFile:   responseFile,
		netrcFile:      netrcFile,
	}
}

// setMockResponse writes a Tekton Results API response to the mock response file
func (e *testEnv) setMockResponse(response string) {
	err := os.WriteFile(e.responseFile, []byte(response), 0644)
	require.NoError(e.t, err, "Failed to write mock response file")
}

// createDefaultNetrc creates a default netrc file for testing
func (e *testEnv) createDefaultNetrc() {
	err := os.WriteFile(e.netrcFile, []byte("machine localhost login test password test\n"), 0600)
	require.NoError(e.t, err, "Failed to create netrc file")
}

// buildPipelineCommand creates a configured exec.Cmd for running the pipeline
func (e *testEnv) buildPipelineCommand(envVars map[string]string) (*exec.Cmd, error) {
	scriptPath, err := scripts.LookPath("tekton-main-job.sh")
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(scriptPath)
	cmd.Env = os.Environ()

	oldPath := os.Getenv("PATH")
	cmd.Env = append(cmd.Env, "PATH="+e.tempDir+":"+oldPath)
	cmd.Env = append(cmd.Env, "MOCK_TKN_RESULTS_RESPONSE_FILE="+e.responseFile)
	cmd.Env = append(cmd.Env, "KUBECTL=oc")

	for k, v := range envVars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	return cmd, nil
}

// runPipeline executes the full Tekton pipeline and returns captured Segment requests.
// The returned error is the pipeline's exit error (nil on success).
func (e *testEnv) runPipeline(envVars map[string]string) ([]webfixture.RequestTrace, error) {
	var pipelineErr error

	requests := webfixture.TraceRequestsFrom(func(url string, _ *http.Client) {
		envWithSegment := make(map[string]string)
		for k, v := range envVars {
			envWithSegment[k] = v
		}
		envWithSegment["SEGMENT_BATCH_API"] = url + "/v1/batch"
		envWithSegment["CURL_NETRC"] = e.netrcFile

		cmd, err := e.buildPipelineCommand(envWithSegment)
		if err != nil {
			e.t.Fatalf("Failed to build pipeline command: %v", err)
		}

		output, err := cmd.CombinedOutput()
		if err != nil {
			e.t.Logf("Pipeline output: %s", string(output))
		}
		pipelineErr = err
	})

	return requests, pipelineErr
}

// runPipelineExpectError executes the pipeline expecting it to fail
func (e *testEnv) runPipelineExpectError(envVars map[string]string) (string, int) {
	cmd, err := e.buildPipelineCommand(envVars)
	require.NoError(e.t, err, "Failed to build pipeline command")

	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	return string(output), exitCode
}

// requireTools verifies that required external tools are available
func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "curl", "jq", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("Required tool %q not found in PATH", tool)
		}
	}
	if _, err := exec.LookPath("split"); err != nil {
		t.Fatal("Required tool \"split\" not found in PATH")
	}
	if err := exec.Command("split", "--version").Run(); err != nil {
		t.Fatal("GNU coreutils split required (need --line-bytes and --filter support)")
	}
}

// TestHappyPath tests the complete pipeline with multiple PipelineRuns
func TestHappyPath(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)

	pr1 := NewPipelineRun("build-pipeline-1", "user-ns-1", "uid-111").
		WithLabels(map[string]string{
			"tekton.dev/pipeline":                   "build-pipeline",
			"pipelines.appstudio.openshift.io/type": "build",
		}).
		WithStatus("Succeeded").
		WithChildReferences(3)

	pr2 := NewPipelineRun("test-pipeline-2", "user-ns-2", "uid-222").
		WithLabels(map[string]string{
			"tekton.dev/pipeline": "test-pipeline",
		}).
		WithStatus("Failed")

	pr3 := NewPipelineRun("deploy-pipeline-3", "user-ns-1", "uid-333").
		WithStatus("Succeeded")

	rec1, err := EncodeTektonResultsRecord(pr1, "user-ns-1/results/1/records/1")
	require.NoError(t, err)
	rec2, err := EncodeTektonResultsRecord(pr2, "user-ns-2/results/2/records/2")
	require.NoError(t, err)
	rec3, err := EncodeTektonResultsRecord(pr3, "user-ns-1/results/3/records/3")
	require.NoError(t, err)

	response, err := CreateTektonResultsResponse([]TektonResultsRecord{rec1, rec2, rec3})
	require.NoError(t, err)
	env.setMockResponse(response)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{})

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-namespace",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "test-cluster-123",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)
	require.Greater(t, len(requests), 0, "Expected at least one Segment API request")

	for _, req := range requests {
		assert.Equal(t, "POST", req.Method)
		assert.Equal(t, "/v1/batch", req.Path)
	}

	allEvents := collectEvents(t, requests)

	// 6 from PipelineRuns (2 per PipelineRun: Started + Completed) + 1 heartbeat
	assert.Equal(t, 7, len(allEvents), "Expected 7 events (6 from PipelineRuns + 1 heartbeat)")

	// Verify event structure
	for _, event := range allEvents {
		assert.Equal(t, "track", event.Type)
		assert.Equal(t, "anonymous", event.AnonymousID)
		assert.NotEmpty(t, event.MessageID)
		assert.NotEmpty(t, event.Timestamp)
		assert.Contains(t, []string{
			"PipelineRun Started", "PipelineRun Completed",
			"Segment Bridge Heartbeat",
		}, event.Event)
		assert.NotNil(t, event.Properties)
		assert.NotNil(t, event.Context)

		library, ok := event.Context["library"].(map[string]interface{})
		require.True(t, ok, "Context should have library field")
		assert.Equal(t, "segment-bridge", library["name"])
		assert.Equal(t, "2.0.0", library["version"])

		// Heartbeat events don't have namespaceHash
		if event.Event != "Segment Bridge Heartbeat" {
			_, hasNamespaceHash := event.Properties["namespaceHash"]
			assert.True(t, hasNamespaceHash, "Properties should have namespaceHash")
		}
	}

	pipelineStartedEvents := filterEvents(allEvents, "PipelineRun Started")
	pipelineCompletedEvents := filterEvents(allEvents, "PipelineRun Completed")
	assert.Equal(t, 3, len(pipelineStartedEvents), "Expected 3 PipelineRun Started events")
	assert.Equal(t, 3, len(pipelineCompletedEvents), "Expected 3 PipelineRun Completed events")

	statuses := extractStatuses(pipelineCompletedEvents)
	assert.Contains(t, statuses, "Succeeded")
	assert.Contains(t, statuses, "Failed")
}

// TestEmptyPipeline tests pipeline with no PipelineRuns
func TestEmptyPipeline(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)

	response, err := CreateTektonResultsResponse([]TektonResultsRecord{})
	require.NoError(t, err)
	env.setMockResponse(response)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{})

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-namespace",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "test-cluster-123",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)

	require.Greater(t, len(requests), 0, "Expected at least one Segment API request")

	allEvents := collectEvents(t, requests)

	assert.Equal(t, 1, len(allEvents), "Expected 1 event (heartbeat only)")
	testfixture.AssertSegmentHeartbeat(t, allEvents, "test-cluster-123", "test", "test")
}

// TestDefaultTektonNamespace tests that pipeline uses default TEKTON_NAMESPACE
func TestDefaultTektonNamespace(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)

	pr1 := NewPipelineRun("pipeline-1", "user-ns", "uid-111").WithStatus("Succeeded")

	rec1, err := EncodeTektonResultsRecord(pr1, "user-ns/results/1/records/1")
	require.NoError(t, err)

	response, err := CreateTektonResultsResponse([]TektonResultsRecord{rec1})
	require.NoError(t, err)
	env.setMockResponse(response)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{})

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "test-cluster-123",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)
	require.Greater(t, len(requests), 0, "Expected at least one Segment API request")

	allEvents := collectEvents(t, requests)

	pipelineEvents := filterEvents(allEvents, "PipelineRun Started")
	assert.Equal(t, 1, len(pipelineEvents), "Should have 1 PipelineRun Started event")
}

// TestMissingAuthToken tests that the pipeline continues despite a missing
// authentication token. fetch-tekton-records.sh runs under set +e, so its
// failure is non-fatal and the remaining fetchers (operator, namespace,
// component) still run. The old e2e/ suite had a contradicting test that
// expected a hard failure here; that test was incorrect and has been removed.
func TestMissingAuthToken(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{})

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE": "test-namespace",
		// TEKTON_RESULTS_TOKEN intentionally not set
		"SA_TOKEN_PATH":           "/nonexistent/path/token",
		"CLUSTER_ID":              "test-cluster-123",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	// The pipeline continues because fetch-tekton-records.sh runs under set +e;
	// other fetchers (operator, namespace, component) still run and succeed.
	require.NoError(t, err, "Pipeline should continue despite missing token")

	allEvents := collectEvents(t, requests)

	// Only the heartbeat is emitted (no PipelineRuns due to auth failure,
	// no operator events since the default Konflux CR is {}).
	assert.Equal(t, 1, len(allEvents), "Expected 1 event (heartbeat only)")
	pipelineEvents := filterEvents(allEvents, "PipelineRun Started")
	assert.Equal(t, 0, len(pipelineEvents), "Should have no PipelineRun events due to missing token")
}

// TestTaskRunFiltering tests that TaskRuns are filtered out
func TestTaskRunFiltering(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)

	pr1 := NewPipelineRun("pipeline-1", "user-ns", "uid-111").WithStatus("Succeeded")
	tr1 := NewTaskRun("task-1", "user-ns", "uid-222")
	tr2 := NewTaskRun("task-2", "user-ns", "uid-333")

	rec1, err := EncodeTektonResultsRecord(pr1, "user-ns/results/1/records/1")
	require.NoError(t, err)
	rec2, err := EncodeTektonResultsRecord(tr1, "user-ns/results/1/records/2")
	require.NoError(t, err)
	rec3, err := EncodeTektonResultsRecord(tr2, "user-ns/results/1/records/3")
	require.NoError(t, err)

	response, err := CreateTektonResultsResponse([]TektonResultsRecord{rec1, rec2, rec3})
	require.NoError(t, err)
	env.setMockResponse(response)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{})

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-namespace",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "test-cluster-123",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)

	allEvents := collectEvents(t, requests)

	// 2 from the 1 PipelineRun (TaskRuns filtered out) + 1 heartbeat
	assert.Equal(t, 3, len(allEvents), "Expected 3 events (2 from PipelineRun + 1 heartbeat)")

	pipelineEvents := filterEvents(allEvents, "PipelineRun Started")
	assert.Equal(t, 1, len(pipelineEvents), "Should have 1 PipelineRun Started event")
}

// TestBatchSplitting tests that large datasets are split into multiple batches
func TestBatchSplitting(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)

	var records []TektonResultsRecord
	for i := 0; i < 20; i++ {
		pr := NewPipelineRun(
			"pipeline-"+string(rune('a'+i)),
			"user-ns",
			"uid-"+string(rune('a'+i)),
		).WithStatus("Succeeded")

		rec, err := EncodeTektonResultsRecord(pr, "user-ns/results/"+string(rune('a'+i))+"/records/1")
		require.NoError(t, err)
		records = append(records, rec)
	}

	response, err := CreateTektonResultsResponse(records)
	require.NoError(t, err)
	env.setMockResponse(response)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{})

	const testBatchSizeLimit = 5000 // small enough to force splitting across 20 runs
	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-namespace",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "test-cluster-123",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
		"SEGMENT_BATCH_DATA_SIZE": strconv.Itoa(testBatchSizeLimit),
	})
	require.NoError(t, err)

	assert.Greater(t, len(requests), 1, "Expected multiple batches for large dataset")

	for _, req := range requests {
		assert.LessOrEqual(t, len(req.Body), testBatchSizeLimit, "Each batch should be within size limit")
	}

	allEvents := collectEvents(t, requests)

	// 40 from PipelineRuns (2 per run × 20 runs) + 1 heartbeat
	assert.Equal(t, 41, len(allEvents), "All events should be delivered across batches")

	pipelineEvents := filterEvents(allEvents, "PipelineRun Started")
	assert.Equal(t, 20, len(pipelineEvents), "Should have 20 PipelineRun Started events")
}

// TestNamespaceAnonymization tests that namespaces are hashed correctly
func TestNamespaceAnonymization(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)

	pr1 := NewPipelineRun("pipeline-1", "namespace-alpha", "uid-111").WithStatus("Succeeded")
	pr2 := NewPipelineRun("pipeline-2", "namespace-beta", "uid-222").WithStatus("Succeeded")
	pr3 := NewPipelineRun("pipeline-3", "namespace-alpha", "uid-333").WithStatus("Succeeded")

	rec1, err := EncodeTektonResultsRecord(pr1, "namespace-alpha/results/1/records/1")
	require.NoError(t, err)
	rec2, err := EncodeTektonResultsRecord(pr2, "namespace-beta/results/2/records/2")
	require.NoError(t, err)
	rec3, err := EncodeTektonResultsRecord(pr3, "namespace-alpha/results/3/records/3")
	require.NoError(t, err)

	response, err := CreateTektonResultsResponse([]TektonResultsRecord{rec1, rec2, rec3})
	require.NoError(t, err)
	env.setMockResponse(response)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{})

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-namespace",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "test-cluster-456",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)

	const clusterID = "test-cluster-456"
	// Literal SHA256("namespace-alpha:test-cluster-456")[:12] and
	// SHA256("namespace-beta:test-cluster-456")[:12].
	// Using literals (not testfixture.ComputeNamespaceHash) so that a change
	// to the hashing algorithm is caught even if the test helper is updated
	// at the same time.
	const wantAlphaHash = "552b8f46a651"
	const wantBetaHash = "c5f05982e046"
	// Cross-check: verify the constants match the current algorithm at test
	// time, making them self-documenting and easy to update.
	require.Equal(t, wantAlphaHash, testfixture.ComputeNamespaceHash("namespace-alpha", clusterID),
		"wantAlphaHash literal is out of date — recompute with sha256sum")
	require.Equal(t, wantBetaHash, testfixture.ComputeNamespaceHash("namespace-beta", clusterID),
		"wantBetaHash literal is out of date — recompute with sha256sum")
	wantClusterIDHash := testfixture.ComputeClusterIDHash(clusterID)

	allEvents := collectEvents(t, requests)

	for _, req := range requests {
		assert.NotContains(t, req.Body, "namespace-alpha",
			"Raw namespace string should not appear in Segment events")
		assert.NotContains(t, req.Body, "namespace-beta",
			"Raw namespace string should not appear in Segment events")
	}

	pipelineEvents := append(
		filterEvents(allEvents, "PipelineRun Started"),
		filterEvents(allEvents, "PipelineRun Completed")...,
	)

	namespaceHashes := make(map[string]int)
	for _, event := range pipelineEvents {
		if hash, ok := event.Properties["namespaceHash"].(string); ok {
			namespaceHashes[hash]++
		}
		// clusterIdHash must be present and correct on every pipeline event
		assert.Equal(t, wantClusterIDHash, event.Properties["clusterIdHash"],
			"clusterIdHash must match SHA256(clusterID)[:12]")
	}

	// Should have exactly 2 unique hashes (one for each namespace)
	assert.Equal(t, 2, len(namespaceHashes), "Should have 2 unique namespace hashes")

	// Assert specific precomputed values so a hash-algorithm change is caught
	assert.Equal(t, 4, namespaceHashes[wantAlphaHash],
		"namespace-alpha (2 runs × 2 events) should map to the correct SHA256 hash")
	assert.Equal(t, 2, namespaceHashes[wantBetaHash],
		"namespace-beta (1 run × 2 events) should map to the correct SHA256 hash")

	// Sanity-check length (12 hex chars from SHA256 truncation)
	assert.Equal(t, 12, len(wantAlphaHash), "Namespace hash should be 12 characters")
	assert.Equal(t, 12, len(wantBetaHash), "Namespace hash should be 12 characters")
}

// startSegmentServerWithStatus returns the URL of an httptest server that always
// responds to /v1/batch with the given HTTP status code.
func startSegmentServerWithStatus(t *testing.T, httpStatus int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/batch" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(httpStatus)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// runPipelineWithOutput behaves like runPipeline but also returns the combined
// stdout+stderr of the pipeline process.
func (e *testEnv) runPipelineWithOutput(envVars map[string]string) ([]webfixture.RequestTrace, string, error) {
	var pipelineErr error
	var pipelineOutput string
	requests := webfixture.TraceRequestsFrom(func(url string, _ *http.Client) {
		envWithSegment := make(map[string]string)
		for k, v := range envVars {
			envWithSegment[k] = v
		}
		envWithSegment["SEGMENT_BATCH_API"] = url + "/v1/batch"
		envWithSegment["CURL_NETRC"] = e.netrcFile
		cmd, err := e.buildPipelineCommand(envWithSegment)
		if err != nil {
			e.t.Fatalf("Failed to build pipeline command: %v", err)
		}
		out, err := cmd.CombinedOutput()
		pipelineOutput = string(out)
		if err != nil {
			e.t.Logf("Pipeline output: %s", pipelineOutput)
		}
		pipelineErr = err
	})
	return requests, pipelineOutput, pipelineErr
}

// TestTektonPipelineE2EDatasourceKPIs tests KPI event emission from the operator
// CR, tenant namespace, and Component data sources.
func TestTektonPipelineE2EDatasourceKPIs(t *testing.T) {
	const kpiNow = "2026-04-15T14:00:00Z"
	const kpiCreated = "2026-04-15T12:00:00Z"

	t.Run("Konflux operator CR emits operator deployment events", func(t *testing.T) {
		const clusterID = "cluster-kpi-konflux"
		konfluxJSON := `{"apiVersion":"konflux.konflux-ci.dev/v1alpha1","kind":"Konflux","metadata":{"uid":"kpi-konflux-uid","name":"konflux","namespace":"konflux-system","creationTimestamp":"2026-02-17T14:42:19Z"},"status":{"conditions":[{"type":"Ready","status":"True","reason":"AllComponentsReady","lastTransitionTime":"2026-03-03T08:43:48Z"}]}}`

		requireTools(t)
		env := setupTestEnv(t)
		env.createDefaultNetrc()
		env.configureMockOC(mockOCConfig{KonfluxCRJSON: konfluxJSON})

		response, err := CreateTektonResultsResponse([]TektonResultsRecord{})
		require.NoError(t, err)
		env.setMockResponse(response)

		requests, err := env.runPipeline(map[string]string{
			"TEKTON_RESULTS_TOKEN":    "fake-token",
			"CLUSTER_ID":              clusterID,
			"SEGMENT_WRITE_KEY":       "fake-key",
			"TEKTON_RESULTS_API_ADDR": "localhost:50051",
		})
		require.NoError(t, err)

		events := collectEvents(t, requests)
		require.Len(t, events, 3) // 2 operator deployment events + 1 heartbeat
		testfixture.AssertKonfluxOperatorEvents(t, events, "kpi-konflux-uid", "konflux-system", clusterID)
		testfixture.AssertSegmentHeartbeat(t, events, clusterID, "test", "test")
	})

	t.Run("tenant Namespace records emit Namespace Created", func(t *testing.T) {
		const clusterID = "cluster-kpi-namespace"
		nsList := `{"items":[{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"tenant-kpi-ns","uid":"kpi-ns-uid","creationTimestamp":"` + kpiCreated + `","labels":{"konflux-ci.dev/type":"tenant"}}}]}`

		requireTools(t)
		env := setupTestEnv(t)
		env.createDefaultNetrc()
		env.configureMockOC(mockOCConfig{NamespacesJSON: nsList})

		response, err := CreateTektonResultsResponse([]TektonResultsRecord{})
		require.NoError(t, err)
		env.setMockResponse(response)

		requests, err := env.runPipeline(map[string]string{
			"TEKTON_RESULTS_TOKEN":    "fake-token",
			"CLUSTER_ID":              clusterID,
			"SEGMENT_WRITE_KEY":       "fake-key",
			"TEKTON_RESULTS_API_ADDR": "localhost:50051",
			"NAMESPACE_NOW_ISO":       kpiNow,
		})
		require.NoError(t, err)

		events := collectEvents(t, requests)
		require.Len(t, events, 2) // 1 Namespace Created KPI + 1 heartbeat
		testfixture.AssertNamespaceCreatedEvent(t, events, "kpi-ns-uid", "tenant-kpi-ns", clusterID)
		testfixture.AssertSegmentHeartbeat(t, events, clusterID, "test", "test")
	})

	t.Run("Component records emit Component Created", func(t *testing.T) {
		const clusterID = "cluster-kpi-component"
		compList := `{"items":[{"apiVersion":"appstudio.redhat.com/v1alpha1","kind":"Component","metadata":{"name":"my-comp","namespace":"tenant-kpi-c","uid":"kpi-comp-uid","creationTimestamp":"` + kpiCreated + `"},"spec":{"application":"my-app"}}]}`

		requireTools(t)
		env := setupTestEnv(t)
		env.createDefaultNetrc()
		env.configureMockOC(mockOCConfig{ComponentsJSON: compList})

		response, err := CreateTektonResultsResponse([]TektonResultsRecord{})
		require.NoError(t, err)
		env.setMockResponse(response)

		requests, err := env.runPipeline(map[string]string{
			"TEKTON_RESULTS_TOKEN":    "fake-token",
			"CLUSTER_ID":              clusterID,
			"SEGMENT_WRITE_KEY":       "fake-key",
			"TEKTON_RESULTS_API_ADDR": "localhost:50051",
			"COMPONENT_NOW_ISO":       kpiNow,
		})
		require.NoError(t, err)

		events := collectEvents(t, requests)
		require.Len(t, events, 2) // 1 Component Created KPI + 1 heartbeat
		testfixture.AssertComponentCreatedEvent(t, events, "kpi-comp-uid", "my-comp", "tenant-kpi-c", "my-app", clusterID)
		testfixture.AssertSegmentHeartbeat(t, events, clusterID, "test", "test")
	})
}

// TestTektonPipelineE2ENoUploadMode verifies the pipeline exits cleanly and
// logs a diagnostic when SEGMENT_WRITE_KEY is not set.
func TestTektonPipelineE2ENoUploadMode(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{})

	response, err := CreateTektonResultsResponse([]TektonResultsRecord{})
	require.NoError(t, err)
	env.setMockResponse(response)

	// SEGMENT_WRITE_KEY intentionally absent; SEGMENT_BATCH_API is provided by
	// runPipelineWithOutput but the script will exit before calling it.
	_, output, err := env.runPipelineWithOutput(map[string]string{
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "cluster-no-upload",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err, "no-upload mode should succeed:\n%s", output)
	assert.Contains(t, output, "No SEGMENT_WRITE_KEY configured")
}

// TestTektonPipelineE2EClusterIdentityFallback verifies that the pipeline
// continues anonymously when neither CLUSTER_ID nor the kube-system UID is
// available.
func TestTektonPipelineE2EClusterIdentityFallback(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{EmptyKubeSystemUID: true})

	response, err := CreateTektonResultsResponse([]TektonResultsRecord{})
	require.NoError(t, err)
	env.setMockResponse(response)

	// CLUSTER_ID intentionally absent; kube-system UID is also empty.
	requests, output, err := env.runPipelineWithOutput(map[string]string{
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"SEGMENT_WRITE_KEY":       "fake-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err, "pipeline should succeed when CLUSTER_ID cannot be resolved:\n%s", output)
	assert.Contains(t, output, "could not read kube-system UID")

	events := collectEvents(t, requests)
	require.Len(t, events, 1)
	testfixture.AssertSegmentHeartbeat(t, events, "anonymous", "test", "test")
}

// TestMalformedTknResults verifies the pipeline tolerates malformed tkn-results
// output and still emits a heartbeat.
func TestMalformedTknResults(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{})
	env.setMockResponse("not-json{[[[")

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-ns",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "cluster-bad-tkn",
		"SEGMENT_WRITE_KEY":       "fake-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err, "Pipeline should succeed despite malformed tkn-results output")

	events := collectEvents(t, requests)
	require.Len(t, events, 1, "Only a heartbeat should be emitted when tkn-results output is malformed")
	testfixture.AssertSegmentHeartbeat(t, events, "cluster-bad-tkn", "test", "test")
}

// TestKonfluxOperatorFetchFailure verifies that a failure in
// fetch-konflux-op-records does not abort the remaining fetchers.
func TestKonfluxOperatorFetchFailure(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)
	env.createDefaultNetrc()
	env.configureMockOC(mockOCConfig{FailKonfluxGet: true})

	pr := NewPipelineRun("build-run", "tenant-ns", "uid-kfx-fail").WithStatus("Succeeded")
	rec, err := EncodeTektonResultsRecord(pr, "tenant-ns/results/1/records/1")
	require.NoError(t, err)

	response, err := CreateTektonResultsResponse([]TektonResultsRecord{rec})
	require.NoError(t, err)
	env.setMockResponse(response)

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-ns",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "cluster-kfx-fail",
		"SEGMENT_WRITE_KEY":       "fake-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err, "Pipeline should continue when fetch-konflux-op-records fails")

	events := collectEvents(t, requests)
	// 2 PipelineRun events + 1 heartbeat; no operator events since the CR fetch failed
	require.Len(t, events, 3)
	testfixture.AssertSegmentHeartbeat(t, events, "cluster-kfx-fail", "test", "test")
	pipelineEvents := filterEvents(events, "PipelineRun Started")
	assert.Len(t, pipelineEvents, 1)
}

// TestTektonPipelineE2EErrors tests scenarios where the pipeline is expected
// to exit with a non-zero status.
func TestTektonPipelineE2EErrors(t *testing.T) {
	testCases := []struct {
		name              string
		extraEnv          map[string]string
		segmentHTTPStatus int
		wantStderr        string
	}{
		{
			name: "missing kubectl and oc fails",
			extraEnv: map[string]string{
				"KUBECTL": "nonexistent-kubectl-binary",
			},
			wantStderr: "KUBECTL=nonexistent-kubectl-binary not found in PATH",
		},
		{
			name:              "Segment batch API returns HTTP 500",
			segmentHTTPStatus: http.StatusInternalServerError,
			wantStderr:        "500",
		},
		{
			name:              "Segment batch API returns HTTP 429",
			segmentHTTPStatus: http.StatusTooManyRequests,
			wantStderr:        "429",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			requireTools(t)
			env := setupTestEnv(t)
			env.createDefaultNetrc()
			env.configureMockOC(mockOCConfig{})

			response, err := CreateTektonResultsResponse([]TektonResultsRecord{})
			require.NoError(t, err)
			env.setMockResponse(response)

			envVars := map[string]string{
				"TEKTON_NAMESPACE":        "test-ns",
				"TEKTON_RESULTS_TOKEN":    "fake-token",
				"CLUSTER_ID":              "test-cluster",
				"SEGMENT_WRITE_KEY":       "fake-key",
				"SEGMENT_RETRIES":         "0",
				"TEKTON_RESULTS_API_ADDR": "localhost:50051",
				"CURL_NETRC":              env.netrcFile,
			}
			for k, v := range tc.extraEnv {
				envVars[k] = v
			}
			if tc.segmentHTTPStatus != 0 {
				serverURL := startSegmentServerWithStatus(t, tc.segmentHTTPStatus)
				envVars["SEGMENT_BATCH_API"] = serverURL + "/v1/batch"
			}

			output, exitCode := env.runPipelineExpectError(envVars)
			assert.NotEqual(t, 0, exitCode, "Pipeline should have failed")
			assert.Contains(t, output, tc.wantStderr,
				"Expected error message not found in output:\n%s", output)
		})
	}
}

// Helper functions

func collectEvents(t *testing.T, requests []webfixture.RequestTrace) []testfixture.SegmentEvent {
	t.Helper()
	bodies := make([]string, len(requests))
	for i, req := range requests {
		bodies[i] = req.Body
	}
	return testfixture.CollectSegmentEventsFromBodies(t, bodies)
}

func filterEvents(events []testfixture.SegmentEvent, eventName string) []testfixture.SegmentEvent {
	var filtered []testfixture.SegmentEvent
	for _, e := range events {
		if e.Event == eventName {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func extractStatuses(events []testfixture.SegmentEvent) []string {
	var statuses []string
	for _, e := range events {
		if status, ok := e.Properties["status"].(string); ok {
			statuses = append(statuses, status)
		}
	}
	return statuses
}
