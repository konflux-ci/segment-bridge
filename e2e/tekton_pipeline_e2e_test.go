//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/scripts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var repoRoot string

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

type segmentEvent struct {
	Type        string                 `json:"type"`
	AnonymousID string                 `json:"anonymousId"`
	Event       string                 `json:"event"`
	MessageID   string                 `json:"messageId"`
	Timestamp   string                 `json:"timestamp"`
	Context     map[string]interface{} `json:"context"`
	Properties  map[string]interface{} `json:"properties"`
}

type segmentBatch struct {
	Batch []segmentEvent `json:"batch"`
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

func createMockTknResults(t *testing.T, response string) string {
	t.Helper()
	dir := t.TempDir()

	respFile := filepath.Join(dir, "response.json")
	require.NoError(t, os.WriteFile(respFile, []byte(response), 0600))

	script := filepath.Join(dir, "tkn-results")
	content := fmt.Sprintf("#!/bin/bash\ncat %q\n", respFile)
	require.NoError(t, os.WriteFile(script, []byte(content), 0755))

	return dir
}

// computeNamespaceHash replicates the SHA256(namespace:cluster_id) logic
// from tekton-to-segment.sh
func computeNamespaceHash(namespace, clusterID string) string {
	h := sha256.Sum256([]byte(namespace + ":" + clusterID))
	return fmt.Sprintf("%x", h)[:12]
}

func runPipeline(t *testing.T, mockDir, serverURL, clusterID string, extraEnv map[string]string) ([]byte, error) {
	t.Helper()

	if repoRoot == "" {
		var err error
		repoRoot, err = scripts.GetRepoRootDir()
		require.NoError(t, err, "Failed to resolve repo root")
	}
	scriptsPath := filepath.Join(repoRoot, "scripts")

	cmd := exec.Command(filepath.Join(scriptsPath, "tekton-main-job.sh"))

	overrides := map[string]string{
		"PATH":                 fmt.Sprintf("%s:%s:%s", mockDir, scriptsPath, os.Getenv("PATH")),
		"TEKTON_NAMESPACE":     "test-ns",
		"TEKTON_RESULTS_TOKEN": "dummy-token",
		"CLUSTER_ID":           clusterID,
		"SEGMENT_BATCH_API":    serverURL,
		"SEGMENT_WRITE_KEY":    "test-write-key",
		"SEGMENT_RETRIES":      "0",
	}
	for k, v := range extraEnv {
		overrides[k] = v
	}

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

	return cmd.CombinedOutput()
}

func collectSegmentEvents(t *testing.T, bodies []string) []segmentEvent {
	t.Helper()
	var events []segmentEvent
	for _, body := range bodies {
		var batch segmentBatch
		require.NoError(t, json.Unmarshal([]byte(body), &batch),
			"Failed to decode batch payload")
		events = append(events, batch.Batch...)
	}
	return events
}

func findEvent(events []segmentEvent, messageID string) *segmentEvent {
	for i := range events {
		if events[i].MessageID == messageID {
			return &events[i]
		}
	}
	return nil
}

func assertPipelineRunEvents(t *testing.T, events []segmentEvent, pr pipelineRunData, clusterID string) {
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
