package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/redhat-appstudio/segment-bridge.git/containerfixture"
	"github.com/redhat-appstudio/segment-bridge.git/kwok"
	"github.com/redhat-appstudio/segment-bridge.git/scripts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
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

const scriptPath = "../scripts/get-konflux-public-info.sh"

// envVarLine matches KEY=value or KEY="value" for parsing env output and expected files.
var envVarLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// parseEnv parses env-style content (e.g. from an env file or from `env` output) into a map.
// Lines are KEY=value or KEY="value"; surrounding quotes are stripped from values.
// Empty lines and lines starting with # are skipped.
func parseEnv(content string) (map[string]string, error) {
	out := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		matches := envVarLine.FindStringSubmatch(line)
		if len(matches) != 3 {
			continue
		}
		key, val := matches[1], matches[2]
		val = strings.TrimPrefix(strings.TrimSuffix(val, `"`), `"`)
		out[key] = val
	}
	return out, scanner.Err()
}

// parseExpectedEnv reads an env file and returns a map (same format as parseEnv).
func parseExpectedEnv(filePath string) (map[string]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return parseEnv(string(data))
}

// buildRestConfig returns a rest.Config from KUBECONFIG (set by kwok.SetKubeconfigWithPort).
func buildRestConfig(t *testing.T) *rest.Config {
	t.Helper()
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
	require.NoError(t, err, "build rest config from KUBECONFIG")
	return config
}

// applyInputDir ensures konflux-info namespace exists, then applies each YAML in inputDir using the Kubernetes Go client.
// Waits for the cluster API to be ready before applying (kwok can take a few seconds to start).
// Skips namespace YAMLs for namespaces that already exist (e.g. kube-system) to avoid conflicts.
func applyInputDir(t *testing.T, inputDir string) {
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

	// Wait for cluster API to be ready and ensure konflux-info exists.
	nsClient := clientset.CoreV1().Namespaces()
	for i := 0; i < 60; i++ {
		_, err := nsClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "konflux-info"}}, metav1.CreateOptions{})
		if err == nil || errors.IsAlreadyExists(err) {
			break
		}
		if i == 59 {
			require.NoError(t, err, "create namespace konflux-info after wait")
		}
		time.Sleep(time.Second)
	}

	entries, err := os.ReadDir(inputDir)
	require.NoError(t, err, "read input dir %s", inputDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		nameLower := strings.ToLower(e.Name())
		if !strings.HasSuffix(nameLower, ".yaml") && !strings.HasSuffix(nameLower, ".yml") {
			continue
		}
		path := filepath.Join(inputDir, e.Name())
		if strings.Contains(e.Name(), "namespace") && strings.Contains(e.Name(), "kube-system") {
			continue
		}
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
			// Strip server-managed fields so Create is accepted (API rejects resourceVersion/uid on create).
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
		}
	}
}

// getKubeSystemUID returns the UID of the kube-system namespace via the Go API.
func getKubeSystemUID(t *testing.T, config *rest.Config) string {
	t.Helper()
	clientset, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "create kubernetes clientset")
	ns, err := clientset.CoreV1().Namespaces().Get(context.Background(), "kube-system", metav1.GetOptions{})
	require.NoError(t, err, "get kube-system namespace")
	return string(ns.UID)
}

func TestGetKonfluxPublicInfo(t *testing.T) {
	testCases := []struct {
		name            string
		inputDir        string
		expectedEnvFile string
	}{
		{
			name:            "k8s-konflux-public-info",
			inputDir:        "sample/input/k8s",
			expectedEnvFile: "sample/expected/k8s-konflux-public-info.env",
		},
	}

	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				applyInputDir(t, tc.inputDir)

				expected, err := parseExpectedEnv(tc.expectedEnvFile)
				require.NoError(t, err, "parse expected env file")
				require.NotEmpty(t, expected, "expected env file must not be empty")

				config := buildRestConfig(t)
				expected["CLUSTER_ID"] = getKubeSystemUID(t, config)

				output := scripts.AssertExecuteScriptWithArgs(t, scriptPath, "env")

				actual, err := parseEnv(string(output))
				require.NoError(t, err)

				for k, want := range expected {
					got, ok := actual[k]
					assert.True(t, ok, "missing env var %s", k)
					assert.Equal(t, want, got, "env var %s", k)
				}
			})
		}
	})
}
