package testfixture

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTempFile(t *testing.T, content string) string {
	t.Helper()
	file, err := os.CreateTemp("", "example")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer file.Close()

	if _, err := file.WriteString(content); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}

	return file.Name()
}

func createTempScript(t *testing.T, content string) string {
	t.Helper()
	scriptFile, err := os.CreateTemp("", "*.sh")
	if err != nil {
		t.Fatalf("Failed to create temp script: %v", err)
	}
	defer scriptFile.Close()

	if _, err := scriptFile.WriteString(content); err != nil {
		t.Fatalf("Failed to write to temp script: %v", err)
	}

	if err := os.Chmod(scriptFile.Name(), 0700); err != nil {
		t.Fatalf("Failed to make script executable: %v", err)
	}

	return scriptFile.Name()
}

func TestRunScriptWithInputFile(t *testing.T) {
	t.Setenv(EnvTestImage, "")

	t.Run("SuccessfulExecution", func(t *testing.T) {
		inputFilePath := createTempFile(t, "Hello world")
		defer os.Remove(inputFilePath)

		scriptFilePath := createTempScript(t, "#!/bin/sh\ncat")
		defer os.Remove(scriptFilePath)

		output, err := RunScriptWithInputFile(inputFilePath, scriptFilePath)
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}

		expectedOutput := "Hello world"
		if string(output) != expectedOutput {
			t.Errorf("Expected %q, got %q", expectedOutput, output)
		}
	})

	t.Run("ScriptExecutionError", func(t *testing.T) {
		inputFilePath := createTempFile(t, "")
		defer os.Remove(inputFilePath)

		scriptFilePath := createTempScript(t, "#!/bin/sh\nexit 1")
		defer os.Remove(scriptFilePath)

		if _, err := RunScriptWithInputFile(inputFilePath, scriptFilePath); err == nil {
			t.Errorf("Expected an error, got none")
		}
	})

	t.Run("FileOpenError", func(t *testing.T) {
		scriptFilePath := createTempScript(t, "#!/bin/sh\ncat")
		defer os.Remove(scriptFilePath)

		if _, err := RunScriptWithInputFile("/tmp/ewefewg34234", scriptFilePath); err == nil {
			t.Errorf("Expected an error, got none")
		}
	})
}

func TestScriptBundledInBridgeImage(t *testing.T) {
	assert.True(t, ScriptBundledInBridgeImage("tekton-to-segment.sh"))
	assert.False(t, ScriptBundledInBridgeImage("not-a-real-script.sh"))
}

func TestRunRepoScriptContainerModeRejectsUnbundledScript(t *testing.T) {
	t.Setenv(EnvTestImage, "localhost/segment-bridge:test")
	t.Setenv(EnvContainerRuntime, "")

	scriptFilePath := createTempScript(t, "#!/bin/sh\necho hi")
	defer os.Remove(scriptFilePath)

	_, err := RunRepoScript(scriptFilePath, nil, os.Environ())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a script bundled")
}

func TestEnvSliceToMapLastWins(t *testing.T) {
	m := envSliceToMap([]string{"A=1", "B=2", "A=3"})
	assert.Equal(t, "3", m["A"])
	assert.Equal(t, "2", m["B"])
}

func TestContainerTestEnvOverridesClusterID(t *testing.T) {
	base := []string{"PATH=/usr/bin", "FOO=bar"}
	out := containerTestEnvOverrides(base)
	assert.Equal(t, "", envSliceToMap(out)["CLUSTER_ID"])

	withID := []string{"PATH=/bin", "CLUSTER_ID=keep-me"}
	out2 := containerTestEnvOverrides(withID)
	assert.Equal(t, "keep-me", envSliceToMap(out2)["CLUSTER_ID"])
}
