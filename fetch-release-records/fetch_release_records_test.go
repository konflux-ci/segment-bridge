package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/redhat-appstudio/segment-bridge.git/containerfixture"
	"github.com/redhat-appstudio/segment-bridge.git/kwok"
	"github.com/redhat-appstudio/segment-bridge.git/scripts"
	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

const scriptPath = "../scripts/fetch-release-records.sh"

const sampleDir = "testdata/release-samples"

const waitReleaseTimeout = 10 * time.Second
const waitReleasePoll = 100 * time.Millisecond

var releaseGroupKind = schema.GroupKind{Group: "appstudio.redhat.com", Kind: "Release"}

const releaseAPIVersion = "v1alpha1"

func buildRestConfig(t *testing.T) *rest.Config {
	t.Helper()
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
	require.NoError(t, err, "build rest config from KUBECONFIG")
	return config
}

func waitForReleaseRESTMapping(ctx context.Context, t *testing.T, disco discovery.CachedDiscoveryInterface) *restmapper.DeferredDiscoveryRESTMapper {
	t.Helper()
	deadline := time.Now().Add(waitReleaseTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		disco.Invalidate()
		m := restmapper.NewDeferredDiscoveryRESTMapper(disco)
		_, lastErr = m.RESTMapping(releaseGroupKind, releaseAPIVersion)
		if lastErr == nil {
			return m
		}
		select {
		case <-ctx.Done():
			require.Fail(t, "context cancelled while waiting for Release RESTMapping")
		case <-time.After(waitReleasePoll):
		}
	}
	require.Fail(t, fmt.Sprintf("timeout waiting for Release RESTMapping after %v: %v",
		waitReleaseTimeout, lastErr))
	return nil
}

func waitForReleasePresent(ctx context.Context, t *testing.T, dynClient dynamic.Interface, mapper *restmapper.DeferredDiscoveryRESTMapper, namespace, releaseName string) {
	t.Helper()
	mapping, err := mapper.RESTMapping(releaseGroupKind, releaseAPIVersion)
	require.NoError(t, err, "RESTMapping for Release before wait")
	gvr := mapping.Resource
	ri := dynClient.Resource(gvr).Namespace(namespace)
	deadline := time.Now().Add(waitReleaseTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		_, lastErr = ri.Get(ctx, releaseName, metav1.GetOptions{})
		if lastErr == nil {
			return
		}
		if !errors.IsNotFound(lastErr) {
			require.NoError(t, lastErr, "unexpected error waiting for Release %s/%s", namespace, releaseName)
		}
		select {
		case <-ctx.Done():
			require.Fail(t, "context cancelled while waiting for Release %s/%s", namespace, releaseName)
		case <-time.After(waitReleasePoll):
		}
	}
	require.Fail(t, fmt.Sprintf("timeout waiting for Release %s/%s after %v: %v",
		namespace, releaseName, waitReleaseTimeout, lastErr))
}

func applyReleaseSampleDir(t *testing.T, inputDir string) {
	t.Helper()
	ctx := context.Background()
	config := buildRestConfig(t)

	clientset, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "create kubernetes clientset")
	_, err = clientset.Discovery().RESTClient().Get().AbsPath("/api").DoRaw(ctx)
	require.NoError(t, err, "cluster API not ready")
	dynClient, err := dynamic.NewForConfig(config)
	require.NoError(t, err, "create dynamic client")
	disco := memory.NewMemCacheClient(clientset.Discovery())
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(disco)

	entries, err := os.ReadDir(inputDir)
	require.NoError(t, err, "read input dir %s", inputDir)
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		nameLower := strings.ToLower(e.Name())
		if !strings.HasSuffix(nameLower, ".yaml") && !strings.HasSuffix(nameLower, ".yml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(inputDir, name)
		data, err := os.ReadFile(path)
		require.NoError(t, err, "read %s", path)
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var doc map[string]interface{}
			if err := decoder.Decode(&doc); err == io.EOF {
				break
			}
			require.NoError(t, err, "decode YAML doc in %s", path)
			if len(doc) == 0 {
				continue
			}
			obj := &unstructured.Unstructured{Object: doc}
			unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")
			unstructured.RemoveNestedField(obj.Object, "metadata", "uid")
			unstructured.RemoveNestedField(obj.Object, "metadata", "creationTimestamp")
			unstructured.RemoveNestedField(obj.Object, "metadata", "selfLink")
			gvk := obj.GroupVersionKind()
			if gvk.Empty() || gvk.Kind == "" {
				continue
			}
			mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
			require.NoError(t, err, "rest mapping for %s in %s", gvk, path)
			gvr := mapping.Resource
			var ri dynamic.ResourceInterface
			ns := obj.GetNamespace()
			if mapping.Scope.Name() == meta.RESTScopeNameNamespace && ns != "" {
				ri = dynClient.Resource(gvr).Namespace(ns)
			} else {
				ri = dynClient.Resource(gvr)
			}
			_, err = ri.Create(ctx, obj, metav1.CreateOptions{})
			if errors.IsAlreadyExists(err) {
				existing, getErr := ri.Get(ctx, obj.GetName(), metav1.GetOptions{})
				require.NoError(t, getErr, "get existing resource for replace in %s", path)
				obj.SetResourceVersion(existing.GetResourceVersion())
				_, err = ri.Update(ctx, obj, metav1.UpdateOptions{})
			}
			require.NoError(t, err, "apply resource from %s", path)

			if gvk.Kind == "CustomResourceDefinition" {
				mapper = waitForReleaseRESTMapping(ctx, t, disco)
			}
			if gvk.Kind == "Release" {
				waitForReleasePresent(ctx, t, dynClient, mapper, obj.GetNamespace(), obj.GetName())
			}
		}
	}
}

func TestFetchReleaseRecords(t *testing.T) {
	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))
		applyReleaseSampleDir(t, sampleDir)

		now := time.Now().UTC().Format(time.RFC3339)
		output := scripts.AssertExecuteScriptWithEnv(t, scriptPath, map[string]string{
			"RELEASE_NOW_ISO": now,
		})
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		var nonEmpty []string
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				nonEmpty = append(nonEmpty, strings.TrimSpace(line))
			}
		}
		require.Len(t, nonEmpty, 1, "expected exactly one JSON line (one release), got %d", len(nonEmpty))

		var rel map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(nonEmpty[0]), &rel), "output must be valid JSON")
		kind, _ := rel["kind"].(string)
		assert.Equal(t, "Release", kind)
		relMeta, _ := rel["metadata"].(map[string]interface{})
		require.NotNil(t, relMeta)
		name, _ := relMeta["name"].(string)
		assert.Equal(t, "kwok-test-release", name)
	})
}

func TestFetchReleaseRecordsExitsZeroWhenReleaseCRDNotInstalled(t *testing.T) {
	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))
		now := time.Now().UTC().Format(time.RFC3339)
		merged := append(os.Environ(), "RELEASE_NOW_ISO="+now)
		out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, merged)
		require.NoError(t, err,
			"script must exit 0 when Release API is absent (stderr=%q)", string(stderr))
		assert.Empty(t, strings.TrimSpace(string(out)), "stdout must be empty when skipping")
		assert.Contains(t, strings.ToLower(string(stderr)), "skipping",
			"expected skip WARNING on stderr")
	})
}

// jqFilterTimeWindow matches fetch-release-records.sh (effective time vs cutoff).
const jqFilterTimeWindow = `
  .items[]? |
  (([.metadata.creationTimestamp] + [.metadata.managedFields[]?.time // empty] | map(select(. != null)) | max) // .metadata.creationTimestamp) as $eff |
  select($eff != null and ($eff | fromdateiso8601) >= ($cutoff | fromdateiso8601)) |
  .
`

func TestReleaseTimeWindowFilter(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.Add(-4 * time.Hour).Format(time.RFC3339)
	tsOld := now.Add(-5 * time.Hour).Format(time.RFC3339)
	tsRecent := now.Add(-2 * time.Hour).Format(time.RFC3339)

	input := map[string]interface{}{
		"items": []map[string]interface{}{
			{
				"apiVersion": "appstudio.redhat.com/v1alpha1",
				"kind":       "Release",
				"metadata": map[string]interface{}{
					"name":              "old-release",
					"namespace":         "default",
					"creationTimestamp": tsOld,
				},
			},
			{
				"apiVersion": "appstudio.redhat.com/v1alpha1",
				"kind":       "Release",
				"metadata": map[string]interface{}{
					"name":              "recent-release",
					"namespace":         "default",
					"creationTimestamp": tsRecent,
				},
			},
		},
	}
	data, err := json.Marshal(input)
	require.NoError(t, err)

	tmp, err := os.CreateTemp(t.TempDir(), "rel-*.json")
	require.NoError(t, err)
	_, err = tmp.Write(data)
	require.NoError(t, err)
	require.NoError(t, tmp.Close())

	cmd := exec.Command("jq", "-c", "--arg", "cutoff", cutoff, strings.TrimSpace(jqFilterTimeWindow), tmp.Name())
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	require.NoError(t, err, "run jq filter")

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var nonEmpty []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(line))
		}
	}
	require.Len(t, nonEmpty, 1, "expected one JSON line (only recent release within 4h), got %d", len(nonEmpty))
	var rel map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(nonEmpty[0]), &rel))
	relMeta, _ := rel["metadata"].(map[string]interface{})
	require.NotNil(t, relMeta)
	assert.Equal(t, "Release", rel["kind"])
	name, _ := relMeta["name"].(string)
	assert.Equal(t, "recent-release", name)
}

func releaseStubEnv(t *testing.T, stubDir string, extra map[string]string) []string {
	t.Helper()
	t.Setenv(testfixture.EnvTestImage, "")
	env := testfixture.EnvWithStubPath(stubDir)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(""), 0o600))
	env = append(env, "KUBECONFIG="+kubeconfig)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func TestReleaseAPIMissingError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'error: the server doesn'\''t have a resource type "releases.appstudio.redhat.com"' >&2
exit 1
`)
	env := releaseStubEnv(t, stubDir, map[string]string{
		"RELEASE_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
	assert.Contains(t, strings.ToLower(string(stderr)), "warning")
	assert.Contains(t, strings.ToLower(string(stderr)), "skipping")
}

func TestReleaseRBACForbidden(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'Error from server (Forbidden): releases.appstudio.redhat.com is forbidden' >&2
exit 1
`)
	env := releaseStubEnv(t, stubDir, map[string]string{
		"RELEASE_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	_, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(string(stderr)), "error")
	assert.Contains(t, string(stderr), "forbidden")
}

func TestReleaseJqFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'not json'
exit 0
`)
	env := releaseStubEnv(t, stubDir, map[string]string{
		"RELEASE_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	_, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.Error(t, err)
	assert.Contains(t, string(stderr), "jq failed")
}

func TestReleaseKubectlWarnings(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo '{"items":[]}'
echo 'W0923 warning: deprecated API' >&2
exit 0
`)
	env := releaseStubEnv(t, stubDir, map[string]string{
		"RELEASE_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
	assert.Contains(t, string(stderr), "deprecated API")
}

func TestReleaseEmptyResults(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo '{"items":[]}'
exit 0
`)
	env := releaseStubEnv(t, stubDir, map[string]string{
		"RELEASE_NOW_ISO":      "2024-06-01T12:00:00Z",
		"RELEASE_RECENT_HOURS": "4",
	})
	out, err := testfixture.RunRepoScript(scriptPath, nil, env)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
}

func TestReleaseAPIFQNameNotFound(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'error: the server could not find the requested resource "releases.appstudio.redhat.com"' >&2
exit 1
`)
	env := releaseStubEnv(t, stubDir, map[string]string{
		"RELEASE_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
	assert.Contains(t, strings.ToLower(string(stderr)), "warning")
	assert.Contains(t, strings.ToLower(string(stderr)), "skipping")
}

func TestReleaseAPINoMatchesForKind(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'error: no matches for kind "Release" in version "appstudio.redhat.com/v1alpha1"' >&2
exit 1
`)
	env := releaseStubEnv(t, stubDir, map[string]string{
		"RELEASE_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
	assert.Contains(t, strings.ToLower(string(stderr)), "warning")
	assert.Contains(t, strings.ToLower(string(stderr)), "skipping")
}

func TestReleaseAPIGenericError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'Error: connection timed out' >&2
exit 1
`)
	env := releaseStubEnv(t, stubDir, map[string]string{
		"RELEASE_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	_, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(string(stderr)), "error")
}

func TestReleaseNoKubectlNoOc(t *testing.T) {
	env := testfixture.MinimalHostEnvWithoutKubectl(t)
	_, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.Error(t, err)
	assert.Contains(t, string(stderr), "oc or kubectl required")
}
