package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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

func buildRestConfig(t *testing.T) *rest.Config {
	t.Helper()
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
	require.NoError(t, err, "build rest config from KUBECONFIG")
	return config
}

// applyNamespaceSamples applies each Namespace YAML in sampleDir (sorted) via dynamic client.
func applyNamespaceSamples(t *testing.T) {
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
			obj := &unstructured.Unstructured{Object: doc}
			unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")
			unstructured.RemoveNestedField(obj.Object, "metadata", "uid")
			unstructured.RemoveNestedField(obj.Object, "metadata", "creationTimestamp")
			unstructured.RemoveNestedField(obj.Object, "metadata", "selfLink")
			gvk := obj.GroupVersionKind()
			require.False(t, gvk.Empty(), "GVK in %s", path)
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
		}
	}
	waitForTenantNamespaces(ctx, t, clientset, 2)
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
		applyNamespaceSamples(t)

		output := scripts.AssertExecuteScript(t, scriptPath)
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
