package scripts

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
)

// A version of exec.LookPath that can find our scripts
// Current implementation works by manipulating $PATH to include the directory
// where this Go file is located, assuming it is placed in the same location as
// the scripts
func LookPath(file string) (string, error) {
	_, goFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("failed to find path of scripts via Go file name")
	}
	if err := pushToPath(path.Dir(goFile)); err != nil {
		return "", err
	}
	return exec.LookPath(file)
}

// Push the given directory in front of $PATH unless it's already listed there
func pushToPath(dir string) error {
	osPath := os.Getenv("PATH")
	osPathList := filepath.SplitList(osPath)
	for _, pathDir := range osPathList {
		if dir == pathDir {
			return nil
		}
	}
	newOsPath := fmt.Sprintf("%s%c%s", dir, filepath.ListSeparator, osPath)
	return os.Setenv("PATH", newOsPath)
}

func GetRepoRootDir() (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("failed to find the path of the root directory")
	}
	dirPath := filepath.Dir(filepath.Dir(filename))
	return dirPath, nil
}

func AssertExecuteScript(t *testing.T, scriptPath string) []byte {
	t.Helper()
	output, err := testfixture.RunRepoScript(scriptPath, nil, nil)
	assert.NoError(t, err, "failed to run script")
	return output
}

// AssertExecuteScriptWithEnv runs the script with the given environment
// (merged with the current process env). Keys in env override existing vars.
// Use for tests that need to set e.g. NAMESPACE_NOW_ISO.
func AssertExecuteScriptWithEnv(t *testing.T, scriptPath string, env map[string]string) []byte {
	t.Helper()
	merged := os.Environ()
	for k, v := range env {
		merged = append(merged, k+"="+v)
	}
	output, err := testfixture.RunRepoScript(scriptPath, nil, merged)
	assert.NoError(t, err, "failed to run script with env")
	return output
}

// AssertExecuteScriptWithArgs runs the script with the given arguments and returns stdout.
// It asserts that the command succeeded. Use this for scripts that take arguments (e.g. a wrapper that runs a child command).
// Behaves like AssertExecuteScript (stdout only); stderr is not captured.
func AssertExecuteScriptWithArgs(t *testing.T, scriptPath string, args ...string) []byte {
	t.Helper()
	output, err := testfixture.RunRepoScript(scriptPath, nil, os.Environ(), args...)
	assert.NoError(t, err, "failed to run script with args")
	return output
}
