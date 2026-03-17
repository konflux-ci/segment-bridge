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

const scriptPath = "../scripts/fetch-namespace-records.sh"

const sampleDir = "testdata/namespace-samples"

// Label selector used by fetch-namespace-records.sh (must stay in sync).
const tenantLabelSelector = "konflux-ci.dev/type=tenant"

const waitNamespaceTimeout = 10 * time.Second
const waitNamespacePoll = 100 * time.Millisecond

// NamespaceFixtureModifier is called for each fixture doc when loading; docIndex
// is the 0-based index of the namespace doc across all files. Use to set e.g.
// metadata.creationTimestamp from test code.
type NamespaceFixtureModifier func(docIndex int, doc map[string]interface{})

func buildRestConfig(t *testing.T) *rest.Config {
	t.Helper()
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
	require.NoError(t, err, "build rest config from KUBECONFIG")
	return config
}

// loadNamespaceFixtureDocs reads YAML from sampleDir (sorted by name) and
// returns a slice of doc maps. If modifier is non-nil, it is called for each
// doc (docIndex 0, 1, ...) so tests can adjust timestamps etc. before apply.
func loadNamespaceFixtureDocs(t *testing.T, modifier NamespaceFixtureModifier) []map[string]interface{} {
	t.Helper()
	entries, err := os.ReadDir(sampleDir)
	require.NoError(t, err, "read sample dir %s", sampleDir)
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(strings.ToLower(n), ".yaml") || strings.HasSuffix(strings.ToLower(n), ".yml") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	require.NotEmpty(t, names, "no yaml samples in %s", sampleDir)

	var docs []map[string]interface{}
	docIndex := 0
	for _, name := range names {
		path := filepath.Join(sampleDir, name)
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
			if modifier != nil {
				modifier(docIndex, doc)
			}
			docs = append(docs, doc)
			docIndex++
		}
	}
	return docs
}

// applyNamespaceDocs applies the given namespace docs to the cluster. When
// stripCreationTimestamp is true, metadata fields (resourceVersion, uid,
// creationTimestamp, selfLink) are removed before apply so the server sets
// them. When false, creationTimestamp is left so tests can assert time-window
// filtering (if the cluster accepts it, e.g. kwok).
func applyNamespaceDocs(t *testing.T, docs []map[string]interface{}, stripCreationTimestamp bool) {
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

	for _, doc := range docs {
		obj := &unstructured.Unstructured{Object: doc}
		unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")
		unstructured.RemoveNestedField(obj.Object, "metadata", "uid")
		unstructured.RemoveNestedField(obj.Object, "metadata", "selfLink")
		if stripCreationTimestamp {
			unstructured.RemoveNestedField(obj.Object, "metadata", "creationTimestamp")
		}
		gvk := obj.GroupVersionKind()
		require.False(t, gvk.Empty(), "GVK in doc")
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
		require.NoError(t, err, "rest mapping for %s", gvk)
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
			require.NoError(t, getErr, "get existing resource for replace")
			obj.SetResourceVersion(existing.GetResourceVersion())
			_, err = ri.Update(ctx, obj, metav1.UpdateOptions{})
		}
		require.NoError(t, err, "apply resource")
	}
	waitForTenantNamespaces(ctx, t, clientset, len(docs))
}

// waitForTenantNamespaces polls until the cluster has at least expectedCount
// namespaces with the tenant label, or fails the test after timeout.
func waitForTenantNamespaces(ctx context.Context, t *testing.T, clientset *kubernetes.Clientset, expectedCount int) {
	t.Helper()
	deadline := time.Now().Add(waitNamespaceTimeout)
	for time.Now().Before(deadline) {
		list, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
			LabelSelector: tenantLabelSelector,
		})
		require.NoError(t, err, "list namespaces with label %s", tenantLabelSelector)
		if len(list.Items) >= expectedCount {
			return
		}
		select {
		case <-ctx.Done():
			require.Fail(t, "context cancelled while waiting for tenant namespaces")
		case <-time.After(waitNamespacePoll):
		}
	}
	list, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: tenantLabelSelector,
	})
	require.NoError(t, err)
	require.Fail(t, fmt.Sprintf("timeout waiting for %d namespaces with label %s (got %d after %v)",
		expectedCount, tenantLabelSelector, len(list.Items), waitNamespaceTimeout))
}

func TestFetchNamespaceRecords(t *testing.T) {
	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))
		docs := loadNamespaceFixtureDocs(t, nil)
		applyNamespaceDocs(t, docs, true)

		now := time.Now().UTC().Format(time.RFC3339)
		output := scripts.AssertExecuteScriptWithEnv(t, scriptPath, map[string]string{
			"NAMESPACE_NOW_ISO": now,
		})
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		var nonEmpty []string
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				nonEmpty = append(nonEmpty, strings.TrimSpace(line))
			}
		}
		require.Len(t, nonEmpty, 2, "expected exactly two JSON lines (one per namespace), got %d", len(nonEmpty))

		for i, line := range nonEmpty {
			var ns map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(line), &ns), "line %d must be valid JSON", i)
			kind, _ := ns["kind"].(string)
			assert.Equal(t, "Namespace", kind, "line %d expected kind Namespace", i)
			meta, _ := ns["metadata"].(map[string]interface{})
			require.NotNil(t, meta, "line %d expected metadata", i)
			name, _ := meta["name"].(string)
			require.NotEmpty(t, name, "line %d expected metadata.name", i)
		}
	})
}

// jqFilterTimeWindow is the same filter as in fetch-namespace-records.sh:
// effective time = max(creationTimestamp, managedFields[].time); keep if >= cutoff.
const jqFilterTimeWindow = `
  .items[]? |
  (([.metadata.creationTimestamp] + [.metadata.managedFields[]?.time // empty] | map(select(. != null)) | max) // .metadata.creationTimestamp) as $eff |
  select($eff != null and ($eff | fromdateiso8601) >= ($cutoff | fromdateiso8601)) |
  .
`

// TestNamespaceTimeWindowFilter asserts the script's 4h time-window filter:
// only namespaces whose effective timestamp is within the window are emitted.
// Kwok does not preserve client-set creationTimestamp, so we test the jq filter
// directly with fixture JSON (two namespaces: 5h ago and 2h ago; expect 1).
func TestNamespaceTimeWindowFilter(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.Add(-4 * time.Hour).Format(time.RFC3339)
	tsOld := now.Add(-5 * time.Hour).Format(time.RFC3339)
	tsRecent := now.Add(-2 * time.Hour).Format(time.RFC3339)

	// kubectl get ns -o json shape
	input := map[string]interface{}{
		"items": []map[string]interface{}{
			{
				"apiVersion": "v1",
				"kind":       "Namespace",
				"metadata": map[string]interface{}{
					"name":              "old-ns",
					"creationTimestamp": tsOld,
				},
			},
			{
				"apiVersion": "v1",
				"kind":       "Namespace",
				"metadata": map[string]interface{}{
					"name":              "recent-ns",
					"creationTimestamp": tsRecent,
				},
			},
		},
	}
	data, err := json.Marshal(input)
	require.NoError(t, err)

	tmp, err := os.CreateTemp(t.TempDir(), "ns-*.json")
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
	require.Len(t, nonEmpty, 1, "expected one JSON line (only recent namespace within 4h), got %d", len(nonEmpty))
	var ns map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(nonEmpty[0]), &ns))
	meta, _ := ns["metadata"].(map[string]interface{})
	require.NotNil(t, meta)
	assert.Equal(t, "Namespace", ns["kind"])
	name, _ := meta["name"].(string)
	assert.Equal(t, "recent-ns", name)
}
