//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/scripts"
	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/redhat-appstudio/segment-bridge.git/webfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Use testfixture types for Segment events
type SegmentEvent = testfixture.SegmentEvent
type SegmentBatch = testfixture.SegmentBatch

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

// runPipeline executes the full Tekton pipeline and returns captured Segment requests
func (e *testEnv) runPipeline(envVars map[string]string) ([]webfixture.RequestTrace, error) {
	var requests []webfixture.RequestTrace
	var pipelineErr error

	requests = webfixture.TraceRequestsFrom(func(url string, _ *http.Client) {
		envWithSegment := make(map[string]string)
		for k, v := range envVars {
			envWithSegment[k] = v
		}
		envWithSegment["SEGMENT_BATCH_API"] = url + "/v1/batch"
		envWithSegment["CURL_NETRC"] = e.netrcFile

		cmd, err := e.buildPipelineCommand(envWithSegment)
		if err != nil {
			pipelineErr = err
			e.t.Fatalf("Failed to build pipeline command: %v", err)
		}

		output, err := cmd.CombinedOutput()
		if err != nil {
			e.t.Logf("Pipeline output: %s", output)
			pipelineErr = err
		}
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

	allEvents, err := collectEvents(requests)
	require.NoError(t, err, "Failed to collect events")

	// Should have 7 events:
	// - 6 from PipelineRuns (2 per PipelineRun: Started + Completed)
	// - 1 Segment Bridge Heartbeat
	// Note: No operator events because we don't set up Konflux CR mock
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

	allEvents, err := collectEvents(requests)
	require.NoError(t, err)

	assert.Equal(t, 1, len(allEvents), "Expected 1 event (heartbeat only, no Konflux CR mock)")
	heartbeatEvents := filterEvents(allEvents, "Segment Bridge Heartbeat")
	assert.Equal(t, 1, len(heartbeatEvents), "Should have Segment Bridge Heartbeat event")
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

	allEvents, err := collectEvents(requests)
	require.NoError(t, err)

	pipelineEvents := filterEvents(allEvents, "PipelineRun Started")
	assert.Equal(t, 1, len(pipelineEvents), "Should have 1 PipelineRun Started event")
}

// TestMissingAuthToken tests that pipeline continues despite missing authentication token
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

	allEvents, err := collectEvents(requests)
	require.NoError(t, err)

	// Should have 1 event (no PipelineRuns due to auth failure, no operator events due to no mock):
	// - 1 Segment Bridge Heartbeat
	assert.Equal(t, 1, len(allEvents), "Expected 1 event (heartbeat only, no PipelineRuns)")

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

	allEvents, err := collectEvents(requests)
	require.NoError(t, err)

	// Should have 3 events:
	// - 2 from 1 PipelineRun (TaskRuns filtered out)
	// - 1 Segment Bridge Heartbeat
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

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-namespace",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "test-cluster-123",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
		"SEGMENT_BATCH_DATA_SIZE": "5000", // Small batch size to force splitting
	})
	require.NoError(t, err)

	assert.Greater(t, len(requests), 1, "Expected multiple batches for large dataset")

	for _, req := range requests {
		assert.LessOrEqual(t, len(req.Body), 5000, "Each batch should be within size limit")
	}

	allEvents, err := collectEvents(requests)
	require.NoError(t, err)

	// Should have 41 events total (40 from PipelineRuns + 1 heartbeat)
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

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":        "test-namespace",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "test-cluster-456",
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err)

	allEvents, err := collectEvents(requests)
	require.NoError(t, err)

	for _, req := range requests {
		assert.NotContains(t, req.Body, "namespace-alpha",
			"Raw namespace string should not appear in Segment events")
		assert.NotContains(t, req.Body, "namespace-beta",
			"Raw namespace string should not appear in Segment events")
		assert.NotContains(t, req.Body, "test-cluster-456",
			"Raw cluster ID should not appear in Segment events")
	}

	pipelineEvents := append(
		filterEvents(allEvents, "PipelineRun Started"),
		filterEvents(allEvents, "PipelineRun Completed")...,
	)

	namespaceHashes := make(map[string]int)
	clusterIdHashes := make(map[string]bool)
	for _, event := range pipelineEvents {
		if hash, ok := event.Properties["namespaceHash"].(string); ok {
			namespaceHashes[hash]++
		}
		if hash, ok := event.Properties["clusterIdHash"].(string); ok {
			clusterIdHashes[hash] = true
		}
	}

	// Should have 2 unique hashes (one for each namespace)
	assert.Equal(t, 2, len(namespaceHashes), "Should have 2 unique namespace hashes")

	// namespace-alpha has 2 PipelineRuns (4 events), namespace-beta has 1 PipelineRun (2 events)
	var hashCounts []int
	for _, count := range namespaceHashes {
		hashCounts = append(hashCounts, count)
	}
	assert.Contains(t, hashCounts, 4, "namespace-alpha should have 4 events")
	assert.Contains(t, hashCounts, 2, "namespace-beta should have 2 events")

	// Verify hashes are 12 characters (truncated SHA256)
	for hash := range namespaceHashes {
		assert.Equal(t, 12, len(hash), "Namespace hash should be 12 characters")
	}

	// Verify namespace hash values match the expected SHA256 algorithm
	expectedAlphaHash := testfixture.ComputeNamespaceHash("namespace-alpha", "test-cluster-456")
	expectedBetaHash := testfixture.ComputeNamespaceHash("namespace-beta", "test-cluster-456")
	assert.Contains(t, namespaceHashes, expectedAlphaHash,
		"namespace-alpha hash should match SHA256(namespace-alpha:test-cluster-456)")
	assert.Contains(t, namespaceHashes, expectedBetaHash,
		"namespace-beta hash should match SHA256(namespace-beta:test-cluster-456)")

	// Verify clusterIdHash is present and correctly anonymized
	assert.Equal(t, 1, len(clusterIdHashes), "Should have 1 unique cluster ID hash")
	expectedClusterHash := testfixture.ComputeClusterIDHash("test-cluster-456")
	assert.Contains(t, clusterIdHashes, expectedClusterHash,
		"clusterIdHash should match SHA256(test-cluster-456)")
	assert.Equal(t, 12, len(expectedClusterHash),
		"Cluster ID hash should be 12 characters")
}

// Helper functions

func collectEvents(requests []webfixture.RequestTrace) ([]SegmentEvent, error) {
	var allEvents []SegmentEvent
	for _, req := range requests {
		var batch SegmentBatch
		if err := json.Unmarshal([]byte(req.Body), &batch); err != nil {
			return nil, err
		}
		allEvents = append(allEvents, batch.Batch...)
	}
	return allEvents, nil
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

// TestKPIEvents tests KPI event generation for Konflux datasources
func TestKPIEvents(t *testing.T) {
	const kpiNow = "2026-04-15T14:00:00Z"
	const kpiCreated = "2026-04-15T12:00:00Z"

	t.Run("Konflux operator CR emits operator deployment events", func(t *testing.T) {
		requireTools(t)
		env := setupTestEnv(t)

		// Setup empty tekton results
		env.setMockResponse(buildEmptyTektonResultsResponse())
		env.createDefaultNetrc()

		// Setup Konflux CR
		konfluxCR := NewKonfluxCR("kpi-konflux-uid", "konflux-system", "2026-02-17T14:42:19Z").
			WithReadyCondition("True", "AllComponentsReady", "2026-03-03T08:43:48Z")
		env.setMockKonfluxCR(konfluxCR)

		clusterID := "cluster-kpi-konflux"
		requests, err := env.runPipeline(map[string]string{
			"TEKTON_NAMESPACE":        "test-namespace",
			"TEKTON_RESULTS_TOKEN":    "fake-token",
			"CLUSTER_ID":              clusterID,
			"SEGMENT_WRITE_KEY":       "fake-write-key",
			"TEKTON_RESULTS_API_ADDR": "localhost:50051",
		})
		require.NoError(t, err)

		events := collectSegmentEvents(t, getBodiesFromRequests(requests))
		require.Len(t, events, 3, "Expected 3 events (2 operator + 1 heartbeat)")

		testfixture.AssertKonfluxOperatorEvents(t, events, "kpi-konflux-uid", "konflux-system", clusterID)
		testfixture.AssertSegmentHeartbeat(t, events, clusterID, "test", "test")
	})

	t.Run("tenant Namespace records emit Namespace Created", func(t *testing.T) {
		requireTools(t)
		env := setupTestEnv(t)

		env.setMockResponse(buildEmptyTektonResultsResponse())
		env.createDefaultNetrc()

		// Setup tenant namespace
		ns := NewNamespace("tenant-kpi-ns", "kpi-ns-uid", kpiCreated).
			WithLabels(map[string]string{"konflux-ci.dev/type": "tenant"})
		env.setMockNamespaces([]*Namespace{ns})

		clusterID := "cluster-kpi-namespace"
		requests, err := env.runPipeline(map[string]string{
			"TEKTON_NAMESPACE":        "test-namespace",
			"TEKTON_RESULTS_TOKEN":    "fake-token",
			"CLUSTER_ID":              clusterID,
			"SEGMENT_WRITE_KEY":       "fake-write-key",
			"TEKTON_RESULTS_API_ADDR": "localhost:50051",
			"NAMESPACE_NOW_ISO":       kpiNow,
		})
		require.NoError(t, err)

		events := collectSegmentEvents(t, getBodiesFromRequests(requests))
		require.Len(t, events, 2, "Expected 2 events (1 namespace + 1 heartbeat)")

		testfixture.AssertNamespaceCreatedEvent(t, events, "kpi-ns-uid", "tenant-kpi-ns", clusterID)
		testfixture.AssertSegmentHeartbeat(t, events, clusterID, "test", "test")
	})

	t.Run("Component records emit Component Created", func(t *testing.T) {
		requireTools(t)
		env := setupTestEnv(t)

		env.setMockResponse(buildEmptyTektonResultsResponse())
		env.createDefaultNetrc()

		// Setup component
		comp := NewComponent("my-comp", "tenant-kpi-c", "kpi-comp-uid", kpiCreated, "my-app")
		env.setMockComponents([]*Component{comp})

		clusterID := "cluster-kpi-component"
		requests, err := env.runPipeline(map[string]string{
			"TEKTON_NAMESPACE":        "test-namespace",
			"TEKTON_RESULTS_TOKEN":    "fake-token",
			"CLUSTER_ID":              clusterID,
			"SEGMENT_WRITE_KEY":       "fake-write-key",
			"TEKTON_RESULTS_API_ADDR": "localhost:50051",
			"COMPONENT_NOW_ISO":       kpiNow,
		})
		require.NoError(t, err)

		events := collectSegmentEvents(t, getBodiesFromRequests(requests))
		require.Len(t, events, 2, "Expected 2 events (1 component + 1 heartbeat)")

		testfixture.AssertComponentCreatedEvent(t, events, "kpi-comp-uid", "my-comp", "tenant-kpi-c", "my-app", clusterID)
		testfixture.AssertSegmentHeartbeat(t, events, clusterID, "test", "test")
	})
}

// TestNoUploadMode tests that the pipeline runs successfully without SEGMENT_WRITE_KEY
func TestNoUploadMode(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)

	env.setMockResponse(buildEmptyTektonResultsResponse())
	env.createDefaultNetrc()

	output, exitCode := env.runPipelineExpectError(map[string]string{
		"TEKTON_NAMESPACE":        "test-namespace",
		"TEKTON_RESULTS_TOKEN":    "fake-token",
		"CLUSTER_ID":              "cluster-no-upload",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
		// SEGMENT_WRITE_KEY intentionally not set
	})

	assert.Equal(t, 0, exitCode, "Pipeline should succeed in no-upload mode")
	assert.Contains(t, output, "No SEGMENT_WRITE_KEY configured")
}

// TestClusterIdentityFallback tests that pipeline succeeds when cluster ID cannot be determined
func TestClusterIdentityFallback(t *testing.T) {
	requireTools(t)
	env := setupTestEnv(t)

	env.setMockResponse(buildEmptyTektonResultsResponse())
	env.createDefaultNetrc()
	env.setMockKubeSystemUID("") // Empty UID simulates failure to get cluster ID

	requests, err := env.runPipeline(map[string]string{
		"TEKTON_NAMESPACE":     "test-namespace",
		"TEKTON_RESULTS_TOKEN": "fake-token",
		// CLUSTER_ID intentionally not set - should fall back to anonymous
		"SEGMENT_WRITE_KEY":       "fake-write-key",
		"TEKTON_RESULTS_API_ADDR": "localhost:50051",
	})
	require.NoError(t, err, "Pipeline should succeed when CLUSTER_ID cannot be resolved")

	events := collectSegmentEvents(t, getBodiesFromRequests(requests))
	require.Len(t, events, 1, "Expected 1 heartbeat event")

	testfixture.AssertSegmentHeartbeat(t, events, "anonymous", "test", "test")
}

// TestErrorHandling tests various error conditions
func TestErrorHandling(t *testing.T) {
	testCases := []struct {
		name              string
		setupEnv          func(*testEnv)
		extraEnv          map[string]string
		segmentHTTPStatus int
		wantStderr        string
	}{
		{
			name: "missing kubectl and oc fails",
			setupEnv: func(env *testEnv) {
				env.setMockResponse(buildEmptyTektonResultsResponse())
				env.createDefaultNetrc()
			},
			extraEnv: map[string]string{
				"KUBECTL": "nonexistent-kubectl-binary",
			},
			wantStderr: "not found in PATH",
		},
		{
			name: "Segment batch API returns HTTP 500",
			setupEnv: func(env *testEnv) {
				env.setMockResponse(buildEmptyTektonResultsResponse())
				env.createDefaultNetrc()
			},
			segmentHTTPStatus: http.StatusInternalServerError,
			wantStderr:        "500",
		},
		{
			name: "Segment batch API returns HTTP 429",
			setupEnv: func(env *testEnv) {
				env.setMockResponse(buildEmptyTektonResultsResponse())
				env.createDefaultNetrc()
			},
			segmentHTTPStatus: http.StatusTooManyRequests,
			wantStderr:        "429",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			requireTools(t)
			env := setupTestEnv(t)
			tc.setupEnv(env)

			envVars := map[string]string{
				"TEKTON_NAMESPACE":        "test-namespace",
				"TEKTON_RESULTS_TOKEN":    "fake-token",
				"CLUSTER_ID":              "test-cluster",
				"SEGMENT_WRITE_KEY":       "fake-write-key",
				"TEKTON_RESULTS_API_ADDR": "localhost:50051",
			}

			if tc.segmentHTTPStatus != 0 {
				// Use a server that returns the error status
				serverURL := startSegmentServerWithStatus(t, tc.segmentHTTPStatus)
				envVars["SEGMENT_BATCH_API"] = serverURL + "/v1/batch"
			}

			for k, v := range tc.extraEnv {
				envVars[k] = v
			}

			output, exitCode := env.runPipelineExpectError(envVars)
			assert.NotEqual(t, 0, exitCode, "Pipeline should have failed")
			assert.Contains(t, output, tc.wantStderr,
				"Expected error message not found in output:\n%s", output)
		})
	}
}

// Helper to build empty Tekton Results response
func buildEmptyTektonResultsResponse() string {
	resp, _ := CreateTektonResultsResponse([]TektonResultsRecord{})
	return resp
}

// Helper to extract request bodies from webfixture requests
func getBodiesFromRequests(requests []webfixture.RequestTrace) []string {
	bodies := make([]string, len(requests))
	for i, req := range requests {
		bodies[i] = req.Body
	}
	return bodies
}

// TestTektonPipelineE2EComprehensive tests comprehensive PipelineRun scenarios
func TestTektonPipelineE2EComprehensive(t *testing.T) {
	testCases := []struct {
		name           string
		pipelineRuns   []*PipelineRun
		taskRuns       []*TaskRun
		clusterID      string
		wantEvents     int
		setupMock      func(*testEnv)
		tknBodyRaw     string
		validateEvents func(*testing.T, []testfixture.SegmentEvent, string)
	}{
		{
			name: "single pipelinerun produces started and completed events",
			pipelineRuns: []*PipelineRun{
				NewPipelineRun("build-run-1", "tenant-a", "uid-001").
					WithLabels(map[string]string{
						"tekton.dev/pipeline":                   "docker-build",
						"pipelines.appstudio.openshift.io/type": "build",
					}).
					WithStatus("Completed").
					WithChildReferences(3),
			},
			clusterID:  "cluster-abc",
			wantEvents: 3, // started + completed + heartbeat
		},
		{
			name: "multiple pipelineruns across namespaces",
			pipelineRuns: []*PipelineRun{
				NewPipelineRun("build-a", "ns-alpha", "uid-101").
					WithLabels(map[string]string{
						"tekton.dev/pipeline":                   "docker-build",
						"pipelines.appstudio.openshift.io/type": "build",
					}).
					WithStatus("Completed").
					WithChildReferences(5),
				NewPipelineRun("test-b", "ns-alpha", "uid-102").
					WithLabels(map[string]string{
						"tekton.dev/pipeline":                   "integration-test",
						"pipelines.appstudio.openshift.io/type": "test",
					}).
					WithStatus("Succeeded").
					WithChildReferences(1),
				NewPipelineRun("managed-c", "ns-managed", "uid-103").
					WithLabels(map[string]string{
						"tekton.dev/pipeline":                   "push-to-registry",
						"pipelines.appstudio.openshift.io/type": "managed",
					}).
					WithStatus("Completed").
					WithChildReferences(8),
			},
			clusterID:  "cluster-xyz",
			wantEvents: 7, // 6 from PRs + 1 heartbeat
		},
		{
			name: "taskruns in response are filtered out",
			pipelineRuns: []*PipelineRun{
				NewPipelineRun("build-x", "ns-beta", "uid-201").
					WithLabels(map[string]string{
						"tekton.dev/pipeline":                   "docker-build",
						"pipelines.appstudio.openshift.io/type": "build",
					}).
					WithStatus("Completed").
					WithChildReferences(4),
			},
			taskRuns: []*TaskRun{
				NewTaskRun("taskrun-1", "ns-beta", "uid-tr-1"),
				NewTaskRun("taskrun-2", "ns-beta", "uid-tr-2"),
			},
			clusterID:  "cluster-filter",
			wantEvents: 3, // 2 from PR + 1 heartbeat (TaskRuns filtered)
		},
		{
			name: "failed pipelinerun reports failure status",
			pipelineRuns: []*PipelineRun{
				NewPipelineRun("build-fail", "ns-gamma", "uid-301").
					WithLabels(map[string]string{
						"tekton.dev/pipeline":                   "docker-build",
						"pipelines.appstudio.openshift.io/type": "build",
					}).
					WithStatus("Failed").
					WithChildReferences(2),
			},
			clusterID:  "cluster-fail",
			wantEvents: 3,
			validateEvents: func(t *testing.T, events []testfixture.SegmentEvent, clusterID string) {
				completedEvents := filterEvents(events, "PipelineRun Completed")
				require.Len(t, completedEvents, 1)
				assert.Equal(t, "Failed", completedEvents[0].Properties["status"])
			},
		},
		{
			name: "pipelinerun without pipeline labels",
			pipelineRuns: []*PipelineRun{
				NewPipelineRun("ad-hoc-run", "ns-delta", "uid-401").
					WithStatus("Completed").
					WithChildReferences(1),
			},
			clusterID:  "cluster-nolabel",
			wantEvents: 3,
			validateEvents: func(t *testing.T, events []testfixture.SegmentEvent, clusterID string) {
				startedEvents := filterEvents(events, "PipelineRun Started")
				require.Len(t, startedEvents, 1)
				assert.Equal(t, false, startedEvents[0].Properties["hasPipelineLabel"])
			},
		},
		{
			name:         "empty results produce only heartbeat event",
			pipelineRuns: []*PipelineRun{},
			clusterID:    "cluster-empty",
			wantEvents:   1,
		},
		{
			name:         "malformed tkn-results output is ignored pipeline still succeeds",
			pipelineRuns: []*PipelineRun{},
			clusterID:    "cluster-bad-tkn",
			wantEvents:   1,
			tknBodyRaw:   "not-json{[[[",
		},
		{
			name: "fetch-konflux-op-records failure does not abort pipeline",
			pipelineRuns: []*PipelineRun{
				NewPipelineRun("build-kfx", "tenant-kfx", "uid-kfx-fail").
					WithLabels(map[string]string{
						"tekton.dev/pipeline":                   "docker-build",
						"pipelines.appstudio.openshift.io/type": "build",
					}).
					WithStatus("Completed").
					WithChildReferences(1),
			},
			clusterID:  "cluster-kfx-fail",
			wantEvents: 3, // PR events + heartbeat (no operator events due to failure)
			setupMock: func(env *testEnv) {
				env.setFailKonfluxGet()
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			requireTools(t)
			env := setupTestEnv(t)

			// Build Tekton Results response
			var records []TektonResultsRecord
			for _, pr := range tc.pipelineRuns {
				rec, err := EncodeTektonResultsRecord(pr, pr.Metadata.Namespace+"/results/"+pr.Metadata.UID+"/records/1")
				require.NoError(t, err)
				records = append(records, rec)
			}
			for _, tr := range tc.taskRuns {
				rec, err := EncodeTektonResultsRecord(tr, tr.Metadata.Namespace+"/results/"+tr.Metadata.UID+"/records/1")
				require.NoError(t, err)
				records = append(records, rec)
			}

			if tc.tknBodyRaw != "" {
				// Use raw body (for malformed JSON tests)
				err := os.WriteFile(filepath.Join(env.tempDir, "response.json"), []byte(tc.tknBodyRaw), 0644)
				require.NoError(t, err)
			} else {
				response, err := CreateTektonResultsResponse(records)
				require.NoError(t, err)
				env.setMockResponse(response)
			}

			env.createDefaultNetrc()

			if tc.setupMock != nil {
				tc.setupMock(env)
			}

			requests, err := env.runPipeline(map[string]string{
				"TEKTON_NAMESPACE":        "test-namespace",
				"TEKTON_RESULTS_TOKEN":    "fake-token",
				"CLUSTER_ID":              tc.clusterID,
				"SEGMENT_WRITE_KEY":       "fake-write-key",
				"TEKTON_RESULTS_API_ADDR": "localhost:50051",
			})
			require.NoError(t, err, "Pipeline should succeed")

			events := collectSegmentEvents(t, getBodiesFromRequests(requests))
			assert.Equal(t, tc.wantEvents, len(events), "Event count mismatch")

			if tc.validateEvents != nil {
				tc.validateEvents(t, events, tc.clusterID)
			}
		})
	}
}
