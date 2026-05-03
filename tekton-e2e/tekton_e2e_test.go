//go:build e2e

// Package tektone2e contains end-to-end tests for the Tekton-to-Segment
// pipeline. Tests validate the complete pipeline behavior — namespace
// anonymization, event generation, batch splitting, KPI events, and error
// handling — using mock Tekton Results API responses and a configurable
// mock oc/kubectl binary.
package tektone2e

import (
	"net/http"
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

// setupTestEnv creates a test environment with mock binaries
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	tempDir := t.TempDir()

	mockTknResults := filepath.Join(tempDir, "tkn-results")
	cmd := exec.Command("go", "build", "-o", mockTknResults, "./mock-tkn-results")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to build mock tkn-results: %s", output)

	mockOC := filepath.Join(tempDir, "oc")
	ocScriptPath := "./mock-tkn-results/oc.sh"
	ocScript, err := os.ReadFile(ocScriptPath)
	require.NoError(t, err, "Failed to read oc.sh template")
	require.NoError(t, os.WriteFile(mockOC, ocScript, 0755), "Failed to write mock oc")

	mockKubectl := filepath.Join(tempDir, "kubectl")
	require.NoError(t, os.WriteFile(mockKubectl, ocScript, 0755), "Failed to write mock kubectl")

	responseFile := filepath.Join(tempDir, "response.json")
	netrcFile := filepath.Join(tempDir, "netrc")

	env := &testEnv{
		t:              t,
		tempDir:        tempDir,
		mockTknResults: mockTknResults,
		responseFile:   responseFile,
		netrcFile:      netrcFile,
	}

	return env
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

	// Should have 9 events:
	// - 6 from PipelineRuns (2 per PipelineRun: Started + Completed)
	// - 2 from Konflux operator (Operator Deployment Started + Completed)
	// - 1 Segment Bridge Heartbeat
	assert.Equal(t, 9, len(allEvents), "Expected 9 events (6 from PipelineRuns + 2 from Konflux operator + 1 heartbeat)")

	// Verify event structure
	for _, event := range allEvents {
		assert.Equal(t, "track", event.Type)
		assert.Equal(t, "anonymous", event.AnonymousID)
		assert.NotEmpty(t, event.MessageID)
		assert.NotEmpty(t, event.Timestamp)
		assert.Contains(t, []string{
			"PipelineRun Started", "PipelineRun Completed",
			"Operator Deployment Started", "Operator Deployment Completed",
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

	operatorStartedEvents := filterEvents(allEvents, "Operator Deployment Started")
	operatorCompletedEvents := filterEvents(allEvents, "Operator Deployment Completed")
	assert.Equal(t, 1, len(operatorStartedEvents), "Expected 1 Operator Deployment Started event")
	assert.Equal(t, 1, len(operatorCompletedEvents), "Expected 1 Operator Deployment Completed event")
}

// TestEmptyPipeline tests pipeline with no PipelineRuns
func TestEmptyPipeline(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)

	response, err := CreateTektonResultsResponse([]TektonResultsRecord{})
	require.NoError(t, err)
	env.setMockResponse(response)
	env.createDefaultNetrc()

	// Run pipeline
	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-namespace",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "test-cluster-123",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)

	require.Greater(t, len(requests), 0, "Expected at least one request for Konflux operator events")

	allEvents := collectEvents(t, requests)

	assert.Equal(t, 3, len(allEvents), "Expected 3 events (2 from Konflux operator + 1 heartbeat)")
	operatorEvents := filterEvents(allEvents, "Operator Deployment Started")
	assert.Equal(t, 1, len(operatorEvents), "Should have Operator Deployment Started event")
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

// TestMissingAuthToken tests pipeline behavior when auth token is missing.
// The pipeline uses `set +e`, so fetch-tekton-records fails but the overall
// script continues; operator and heartbeat events are still emitted.
// The pipeline uses 'set +e' so fetch-tekton-records.sh can fail gracefully while
// other data sources (operator, namespace, component records) still succeed
func TestMissingAuthToken(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)
	env.createDefaultNetrc()

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE": "test-namespace",
		// TEKTON_RESULTS_TOKEN intentionally not set
		"SA_TOKEN_PATH":           "/nonexistent/path/token",
		"CLUSTER_ID":              "test-cluster-123",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err, "Pipeline should continue despite missing token")

	allEvents := collectEvents(t, requests)

	// Should have 3 events (no PipelineRuns due to auth failure):
	// - 2 from Konflux operator
	// - 1 Segment Bridge Heartbeat
	assert.Equal(t, 3, len(allEvents), "Expected 3 events (2 from operator + 1 heartbeat, no PipelineRuns)")

	// Verify no PipelineRun events were emitted
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

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-namespace",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "test-cluster-123",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)

	allEvents := collectEvents(t, requests)

	// Should have 5 events:
	// - 2 from 1 PipelineRun (TaskRuns filtered out)
	// - 2 from Konflux operator
	// - 1 Segment Bridge Heartbeat
	assert.Equal(t, 5, len(allEvents), "Expected 5 events (2 from PipelineRun + 2 from operator + 1 heartbeat)")

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

	// 40 from PipelineRuns (2 per run × 20 runs) + 2 from Konflux operator + 1 heartbeat
	assert.Equal(t, 43, len(allEvents), "All events should be delivered across batches")

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
