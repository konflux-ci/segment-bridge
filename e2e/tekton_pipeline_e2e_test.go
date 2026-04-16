//go:build e2e

package e2e

import (
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

// mockOCConfig customizes the mock `oc` binary. Zero value matches the
// original defaults: public info with test versions, empty tenant/component
// lists, kube-system UID from CLUSTER_ID env at runtime.
type mockOCConfig struct {
	// PublicInfoInner is the JSON object stored in konflux-public-info data.info.json.
	PublicInfoInner string
	// KubeSystemUID, if set, is printed for kube-system namespace uid (instead of $CLUSTER_ID).
	KubeSystemUID string
	// EmptyKubeSystemUID writes an empty kube-system uid (simulates missing cluster id when CLUSTER_ID is unset).
	EmptyKubeSystemUID bool
	KonfluxCRJSON      string
	NamespacesJSON     string
	ComponentsJSON     string
	// FailKonfluxGet makes the konflux CR fetch exit 1 (simulates fetch-konflux-op-records failure).
	FailKonfluxGet bool
}

func createMockOC(t *testing.T, dir string, cfg mockOCConfig) {
	t.Helper()

	inner := cfg.PublicInfoInner
	if inner == "" {
		inner = `{"konfluxVersion":"test","kubernetesVersion":"test"}`
	}
	cm := map[string]map[string]string{
		"data": {"info.json": inner},
	}
	cmBytes, err := json.Marshal(cm)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "configmap-konflux-public-info.json"), cmBytes, 0600))

	konflux := cfg.KonfluxCRJSON
	if konflux == "" {
		konflux = "{}"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "konflux-cr.json"), []byte(konflux), 0600))

	ns := cfg.NamespacesJSON
	if ns == "" {
		ns = `{"items":[]}`
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "namespaces.json"), []byte(ns), 0600))

	comp := cfg.ComponentsJSON
	if comp == "" {
		comp = `{"items":[]}`
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "components.json"), []byte(comp), 0600))

	switch {
	case cfg.EmptyKubeSystemUID:
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kube-system-uid"), []byte{}, 0600))
	case cfg.KubeSystemUID != "":
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kube-system-uid"),
			[]byte(cfg.KubeSystemUID), 0600))
	}
	if cfg.FailKonfluxGet {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "FAIL_KONFLUX"), []byte("1"), 0600))
	}

	mockOC := filepath.Join(dir, "oc")
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
  *"get"*konfluxes*)
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
	require.NoError(t, os.WriteFile(mockOC, []byte(ocScript), 0755))
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
	UnsetEnv         []string
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

func (pc *pipelineConfig) unsetKeySet() map[string]struct{} {
	if len(pc.UnsetEnv) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(pc.UnsetEnv))
	for _, k := range pc.UnsetEnv {
		m[k] = struct{}{}
	}
	return m
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
	for k := range pc.unsetKeySet() {
		delete(overrides, k)
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

	unset := cfg.unsetKeySet()
	overrides := cfg.envOverrides(scriptsPath)
	var env []string
	applied := make(map[string]bool)
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if _, drop := unset[key]; drop {
			continue
		}
		if val, ok := overrides[key]; ok {
			env = append(env, key+"="+val)
			applied[key] = true
		} else {
			env = append(env, entry)
		}
	}
	for k, v := range overrides {
		if _, drop := unset[k]; drop {
			continue
		}
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

func assertPipelineRunEvents(t *testing.T, events []testfixture.SegmentEvent, pr pipelineRunData, clusterID string) {
	t.Helper()

	nsHash := testfixture.ComputeNamespaceHash(pr.Namespace, clusterID)

	started := testfixture.FindSegmentEvent(events, pr.UID+"-started")
	completed := testfixture.FindSegmentEvent(events, pr.UID+"-completed")
	require.NotNil(t, started, "Missing Started event for %s", pr.UID)
	require.NotNil(t, completed, "Missing Completed event for %s", pr.UID)

	testfixture.AssertSegmentTrackEnvelope(t, started, clusterID)
	testfixture.AssertSegmentTrackEnvelope(t, completed, clusterID)

	assert.Equal(t, "PipelineRun Started", started.Event)
	assert.Equal(t, pr.StartTime, started.Timestamp)
	assert.Equal(t, nsHash, started.Properties["namespaceHash"])
	assert.Equal(t, testfixture.ComputeClusterIDHash(clusterID), started.Properties["clusterIdHash"])
	assert.Equal(t, float64(pr.TaskCount), started.Properties["taskCount"])
	assert.Equal(t, pr.PipelineLabel != "", started.Properties["hasPipelineLabel"])
	if pr.PipelineType != "" {
		assert.Equal(t, pr.PipelineType, started.Properties["pipelineType"])
	}

	assert.Equal(t, "PipelineRun Completed", completed.Event)
	assert.Equal(t, pr.CompletionTime, completed.Timestamp)
	assert.Equal(t, nsHash, completed.Properties["namespaceHash"])
	assert.Equal(t, testfixture.ComputeClusterIDHash(clusterID), completed.Properties["clusterIdHash"])
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
		unsetEnv     []string
		tknBody      string
		mockOC       mockOCConfig
		extraEnv     map[string]string
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
			wantEvents: 3, // started + completed + Segment Bridge Heartbeat
		},
		{
			name: "TEKTON_NAMESPACE omitted defaults to wildcard and succeeds",
			pipelineRuns: []pipelineRunData{{
				UID: "uid-002", Name: "build-run-2", Namespace: "tenant-b",
				PipelineLabel: "docker-build", PipelineType: "build",
				StartTime: "2026-01-16T10:00:00Z", CompletionTime: "2026-01-16T10:05:00Z",
				StatusReason: "Completed", TaskCount: 2,
			}},
			clusterID:  "cluster-omit-ns",
			wantEvents: 3,
			unsetEnv:   []string{"TEKTON_NAMESPACE"},
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
			wantEvents: 7,
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
			wantEvents: 3,
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
			wantEvents: 3,
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
			wantEvents: 3,
		},
		{
			name:         "empty results produce only heartbeat event",
			pipelineRuns: []pipelineRunData{},
			clusterID:    "cluster-empty",
			wantEvents:   1,
		},
		{
			name:         "malformed tkn-results output is ignored; pipeline still succeeds",
			pipelineRuns: []pipelineRunData{},
			clusterID:    "cluster-bad-tkn",
			wantEvents:   1,
			tknBody:      "not-json{[[[",
		},
		{
			name: "fetch-konflux-op-records failure does not abort remaining fetchers or pipeline",
			pipelineRuns: []pipelineRunData{{
				UID: "uid-kfx-fail", Name: "build-kfx", Namespace: "tenant-kfx",
				PipelineLabel: "docker-build", PipelineType: "build",
				StartTime: "2026-05-01T10:00:00Z", CompletionTime: "2026-05-01T10:02:00Z",
				StatusReason: "Completed", TaskCount: 1,
			}},
			clusterID:  "cluster-kfx-fail",
			wantEvents: 3,
			mockOC:     mockOCConfig{FailKonfluxGet: true},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var prJSONs []string
			for _, pr := range tc.pipelineRuns {
				prJSONs = append(prJSONs, buildPipelineRunJSON(t, pr))
			}

			mockDir := t.TempDir()
			tknPayload := tc.tknBody
			if tknPayload == "" {
				tknPayload = buildTknResultsResponse(t, prJSONs, tc.taskRuns)
			}
			createMockTknResults(t, mockDir, tknPayload)
			createMockOC(t, mockDir, tc.mockOC)

			serverURL, getBodies := startSegmentServer(t)

			cfg := newPipelineConfig(mockDir, serverURL, tc.clusterID)
			cfg.UnsetEnv = tc.unsetEnv
			for k, v := range tc.extraEnv {
				cfg.ExtraEnv[k] = v
			}
			result := runPipeline(t, cfg)
			require.NoError(t, result.Err, "Pipeline failed:\n%s", string(result.Output))

			bodies := getBodies()
			events := testfixture.CollectSegmentEventsFromBodies(t, bodies)
			require.Equal(t, tc.wantEvents, len(events), "Event count mismatch")

			if len(tc.pipelineRuns) == 0 {
				testfixture.AssertSegmentHeartbeat(t, events, tc.clusterID, "test", "test")
				return
			}

			testfixture.AssertSegmentHeartbeat(t, events, tc.clusterID, "test", "test")
			for _, pr := range tc.pipelineRuns {
				assertPipelineRunEvents(t, events, pr, tc.clusterID)
			}
		})
	}
}

func TestTektonPipelineE2EDatasourceKPIs(t *testing.T) {
	const kpiNow = "2026-04-15T14:00:00Z"
	const kpiCreated = "2026-04-15T12:00:00Z"

	t.Run("Konflux operator CR emits operator deployment events", func(t *testing.T) {
		clusterID := "cluster-kpi-konflux"
		konfluxJSON := `{"apiVersion":"konflux.konflux-ci.dev/v1alpha1","kind":"Konflux","metadata":{"uid":"kpi-konflux-uid","name":"konflux","namespace":"konflux-system","creationTimestamp":"2026-02-17T14:42:19Z"},"status":{"conditions":[{"type":"Ready","status":"True","reason":"AllComponentsReady","lastTransitionTime":"2026-03-03T08:43:48Z"}]}}`

		mockDir := t.TempDir()
		createMockTknResults(t, mockDir, buildTknResultsResponse(t, nil, nil))
		createMockOC(t, mockDir, mockOCConfig{KonfluxCRJSON: konfluxJSON})

		serverURL, getBodies := startSegmentServer(t)
		cfg := newPipelineConfig(mockDir, serverURL, clusterID)
		require.NoError(t, runPipeline(t, cfg).Err)

		events := testfixture.CollectSegmentEventsFromBodies(t, getBodies())
		require.Len(t, events, 3)
		testfixture.AssertKonfluxOperatorEvents(t, events, "kpi-konflux-uid", "konflux-system", clusterID)
		testfixture.AssertSegmentHeartbeat(t, events, clusterID, "test", "test")
	})

	t.Run("tenant Namespace records emit Namespace Created", func(t *testing.T) {
		clusterID := "cluster-kpi-namespace"
		nsList := `{"items":[{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"tenant-kpi-ns","uid":"kpi-ns-uid","creationTimestamp":"` + kpiCreated + `","labels":{"konflux-ci.dev/type":"tenant"}}}]}`

		mockDir := t.TempDir()
		createMockTknResults(t, mockDir, buildTknResultsResponse(t, nil, nil))
		createMockOC(t, mockDir, mockOCConfig{NamespacesJSON: nsList})

		serverURL, getBodies := startSegmentServer(t)
		cfg := newPipelineConfig(mockDir, serverURL, clusterID)
		cfg.ExtraEnv["NAMESPACE_NOW_ISO"] = kpiNow
		require.NoError(t, runPipeline(t, cfg).Err)

		events := testfixture.CollectSegmentEventsFromBodies(t, getBodies())
		require.Len(t, events, 2)
		testfixture.AssertNamespaceCreatedEvent(t, events, "kpi-ns-uid", "tenant-kpi-ns", clusterID)
		testfixture.AssertSegmentHeartbeat(t, events, clusterID, "test", "test")
	})

	t.Run("Component records emit Component Created", func(t *testing.T) {
		clusterID := "cluster-kpi-component"
		compList := `{"items":[{"apiVersion":"appstudio.redhat.com/v1alpha1","kind":"Component","metadata":{"name":"my-comp","namespace":"tenant-kpi-c","uid":"kpi-comp-uid","creationTimestamp":"` + kpiCreated + `"},"spec":{"application":"my-app"}}]}`

		mockDir := t.TempDir()
		createMockTknResults(t, mockDir, buildTknResultsResponse(t, nil, nil))
		createMockOC(t, mockDir, mockOCConfig{ComponentsJSON: compList})

		serverURL, getBodies := startSegmentServer(t)
		cfg := newPipelineConfig(mockDir, serverURL, clusterID)
		cfg.ExtraEnv["COMPONENT_NOW_ISO"] = kpiNow
		require.NoError(t, runPipeline(t, cfg).Err)

		events := testfixture.CollectSegmentEventsFromBodies(t, getBodies())
		require.Len(t, events, 2)
		testfixture.AssertComponentCreatedEvent(t, events, "kpi-comp-uid", "my-comp", "tenant-kpi-c", "my-app", clusterID)
		testfixture.AssertSegmentHeartbeat(t, events, clusterID, "test", "test")
	})
}

func TestTektonPipelineE2ENoUploadMode(t *testing.T) {
	mockDir := t.TempDir()
	createMockTknResults(t, mockDir, buildTknResultsResponse(t, nil, nil))
	createMockOC(t, mockDir, mockOCConfig{})

	cfg := newPipelineConfig(mockDir, "http://127.0.0.1:9/unused", "cluster-no-upload")
	cfg.UnsetEnv = []string{"SEGMENT_WRITE_KEY"}
	result := runPipeline(t, cfg)
	require.NoError(t, result.Err, "no-upload mode should succeed:\n%s", string(result.Output))
	assert.Contains(t, string(result.Output), "No SEGMENT_WRITE_KEY configured")
}

func TestTektonPipelineE2EClusterIdentityFallback(t *testing.T) {
	mockDir := t.TempDir()
	createMockTknResults(t, mockDir, buildTknResultsResponse(t, nil, nil))
	createMockOC(t, mockDir, mockOCConfig{EmptyKubeSystemUID: true})

	serverURL, getBodies := startSegmentServer(t)
	cfg := newPipelineConfig(mockDir, serverURL, "ignored-external-id")
	cfg.UnsetEnv = []string{"CLUSTER_ID"}
	result := runPipeline(t, cfg)
	require.NoError(t, result.Err, "pipeline should succeed when CLUSTER_ID cannot be resolved from the apiserver:\n%s", string(result.Output))
	assert.Contains(t, string(result.Output), "could not read kube-system UID")

	events := testfixture.CollectSegmentEventsFromBodies(t, getBodies())
	require.Len(t, events, 1)
	testfixture.AssertSegmentHeartbeat(t, events, "anonymous", "test", "test")
}

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

func TestTektonPipelineE2EErrors(t *testing.T) {
	testCases := []struct {
		name              string
		extraEnv          map[string]string
		unsetEnv          []string
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
			name: "missing auth token fails",
			extraEnv: map[string]string{
				"TEKTON_NAMESPACE":     "test-ns",
				"TEKTON_RESULTS_TOKEN": "",
				"SA_TOKEN_PATH":        "/nonexistent/path/token",
			},
			wantStderr: "No authentication token available",
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
			mockDir := t.TempDir()
			createMockTknResults(t, mockDir, buildTknResultsResponse(t, nil, nil))
			createMockOC(t, mockDir, mockOCConfig{})

			serverURL := "http://localhost:0/unused"
			if tc.segmentHTTPStatus != 0 {
				serverURL = startSegmentServerWithStatus(t, tc.segmentHTTPStatus)
			}

			cfg := newPipelineConfig(mockDir, serverURL, "test-cluster")
			for k, v := range tc.extraEnv {
				cfg.ExtraEnv[k] = v
			}
			cfg.UnsetEnv = tc.unsetEnv
			result := runPipeline(t, cfg)
			assert.Error(t, result.Err, "Pipeline should have failed")
			assert.Contains(t, string(result.Output), tc.wantStderr,
				"Expected error message not found in output:\n%s", string(result.Output))
		})
	}
}
