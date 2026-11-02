package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redhat-appstudio/segment-bridge.git/containerfixture"
	"github.com/redhat-appstudio/segment-bridge.git/kwok"
	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

func runReleasePaginationStub(t *testing.T, stub string) ([]byte, []byte, error) {
	t.Helper()
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, stub)
	env := releaseStubEnv(t, stubDir, map[string]string{"RELEASE_NOW_ISO": "2024-06-01T12:00:00Z"})
	cmd := exec.Command(scriptPath)
	if outputDir := os.Getenv("KCOV_OUTPUT_DIR"); outputDir != "" {
		if kcov, err := exec.LookPath("kcov"); err == nil {
			absScript, err := filepath.Abs(scriptPath)
			require.NoError(t, err)
			cmd = exec.Command(kcov, "--include-path="+filepath.Dir(absScript), outputDir, absScript)
		}
	}
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return []byte(stdout.String()), []byte(stderr.String()), err
}

func TestReleaseFetchesAllPagesAndPreservesFiltering(t *testing.T) {
	out, stderr, err := runReleasePaginationStub(t, `#!/bin/bash
case "$3" in
  '/apis/appstudio.redhat.com/v1alpha1/releases?limit=100')
    cat <<'JSON'
{"apiVersion":"appstudio.redhat.com/v1alpha1","kind":"ReleaseList","metadata":{"continue":"next+token/="},"items":[{"apiVersion":"appstudio.redhat.com/v1alpha1","kind":"Release","metadata":{"name":"first","creationTimestamp":"2024-06-01T11:00:00Z","managedFields":[{"time":"2024-06-01T11:30:00Z"}]}},{"apiVersion":"appstudio.redhat.com/v1alpha1","kind":"Release","metadata":{"name":"old","creationTimestamp":"2024-05-31T12:00:00Z","managedFields":[{"time":"2024-06-01T11:30:00Z"}]}}]}
JSON
    ;;
  '/apis/appstudio.redhat.com/v1alpha1/releases?limit=100&continue=next%2Btoken%2F%3D')
    cat <<'JSON'
{"apiVersion":"appstudio.redhat.com/v1alpha1","kind":"ReleaseList","metadata":{},"items":[{"apiVersion":"appstudio.redhat.com/v1alpha1","kind":"Release","metadata":{"name":"second","creationTimestamp":"2024-06-01T10:00:00Z"}}]}
JSON
    ;;
  *) echo "unexpected request" >&2; exit 1 ;;
esac
`)
	require.NoError(t, err, "stderr: %s", stderr)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.Len(t, lines, 2)
	var names []string
	for _, line := range lines {
		var release map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &release))
		assert.Equal(t, "Release", release["kind"])
		meta := release["metadata"].(map[string]interface{})
		names = append(names, meta["name"].(string))
		assert.NotContains(t, meta, "managedFields")
	}
	assert.Equal(t, []string{"first", "second"}, names)
}

func TestReleaseExpiredPageProducesNoPartialOutput(t *testing.T) {
	out, stderr, err := runReleasePaginationStub(t, `#!/bin/bash
case "$3" in
  '/apis/appstudio.redhat.com/v1alpha1/releases?limit=100')
    echo '{"apiVersion":"appstudio.redhat.com/v1alpha1","kind":"ReleaseList","metadata":{"continue":"next"},"items":[{"apiVersion":"appstudio.redhat.com/v1alpha1","kind":"Release","metadata":{"name":"first","creationTimestamp":"2024-06-01T11:00:00Z"}}]}'
    ;;
  '/apis/appstudio.redhat.com/v1alpha1/releases?limit=100&continue=next')
    echo 'Error from server (Expired): The provided continue parameter is too old' >&2
    exit 1
    ;;
  *) echo 'unexpected request' >&2; exit 1 ;;
esac
`)
	require.Error(t, err)
	assert.Empty(t, string(out), "an incomplete list must not be published")
	assert.Contains(t, string(stderr), "Expired")
}

// The real Kubernetes list API returns continuation tokens when the list
// exceeds the page size. Exercise all pages through the existing Kwok fixture.
func TestReleaseFetchesAllPagesFromKwok(t *testing.T) {
	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))
		applyReleaseSampleDir(t, sampleDir)
		client, err := dynamic.NewForConfig(buildRestConfig(t))
		require.NoError(t, err)
		releases := client.Resource(schema.GroupVersionResource{Group: "appstudio.redhat.com", Version: "v1alpha1", Resource: "releases"}).Namespace("default")
		for i := 0; i < 100; i++ {
			obj := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "appstudio.redhat.com/v1alpha1",
				"kind":       "Release",
				"metadata":   map[string]interface{}{"name": fmt.Sprintf("page-release-%03d", i), "namespace": "default"},
				"spec":       map[string]interface{}{"snapshot": "kwok-test-snapshot", "releasePlan": "kwok-test-release-plan"},
			}}
			_, err := releases.Create(context.Background(), obj, metav1.CreateOptions{})
			require.NoError(t, err)
		}
		// Compare complete objects with the old kubectl list on the same
		// unchanged Kwok data, including default managedFields handling.
		baseline, err := exec.Command("kubectl", "get", "releases.appstudio.redhat.com", "-A", "-o", "json").Output()
		require.NoError(t, err)
		var baselineList struct {
			Items []map[string]interface{} `json:"items"`
		}
		require.NoError(t, json.Unmarshal(baseline, &baselineList))
		expected := make(map[string]map[string]interface{})
		for _, item := range baselineList.Items {
			metadata := item["metadata"].(map[string]interface{})
			expected[metadata["name"].(string)] = item
		}
		env := append(os.Environ(), "RELEASE_NOW_ISO="+time.Now().UTC().Format(time.RFC3339))
		out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
		require.NoError(t, err, "stderr: %s", stderr)
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		require.Len(t, lines, 101)
		names := make(map[string]bool)
		for _, line := range lines {
			var release map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(line), &release))
			metadata := release["metadata"].(map[string]interface{})
			name := metadata["name"].(string)
			names[name] = true
			assert.Equal(t, expected[name], release, "release %s must match the existing kubectl output", name)
		}
		assert.True(t, names["kwok-test-release"])
		for i := 0; i < 100; i++ {
			assert.True(t, names[fmt.Sprintf("page-release-%03d", i)])
		}
	})
}

func TestReleaseRejectsMalformedPage(t *testing.T) {
	out, stderr, err := runReleasePaginationStub(t, `#!/bin/bash
echo '{"metadata":{}}'
`)
	require.Error(t, err)
	assert.Empty(t, string(out))
	assert.Contains(t, string(stderr), "jq failed")
}

func TestReleaseMissingRawAPIOnFirstPageIsSkipped(t *testing.T) {
	out, stderr, err := runReleasePaginationStub(t, `#!/bin/bash
echo 'Error from server (NotFound): the server could not find the requested resource' >&2
exit 1
`)
	require.NoError(t, err)
	assert.Empty(t, string(out))
	assert.Contains(t, string(stderr), "skipping release fetch")
}

func TestReleaseMissingRawAPIOnLaterPageFails(t *testing.T) {
	out, stderr, err := runReleasePaginationStub(t, `#!/bin/bash
case "$3" in
  '/apis/appstudio.redhat.com/v1alpha1/releases?limit=100')
    echo '{"metadata":{"continue":"next"},"items":[{"metadata":{"name":"first","creationTimestamp":"2024-06-01T11:00:00Z"}}]}'
    ;;
  *) echo 'Error from server (NotFound): the server could not find the requested resource' >&2; exit 1 ;;
esac
`)
	require.Error(t, err)
	assert.Empty(t, string(out))
	assert.Contains(t, string(stderr), "NotFound")
}

func TestReleaseRejectsInvalidContinueToken(t *testing.T) {
	out, stderr, err := runReleasePaginationStub(t, `#!/bin/bash
echo '{"metadata":{"continue":false},"items":[]}'
`)
	require.Error(t, err)
	assert.Empty(t, string(out))
	assert.Contains(t, string(stderr), "jq failed")
}
