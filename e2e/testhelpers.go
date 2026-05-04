//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/require"
)

// requestCollector captures HTTP requests for testing
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
	w.Write([]byte(`{"success":true}`))
}

func (rc *requestCollector) getBodies() []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	result := make([]string, len(rc.bodies))
	copy(result, rc.bodies)
	return result
}

// startSegmentServerWithStatus creates an HTTP server that returns a specific status code
func startSegmentServerWithStatus(t *testing.T, statusCode int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/batch" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(statusCode)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// setMockKonfluxCR writes a Konflux CR JSON to the mock directory
func (e *testEnv) setMockKonfluxCR(cr *KonfluxCR) {
	crJSON, err := CreateKonfluxCRJSON(cr)
	require.NoError(e.t, err)
	crFile := filepath.Join(e.tempDir, "konflux-cr.json")
	require.NoError(e.t, os.WriteFile(crFile, []byte(crJSON), 0644))
}

// setMockNamespaces writes a namespace list JSON to the mock directory
func (e *testEnv) setMockNamespaces(namespaces []*Namespace) {
	nsJSON, err := CreateNamespaceListJSON(namespaces)
	require.NoError(e.t, err)
	nsFile := filepath.Join(e.tempDir, "namespaces.json")
	require.NoError(e.t, os.WriteFile(nsFile, []byte(nsJSON), 0644))
}

// setMockComponents writes a component list JSON to the mock directory
func (e *testEnv) setMockComponents(components []*Component) {
	compJSON, err := CreateComponentListJSON(components)
	require.NoError(e.t, err)
	compFile := filepath.Join(e.tempDir, "components.json")
	require.NoError(e.t, os.WriteFile(compFile, []byte(compJSON), 0644))
}

// setMockKubeSystemUID writes a custom kube-system UID (for cluster ID fallback tests)
func (e *testEnv) setMockKubeSystemUID(uid string) {
	uidFile := filepath.Join(e.tempDir, "kube-system-uid")
	require.NoError(e.t, os.WriteFile(uidFile, []byte(uid), 0644))
}

// setMockKonfluxPublicInfo writes a custom Konflux public info configmap
func (e *testEnv) setMockKonfluxPublicInfo(konfluxVersion, k8sVersion string) {
	inner := map[string]interface{}{
		"konfluxVersion":    konfluxVersion,
		"kubernetesVersion": k8sVersion,
	}
	innerJSON, err := json.Marshal(inner)
	require.NoError(e.t, err)

	cm := map[string]interface{}{
		"data": map[string]string{
			"info.json": string(innerJSON),
		},
	}
	cmJSON, err := json.Marshal(cm)
	require.NoError(e.t, err)

	cmFile := filepath.Join(e.tempDir, "configmap-konflux-public-info.json")
	require.NoError(e.t, os.WriteFile(cmFile, cmJSON, 0644))
}

// setFailKonfluxGet makes the mock oc fail when fetching Konflux CR
func (e *testEnv) setFailKonfluxGet() {
	failFile := filepath.Join(e.tempDir, "FAIL_KONFLUX")
	require.NoError(e.t, os.WriteFile(failFile, []byte("1"), 0644))
}

// collectSegmentEvents extracts all Segment events from request bodies
func collectSegmentEvents(t *testing.T, bodies []string) []testfixture.SegmentEvent {
	t.Helper()
	return testfixture.CollectSegmentEventsFromBodies(t, bodies)
}
