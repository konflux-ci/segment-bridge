//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/scripts"
	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	repoRoot     string
	repoRootOnce sync.Once
	repoRootErr  error
)

type pipelineRunData struct {
	UID            string
	Name           string
	Namespace      string
	PipelineLabel  string
	PipelineType   string
	StartTime      string
	CompletionTime string
	StatusReason   string
	TaskCount      int
}

type tknRecord struct {
	Data struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"data"`
}

type requestCollector struct {
	mu     sync.Mutex
	bodies []string
}

func (rc *requestCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "expected POST method", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusInternalServerError)
		return
	}
	rc.mu.Lock()
	rc.bodies = append(rc.bodies, string(body))
	rc.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"success":true}`)
}

func (rc *requestCollector) getBodies() []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	result := make([]string, len(rc.bodies))
	copy(result, rc.bodies)
	return result
}

func startSegmentServer(t *testing.T) (serverURL string, getBodies func() []string) {
	t.Helper()
	rc := &requestCollector{}
	mux := http.NewServeMux()
	mux.Handle("/v1/batch", rc)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL, rc.getBodies
}

func buildPipelineRunJSON(t *testing.T, pr pipelineRunData) string {
	t.Helper()
	childRefs := make([]map[string]string, pr.TaskCount)
	for i := range childRefs {
		childRefs[i] = map[string]string{
			"name": fmt.Sprintf("task-%d", i+1),
			"kind": "TaskRun",
		}
	}

	labels := map[string]string{}
	if pr.PipelineLabel != "" {
		labels["tekton.dev/pipeline"] = pr.PipelineLabel
	}
	if pr.PipelineType != "" {
		labels["pipelines.appstudio.openshift.io/type"] = pr.PipelineType
	}

	run := map[string]interface{}{
		"apiVersion": "tekton.dev/v1",
		"kind":       "PipelineRun",
		"metadata": map[string]interface{}{
			"name":      pr.Name,
			"namespace": pr.Namespace,
			"uid":       pr.UID,
			"labels":    labels,
		},
		"status": map[string]interface{}{
			"startTime":      pr.StartTime,
			"completionTime": pr.CompletionTime,
			"conditions": []map[string]string{{
				"type":   "Succeeded",
				"status": "True",
				"reason": pr.StatusReason,
			}},
			"childReferences": childRefs,
		},
	}

	data, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("failed to marshal PipelineRun JSON: %v", err)
	}
	return string(data)
}

// buildTknResultsResponse matches the format of `tkn-results records list -o json`
func buildTknResultsResponse(t *testing.T, pipelineRuns []string, taskRuns []string) string {
	t.Helper()
	var records []tknRecord

	for _, pr := range pipelineRuns {
		rec := tknRecord{}
		rec.Data.Type = "tekton.dev/v1.PipelineRun"
		rec.Data.Value = base64.StdEncoding.EncodeToString([]byte(pr))
		records = append(records, rec)
	}
	for _, tr := range taskRuns {
		rec := tknRecord{}
		rec.Data.Type = "tekton.dev/v1.TaskRun"
		rec.Data.Value = base64.StdEncoding.EncodeToString([]byte(tr))
		records = append(records, rec)
	}

	resp := map[string]interface{}{"records": records}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal tkn-results response: %v", err)
	}
	return string(data)
}

func createMockTknResults(t *testing.T, dir, response string) {
	t.Helper()

	respFile := filepath.Join(dir, "response.json")
	require.NoError(t, os.WriteFile(respFile, []byte(response), 0600))

	script := filepath.Join(dir, "tkn-results")
	content := fmt.Sprintf("#!/usr/bin/env bash\ncat %q\n", respFile)
	require.NoError(t, os.WriteFile(script, []byte(content), 0755))
}

func createMockOC(t *testing.T, dir string) {
	t.Helper()

	mockOC := filepath.Join(dir, "oc")
	ocScript := `#!/usr/bin/env bash
case "$*" in
  *"get namespace kube-system"*)
    echo "$CLUSTER_ID"
    ;;
  *"get configmap konflux-public-info"*)
    echo '{"data":{"info.json":"{\"konfluxVersion\":\"test\",\"kubernetesVersion\":\"test\"}"}}'
    ;;
  *"get konfluxes"*)
    echo '{}'
    ;;
  *"get ns"*)
    echo '{"items":[]}'
    ;;
  *"get components.appstudio.redhat.com"*)
    echo '{"items":[]}'
    ;;
  *)
    echo "mock oc: unexpected: $*" >&2
    exit 1
    ;;
esac
`
	require.NoError(t, os.WriteFile(mockOC, []byte(ocScript), 0755))
}

// computeNamespaceHash replicates the SHA256(namespace:cluster_id) logic
// from tekton-to-segment.sh
func computeNamespaceHash(namespace, clusterID string) string {
	h := sha256.Sum256([]byte(namespace + ":" + clusterID))
	return fmt.Sprintf("%x", h)[:12]
}

type pipelineConfig struct {
	MockDir          string
	ServerURL        string
	ClusterID        string
	TektonNamespace  string
	TektonResultsTkn string
	SegmentWriteKey  string
	SegmentRetries   string
	ExtraEnv         map[string]string
}

func newPipelineConfig(mockDir, serverURL, clusterID string) *pipelineConfig {
	return &pipelineConfig{
		MockDir:          mockDir,
		ServerURL:        serverURL,
		ClusterID:        clusterID,
		TektonNamespace:  "test-ns",
		TektonResultsTkn: "dummy-token",
		SegmentWriteKey:  "test-write-key",
		SegmentRetries:   "0",
		ExtraEnv:         make(map[string]string),
	}
}

func (pc *pipelineConfig) envOverrides(scriptsPath string) map[string]string {
	overrides := map[string]string{
		"PATH":                 fmt.Sprintf("%s:%s:%s", pc.MockDir, scriptsPath, os.Getenv("PATH")),
		"KUBECTL":              "oc",
		"TEKTON_NAMESPACE":     pc.TektonNamespace,
		"TEKTON_RESULTS_TOKEN": pc.TektonResultsTkn,
		"CLUSTER_ID":           pc.ClusterID,
		"SEGMENT_BATCH_API":    pc.ServerURL + "/v1/batch",
		"SEGMENT_WRITE_KEY":    pc.SegmentWriteKey,
		"SEGMENT_RETRIES":      pc.SegmentRetries,
	}
	for k, v := range pc.ExtraEnv {
		overrides[k] = v
	}
	return overrides
}

type pipelineResult struct {
	Output   []byte
	ExitCode int
	Err      error
}

func runPipeline(t *testing.T, cfg *pipelineConfig) pipelineResult {
	t.Helper()

	repoRootOnce.Do(func() {
		repoRoot, repoRootErr = scripts.GetRepoRootDir()
	})
	require.NoError(t, repoRootErr, "Failed to resolve repo root")
	scriptsPath := filepath.Join(repoRoot, "scripts")

	cmd := exec.Command(filepath.Join(scriptsPath, "tekton-main-job.sh"))
	cmd.Dir = repoRoot

	overrides := cfg.envOverrides(scriptsPath)
	env := os.Environ()
	applied := make(map[string]bool)
	for i, entry := range env {
		key := strings.SplitN(entry, "=", 2)[0]
		if val, ok := overrides[key]; ok {
			env[i] = key + "=" + val
			applied[key] = true
		}
	}
	for k, v := range overrides {
		if !applied[k] {
			env = append(env, k+"="+v)
		}
	}
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	result := pipelineResult{Output: output, Err: err}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result
}

func collectSegmentEvents(t *testing.T, bodies []string) []testfixture.SegmentEvent {
	t.Helper()
	var events []testfixture.SegmentEvent
	for _, body := range bodies {
		var batch testfixture.SegmentBatch
		require.NoError(t, json.Unmarshal([]byte(body), &batch),
			"Failed to decode batch payload")
		events = append(events, batch.Batch...)
	}
	return events
}

func findEvent(events []testfixture.SegmentEvent, messageID string) *testfixture.SegmentEvent {
	for i := range events {
		if events[i].MessageID == messageID {
			return &events[i]
		}
	}
	return nil
}

func assertPipelineRunEvents(t *testing.T, events []testfixture.SegmentEvent, pr pipelineRunData, clusterID string) {
	t.Helper()

	nsHash := computeNamespaceHash(pr.Namespace, clusterID)

	started := findEvent(events, pr.UID+"-started")
	completed := findEvent(events, pr.UID+"-completed")
	require.NotNil(t, started, "Missing Started event for %s", pr.UID)
	require.NotNil(t, completed, "Missing Completed event for %s", pr.UID)

	assert.Equal(t, "PipelineRun Started", started.Event)
	assert.Equal(t, pr.StartTime, started.Timestamp)
	assert.Equal(t, nsHash, started.Properties["namespaceHash"])
	assert.Equal(t, float64(pr.TaskCount), started.Properties["taskCount"])
	assert.Equal(t, pr.PipelineLabel != "", started.Properties["hasPipelineLabel"])
	if pr.PipelineType != "" {
		assert.Equal(t, pr.PipelineType, started.Properties["pipelineType"])
	}

	assert.Equal(t, "PipelineRun Completed", completed.Event)
	assert.Equal(t, pr.CompletionTime, completed.Timestamp)
	assert.Equal(t, nsHash, completed.Properties["namespaceHash"])
	assert.Equal(t, pr.StatusReason, completed.Properties["status"])
	assert.Equal(t, pr.StartTime, completed.Properties["startTime"])
	assert.Equal(t, pr.CompletionTime, completed.Properties["completionTime"])
	assert.NotNil(t, completed.Properties["durationSeconds"])
}

func TestTektonPipelineE2E(t *testing.T) {
	taskRunJSON := func(uid, name, ns string) string {
		data, _ := json.Marshal(map[string]interface{}{
			"apiVersion": "tekton.dev/v1",
			"kind":       "TaskRun",
			"metadata":   map[string]interface{}{"name": name, "namespace": ns, "uid": uid},
		})
		return string(data)
	}

	testCases := []struct {
		name         string
		pipelineRuns []pipelineRunData
		taskRuns     []string
		clusterID    string
		wantEvents   int
	}{
		{
			name: "single pipelinerun produces started and completed events",
			pipelineRuns: []pipelineRunData{{
				UID: "uid-001", Name: "build-run-1", Namespace: "tenant-a",
				PipelineLabel: "docker-build", PipelineType: "build",
				StartTime: "2026-01-15T10:00:00Z", CompletionTime: "2026-01-15T10:05:00Z",
				StatusReason: "Completed", TaskCount: 3,
			}},
			clusterID:  "cluster-abc",
			wantEvents: 2,
		},
		{
			name: "multiple pipelineruns across namespaces",
			pipelineRuns: []pipelineRunData{
				{
					UID: "uid-101", Name: "build-a", Namespace: "ns-alpha",
					PipelineLabel: "docker-build", PipelineType: "build",
					StartTime: "2026-02-01T08:00:00Z", CompletionTime: "2026-02-01T08:10:00Z",
					StatusReason: "Completed", TaskCount: 5,
				},
				{
					UID: "uid-102", Name: "test-b", Namespace: "ns-alpha",
					PipelineLabel: "integration-test", PipelineType: "test",
					StartTime: "2026-02-01T09:00:00Z", CompletionTime: "2026-02-01T09:01:00Z",
					StatusReason: "Succeeded", TaskCount: 1,
				},
				{
					UID: "uid-103", Name: "managed-c", Namespace: "ns-managed",
					PipelineLabel: "push-to-registry", PipelineType: "managed",
					StartTime: "2026-02-01T10:00:00Z", CompletionTime: "2026-02-01T10:05:00Z",
					StatusReason: "Completed", TaskCount: 8,
				},
			},
			clusterID:  "cluster-xyz",
			wantEvents: 6,
		},
		{
			name: "taskruns in response are filtered out",
			pipelineRuns: []pipelineRunData{{
				UID: "uid-201", Name: "build-x", Namespace: "ns-beta",
				PipelineLabel: "docker-build", PipelineType: "build",
				StartTime: "2026-03-01T12:00:00Z", CompletionTime: "2026-03-01T12:03:00Z",
				StatusReason: "Completed", TaskCount: 4,
			}},
			taskRuns: []string{
				taskRunJSON("uid-tr-1", "taskrun-1", "ns-beta"),
				taskRunJSON("uid-tr-2", "taskrun-2", "ns-beta"),
			},
			clusterID:  "cluster-filter",
			wantEvents: 2,
		},
		{
			name: "failed pipelinerun reports failure status",
			pipelineRuns: []pipelineRunData{{
				UID: "uid-301", Name: "build-fail", Namespace: "ns-gamma",
				PipelineLabel: "docker-build", PipelineType: "build",
				StartTime: "2026-03-05T14:00:00Z", CompletionTime: "2026-03-05T14:01:00Z",
				StatusReason: "Failed", TaskCount: 2,
			}},
			clusterID:  "cluster-fail",
			wantEvents: 2,
		},
		{
			name: "pipelinerun without pipeline labels",
			pipelineRuns: []pipelineRunData{{
				UID: "uid-401", Name: "ad-hoc-run", Namespace: "ns-delta",
				PipelineLabel: "", PipelineType: "",
				StartTime: "2026-03-06T08:00:00Z", CompletionTime: "2026-03-06T08:02:00Z",
				StatusReason: "Completed", TaskCount: 1,
			}},
			clusterID:  "cluster-nolabel",
			wantEvents: 2,
		},
		{
			name:         "empty results produce no events and no api calls",
			pipelineRuns: []pipelineRunData{},
			clusterID:    "cluster-empty",
			wantEvents:   0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var prJSONs []string
			for _, pr := range tc.pipelineRuns {
				prJSONs = append(prJSONs, buildPipelineRunJSON(t, pr))
			}

			mockDir := t.TempDir()
			createMockTknResults(t, mockDir, buildTknResultsResponse(t, prJSONs, tc.taskRuns))
			createMockOC(t, mockDir)

			collector := &requestCollector{}
			server := httptest.NewServer(collector)
			defer server.Close()

			cfg := newPipelineConfig(mockDir, server.URL, tc.clusterID)
			result := runPipeline(t, cfg)
			require.NoError(t, result.Err, "Pipeline failed:\n%s", string(result.Output))

			bodies := collector.getBodies()

			if tc.wantEvents == 0 {
				assert.Empty(t, bodies, "No API calls expected for empty input")
				return
			}

			events := collectSegmentEvents(t, bodies)
			require.Equal(t, tc.wantEvents, len(events), "Event count mismatch")

			for _, pr := range tc.pipelineRuns {
				assertPipelineRunEvents(t, events, pr, tc.clusterID)
			}
		})
	}
}

func TestTektonPipelineE2EErrors(t *testing.T) {
	testCases := []struct {
		name       string
		extraEnv   map[string]string
		wantStderr string
	}{
		{
			name: "missing kubectl and oc fails",
			extraEnv: map[string]string{
				"KUBECTL": "nonexistent-kubectl-binary",
			},
			wantStderr: "KUBECTL=nonexistent-kubectl-binary not found in PATH",
		},
		{
			name: "missing auth token fails",
			extraEnv: map[string]string{
				"TEKTON_NAMESPACE":     "test-ns",
				"TEKTON_RESULTS_TOKEN": "",
				"SA_TOKEN_PATH":        "/nonexistent/path/token",
			},
			wantStderr: "No authentication token available",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockDir := t.TempDir()
			createMockTknResults(t, mockDir, buildTknResultsResponse(t, nil, nil))
			createMockOC(t, mockDir)

			cfg := newPipelineConfig(mockDir, "http://localhost:0/unused", "test-cluster")
			for k, v := range tc.extraEnv {
				cfg.ExtraEnv[k] = v
			}
			result := runPipeline(t, cfg)
			assert.Error(t, result.Err, "Pipeline should have failed")
			assert.Contains(t, string(result.Output), tc.wantStderr,
				"Expected error message not found in output:\n%s", string(result.Output))
		})
	}
}
