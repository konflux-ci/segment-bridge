package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/redhat-appstudio/segment-bridge.git/containerfixture"
	"github.com/redhat-appstudio/segment-bridge.git/kwok"
	"github.com/redhat-appstudio/segment-bridge.git/scripts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

const scriptPath = "../scripts/fetch-component-records.sh"

const sampleDir = "testdata/component-samples"

func buildRestConfig(t *testing.T) *rest.Config {
	t.Helper()
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
	require.NoError(t, err, "build rest config from KUBECONFIG")
	return config
}

// applyComponentSampleDir applies each YAML in sampleDir in sorted order (CRD first).
func applyComponentSampleDir(t *testing.T, inputDir string) {
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

	applyFile := func(path string, m *restmapper.DeferredDiscoveryRESTMapper) {
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
			mapping, err := m.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
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
		}
	}

	for i, name := range names {
		path := filepath.Join(inputDir, name)
		applyFile(path, mapper)
		if i == 0 || strings.Contains(strings.ToLower(name), "crd") {
			disco.Invalidate()
			mapper = restmapper.NewDeferredDiscoveryRESTMapper(disco)
			time.Sleep(500 * time.Millisecond)
		}
	}
	time.Sleep(500 * time.Millisecond)
}

func TestFetchComponentRecords(t *testing.T) {
	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))
		applyComponentSampleDir(t, sampleDir)

		now := time.Now().UTC().Format(time.RFC3339)
		output := scripts.AssertExecuteScriptWithEnv(t, scriptPath, map[string]string{
			"COMPONENT_NOW_ISO": now,
		})
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		var nonEmpty []string
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				nonEmpty = append(nonEmpty, strings.TrimSpace(line))
			}
		}
		require.Len(t, nonEmpty, 1, "expected exactly one JSON line (one component), got %d", len(nonEmpty))

		var comp map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(nonEmpty[0]), &comp), "output must be valid JSON")
		kind, _ := comp["kind"].(string)
		assert.Equal(t, "Component", kind)
		meta, _ := comp["metadata"].(map[string]interface{})
		require.NotNil(t, meta)
		name, _ := meta["name"].(string)
		assert.Equal(t, "kwok-test-component", name)
	})
}

// jqFilterTimeWindow matches fetch-component-records.sh (effective time vs cutoff).
const jqFilterTimeWindow = `
  .items[]? |
  (([.metadata.creationTimestamp] + [.metadata.managedFields[]?.time // empty] | map(select(. != null)) | max) // .metadata.creationTimestamp) as $eff |
  select($eff != null and ($eff | fromdateiso8601) >= ($cutoff | fromdateiso8601)) |
  .
`

func TestComponentTimeWindowFilter(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.Add(-4 * time.Hour).Format(time.RFC3339)
	tsOld := now.Add(-5 * time.Hour).Format(time.RFC3339)
	tsRecent := now.Add(-2 * time.Hour).Format(time.RFC3339)

	input := map[string]interface{}{
		"items": []map[string]interface{}{
			{
				"apiVersion": "appstudio.redhat.com/v1alpha1",
				"kind":       "Component",
				"metadata": map[string]interface{}{
					"name":              "old-comp",
					"namespace":         "default",
					"creationTimestamp": tsOld,
				},
			},
			{
				"apiVersion": "appstudio.redhat.com/v1alpha1",
				"kind":       "Component",
				"metadata": map[string]interface{}{
					"name":              "recent-comp",
					"namespace":         "default",
					"creationTimestamp": tsRecent,
				},
			},
		},
	}
	data, err := json.Marshal(input)
	require.NoError(t, err)

	tmp, err := os.CreateTemp(t.TempDir(), "comp-*.json")
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
	require.Len(t, nonEmpty, 1, "expected one JSON line (only recent component within 4h), got %d", len(nonEmpty))
	var comp map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(nonEmpty[0]), &comp))
	meta, _ := comp["metadata"].(map[string]interface{})
	require.NotNil(t, meta)
	assert.Equal(t, "Component", comp["kind"])
	name, _ := meta["name"].(string)
	assert.Equal(t, "recent-comp", name)
}
