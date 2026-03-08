package main

import (
	"bufio"
	"os"
	"os/exec"
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

// applyInputDir ensures konflux-info namespace exists, then runs oc apply -f for each YAML in inputDir.
// Waits for the cluster API to be ready before applying (kwok can take a few seconds to start).
// Skips namespace YAMLs for namespaces that already exist (e.g. kube-system) to avoid conflicts.
func applyInputDir(t *testing.T, inputDir string) {
	t.Helper()
	// Wait for cluster API to be ready and ensure konflux-info exists.
	for i := 0; i < 60; i++ {
		createNs := exec.Command("oc", "create", "namespace", "konflux-info")
		createNs.Env = os.Environ()
		out, err := createNs.CombinedOutput()
		outStr := string(out)
		if err == nil || strings.Contains(outStr, "AlreadyExists") {
			break
		}
		if i == 59 {
			require.NoError(t, err, "create namespace konflux-info after wait: %s", outStr)
		}
		time.Sleep(time.Second)
	}

	entries, err := os.ReadDir(inputDir)
	require.NoError(t, err, "read input dir %s", inputDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".yaml") && !strings.HasSuffix(strings.ToLower(e.Name()), ".yml") {
			continue
		}
		path := filepath.Join(inputDir, e.Name())
		// Skip namespace kube-system - it already exists in the cluster; applying would conflict.
		if strings.Contains(e.Name(), "namespace") && strings.Contains(e.Name(), "kube-system") {
			continue
		}
		cmd := exec.Command("oc", "apply", "-f", path)
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "oc apply -f %s: %s", path, string(out))
	}
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

				// CLUSTER_ID comes from kube-system namespace uid; use cluster value since we don't apply kube-system (it already exists).
				getUID := exec.Command("oc", "get", "namespace", "kube-system", "-o", "jsonpath={.metadata.uid}")
				getUID.Env = os.Environ()
				uidOut, err := getUID.Output()
				require.NoError(t, err, "get kube-system uid")
				expected["CLUSTER_ID"] = strings.TrimSpace(string(uidOut))

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
