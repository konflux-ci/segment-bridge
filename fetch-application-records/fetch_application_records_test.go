package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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

const scriptPath = "../scripts/fetch-application-records.sh"

const sampleDir = "testdata/application-samples"

const waitApplicationTimeout = 10 * time.Second
const waitApplicationPoll = 100 * time.Millisecond

var applicationGroupKind = schema.GroupKind{Group: "appstudio.redhat.com", Kind: "Application"}

const applicationAPIVersion = "v1alpha1"

func buildRestConfig(t *testing.T) *rest.Config {
	t.Helper()
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
	require.NoError(t, err, "build rest config from KUBECONFIG")
	return config
}

func waitForApplicationRESTMapping(ctx context.Context, t *testing.T, disco discovery.CachedDiscoveryInterface) *restmapper.DeferredDiscoveryRESTMapper {
	t.Helper()
	deadline := time.Now().Add(waitApplicationTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		disco.Invalidate()
		m := restmapper.NewDeferredDiscoveryRESTMapper(disco)
		_, lastErr = m.RESTMapping(applicationGroupKind, applicationAPIVersion)
		if lastErr == nil {
			return m
		}
		select {
		case <-ctx.Done():
			require.Fail(t, "context cancelled while waiting for Application RESTMapping")
		case <-time.After(waitApplicationPoll):
		}
	}
	require.Fail(t, fmt.Sprintf("timeout waiting for Application RESTMapping after %v: %v",
		waitApplicationTimeout, lastErr))
	return nil
}

func waitForApplicationPresent(ctx context.Context, t *testing.T, dynClient dynamic.Interface, mapper *restmapper.DeferredDiscoveryRESTMapper, namespace, applicationName string) {
	t.Helper()
	mapping, err := mapper.RESTMapping(applicationGroupKind, applicationAPIVersion)
	require.NoError(t, err, "RESTMapping for Application before wait")
	gvr := mapping.Resource
	ri := dynClient.Resource(gvr).Namespace(namespace)
	deadline := time.Now().Add(waitApplicationTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		_, lastErr = ri.Get(ctx, applicationName, metav1.GetOptions{})
		if lastErr == nil {
			return
		}
		if !errors.IsNotFound(lastErr) {
			require.NoError(t, lastErr, "unexpected error waiting for Application %s/%s", namespace, applicationName)
		}
		select {
		case <-ctx.Done():
			require.Fail(t, "context cancelled while waiting for Application %s/%s", namespace, applicationName)
		case <-time.After(waitApplicationPoll):
		}
	}
	require.Fail(t, fmt.Sprintf("timeout waiting for Application %s/%s after %v: %v",
		namespace, applicationName, waitApplicationTimeout, lastErr))
}

func applyApplicationSampleDir(t *testing.T, inputDir string) {
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
				mapper = waitForApplicationRESTMapping(ctx, t, disco)
			}
			if gvk.Kind == "Application" {
				waitForApplicationPresent(ctx, t, dynClient, mapper, obj.GetNamespace(), obj.GetName())
			}
		}
	}
}

func TestFetchApplicationRecords(t *testing.T) {
	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))
		applyApplicationSampleDir(t, sampleDir)

		now := time.Now().UTC().Format(time.RFC3339)
		output := scripts.AssertExecuteScriptWithEnv(t, scriptPath, map[string]string{
			"APPLICATION_NOW_ISO": now,
		})
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		var nonEmpty []string
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				nonEmpty = append(nonEmpty, strings.TrimSpace(line))
			}
		}
		require.Len(t, nonEmpty, 1, "expected exactly one JSON line (one application), got %d", len(nonEmpty))

		var app map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(nonEmpty[0]), &app), "output must be valid JSON")
		kind, _ := app["kind"].(string)
		assert.Equal(t, "Application", kind)
		appMeta, _ := app["metadata"].(map[string]interface{})
		require.NotNil(t, appMeta)
		name, _ := appMeta["name"].(string)
		assert.Equal(t, "kwok-test-application", name)
	})
}

func TestFetchApplicationRecordsExitsZeroWhenApplicationCRDNotInstalled(t *testing.T) {
	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))
		now := time.Now().UTC().Format(time.RFC3339)
		merged := append(os.Environ(), "APPLICATION_NOW_ISO="+now)
		out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, merged)
		require.NoError(t, err,
			"script must exit 0 when Application API is absent (stderr=%q)", string(stderr))
		assert.Empty(t, strings.TrimSpace(string(out)), "stdout must be empty when skipping")
		assert.Contains(t, strings.ToLower(string(stderr)), "skipping",
			"expected skip WARNING on stderr")
	})
}

func TestFetchApplicationRecordsFiltersOldApplications(t *testing.T) {
	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))
		applyApplicationSampleDir(t, sampleDir)

		// Set APPLICATION_NOW_ISO to a point far in the future so the 4-hour
		// cutoff (futureNow - 4h) is still well ahead of now, meaning the
		// sample application (created just now by kwok) is below the cutoff
		// and therefore filtered out.
		pastNow := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
		out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, append(
			os.Environ(),
			"APPLICATION_NOW_ISO="+pastNow,
		))
		require.NoError(t, err, "script must exit 0 (stderr=%q)", string(stderr))
		assert.Empty(t, strings.TrimSpace(string(out)),
			"expected no output: application must be filtered out when the time window is entirely in the future")
	})
}

func applicationStubEnv(t *testing.T, stubDir string, extra map[string]string) []string {
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

func TestApplicationAPIMissingError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'error: the server doesn'\''t have a resource type "applications.appstudio.redhat.com"' >&2
exit 1
`)
	env := applicationStubEnv(t, stubDir, map[string]string{
		"APPLICATION_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
	assert.Contains(t, strings.ToLower(string(stderr)), "warning")
	assert.Contains(t, strings.ToLower(string(stderr)), "skipping")
}

func TestApplicationRBACForbidden(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'Error from server (Forbidden): applications.appstudio.redhat.com is forbidden' >&2
exit 1
`)
	env := applicationStubEnv(t, stubDir, map[string]string{
		"APPLICATION_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	_, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(string(stderr)), "error")
	assert.Contains(t, string(stderr), "forbidden")
}

func TestApplicationJqFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'not json'
exit 0
`)
	env := applicationStubEnv(t, stubDir, map[string]string{
		"APPLICATION_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	_, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.Error(t, err)
	assert.Contains(t, string(stderr), "jq failed")
}

func TestApplicationKubectlWarnings(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo '{"items":[]}'
echo 'W0923 warning: deprecated API' >&2
exit 0
`)
	env := applicationStubEnv(t, stubDir, map[string]string{
		"APPLICATION_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
	assert.Contains(t, string(stderr), "deprecated API")
}

func TestApplicationEmptyResults(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo '{"items":[]}'
exit 0
`)
	env := applicationStubEnv(t, stubDir, map[string]string{
		"APPLICATION_NOW_ISO":      "2024-06-01T12:00:00Z",
		"APPLICATION_RECENT_HOURS": "4",
	})
	out, err := testfixture.RunRepoScript(scriptPath, nil, env)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
}

func TestApplicationAPIFQNameNotFound(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'error: the server could not find the requested resource "applications.appstudio.redhat.com"' >&2
exit 1
`)
	env := applicationStubEnv(t, stubDir, map[string]string{
		"APPLICATION_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
	assert.Contains(t, strings.ToLower(string(stderr)), "warning")
	assert.Contains(t, strings.ToLower(string(stderr)), "skipping")
}

func TestApplicationAPINoMatchesForKind(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'error: no matches for kind "Application" in version "appstudio.redhat.com/v1alpha1"' >&2
exit 1
`)
	env := applicationStubEnv(t, stubDir, map[string]string{
		"APPLICATION_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	out, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
	assert.Contains(t, strings.ToLower(string(stderr)), "warning")
	assert.Contains(t, strings.ToLower(string(stderr)), "skipping")
}

func TestApplicationAPIGenericError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires GNU date -d (Linux only)")
	}
	stubDir := t.TempDir()
	testfixture.WriteKubectlOcStubs(t, stubDir, `#!/bin/bash
echo 'Error: connection timed out' >&2
exit 1
`)
	env := applicationStubEnv(t, stubDir, map[string]string{
		"APPLICATION_NOW_ISO": "2024-06-01T12:00:00Z",
	})
	_, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(string(stderr)), "error")
}

func TestApplicationNoKubectlNoOc(t *testing.T) {
	env := testfixture.MinimalHostEnvWithoutKubectl(t)
	_, stderr, err := testfixture.RunRepoScriptWithStderr(scriptPath, nil, env)
	require.Error(t, err)
	assert.Contains(t, string(stderr), "oc or kubectl required")
}
