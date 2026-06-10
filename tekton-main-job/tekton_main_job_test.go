package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mainJobScript = "../scripts/tekton-main-job.sh"

// linkMainJob creates a symlink to tekton-main-job.sh inside dir so that $0
// resolves to the symlink path (SELFDIR = dir, finding the stubs), while kcov
// can still resolve the real source inode for coverage tracking.
func linkMainJob(t *testing.T, dir string) string {
	t.Helper()
	src, err := filepath.Abs(mainJobScript)
	require.NoError(t, err)
	dst := filepath.Join(dir, "tekton-main-job.sh")
	require.NoError(t, os.Symlink(src, dst))
	return dst
}

// runMainJobWithEnv executes tekton-main-job.sh with the given environment.
// When KCOV_OUTPUT_DIR is set and kcov is installed, the script is run under
// kcov using scriptPath (the symlink) so that $0 resolves to the temp dir
// (SELFDIR = temp dir, finding the stubs) while --include-path points to the
// real scripts directory for coverage attribution.
func runMainJobWithEnv(t *testing.T, scriptPath string, env []string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var cmd *exec.Cmd
	if kcovDir := strings.TrimSpace(os.Getenv(testfixture.EnvKcovOutputDir)); kcovDir != "" {
		if _, err := exec.LookPath("kcov"); err == nil {
			absScript, err := filepath.Abs(mainJobScript)
			require.NoError(t, err, "resolve absolute path of main job script for kcov")
			cmd = exec.Command("kcov",
				"--include-path="+filepath.Dir(absScript),
				kcovDir,
				scriptPath,
			)
			cmd.Env = env
		}
	}
	if cmd == nil {
		cmd = exec.Command("bash", scriptPath)
		cmd.Env = env
	}
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// runMainJob executes tekton-main-job.sh with SEGMENT_WRITE_KEY set so that
// segment_sink invokes the stub segment-mass-uploader.sh (passthrough).
func runMainJob(t *testing.T, scriptPath string) (stdout, stderr string, exitCode int) {
	t.Helper()
	env := append(os.Environ(),
		"SEGMENT_WRITE_KEY=test-key",
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
	)
	return runMainJobWithEnv(t, scriptPath, env)
}

func TestBestEffortFetchSources(t *testing.T) {
	dir := t.TempDir()

	// -- Fetch stubs --
	// fetch-tekton-records.sh: FAILS (exit 1), stderr only
	testfixture.WriteStub(t, dir, "fetch-tekton-records.sh",
		"#!/bin/bash\necho 'ERROR: simulated tekton-results failure' >&2\nexit 1\n")

	// fetch-konflux-op-records.sh: succeeds, outputs marker
	testfixture.WriteStub(t, dir, "fetch-konflux-op-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"op\"}'\n")

	// fetch-namespace-records.sh: succeeds, outputs marker
	testfixture.WriteStub(t, dir, "fetch-namespace-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"ns\"}'\n")

	// fetch-component-records.sh: succeeds, outputs marker
	testfixture.WriteStub(t, dir, "fetch-component-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"comp\"}'\n")

	// -- Downstream stubs (passthrough) --
	testfixture.WriteStub(t, dir, "get-konflux-public-info.sh",
		"#!/bin/bash\nexec \"$@\"\n")

	testfixture.WriteStub(t, dir, "tekton-to-segment.sh",
		"#!/bin/bash\ncat\n")

	testfixture.WriteStub(t, dir, "segment-mass-uploader.sh",
		"#!/bin/bash\ncat\n")

	script := linkMainJob(t, dir)
	stdout, stderr, exitCode := runMainJob(t, script)

	assert.Equal(t, 0, exitCode,
		"main job should exit 0 even when a fetch source fails; stderr:\n%s", stderr)

	assert.Contains(t, stdout, `{"marker":"op"}`,
		"output should contain events from fetch-konflux-op-records (succeeded)")
	assert.Contains(t, stdout, `{"marker":"ns"}`,
		"output should contain events from fetch-namespace-records (succeeded)")
	assert.Contains(t, stdout, `{"marker":"comp"}`,
		"output should contain events from fetch-component-records (succeeded)")

	assert.Contains(t, stderr, "simulated tekton-results failure",
		"stderr should show the failing fetch's error message")
}

func TestLastFetchFails(t *testing.T) {
	dir := t.TempDir()

	testfixture.WriteStub(t, dir, "fetch-tekton-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"tekton\"}'\n")
	testfixture.WriteStub(t, dir, "fetch-konflux-op-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"op\"}'\n")
	testfixture.WriteStub(t, dir, "fetch-namespace-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"ns\"}'\n")
	// Last fetch fails
	testfixture.WriteStub(t, dir, "fetch-component-records.sh",
		"#!/bin/bash\necho 'ERROR: component API missing' >&2\nexit 1\n")

	testfixture.WriteStub(t, dir, "get-konflux-public-info.sh",
		"#!/bin/bash\nexec \"$@\"\n")
	testfixture.WriteStub(t, dir, "tekton-to-segment.sh",
		"#!/bin/bash\ncat\n")
	testfixture.WriteStub(t, dir, "segment-mass-uploader.sh",
		"#!/bin/bash\ncat\n")

	script := linkMainJob(t, dir)
	stdout, stderr, exitCode := runMainJob(t, script)

	assert.Equal(t, 0, exitCode,
		"main job should exit 0 even when the last fetch fails; stderr:\n%s", stderr)
	assert.Contains(t, stdout, `{"marker":"tekton"}`)
	assert.Contains(t, stdout, `{"marker":"op"}`)
	assert.Contains(t, stdout, `{"marker":"ns"}`)
	assert.NotContains(t, stdout, "component",
		"failing fetch should not produce stdout output")
}

func TestAllFetchSourcesFail(t *testing.T) {
	dir := t.TempDir()

	failStub := "#!/bin/bash\necho 'ERROR: simulated failure' >&2\nexit 1\n"
	testfixture.WriteStub(t, dir, "fetch-tekton-records.sh", failStub)
	testfixture.WriteStub(t, dir, "fetch-konflux-op-records.sh", failStub)
	testfixture.WriteStub(t, dir, "fetch-namespace-records.sh", failStub)
	testfixture.WriteStub(t, dir, "fetch-component-records.sh", failStub)

	testfixture.WriteStub(t, dir, "get-konflux-public-info.sh",
		"#!/bin/bash\nexec \"$@\"\n")
	testfixture.WriteStub(t, dir, "tekton-to-segment.sh",
		"#!/bin/bash\ncat\n")
	testfixture.WriteStub(t, dir, "segment-mass-uploader.sh",
		"#!/bin/bash\ncat\n")

	script := linkMainJob(t, dir)
	stdout, stderr, exitCode := runMainJob(t, script)

	assert.Equal(t, 0, exitCode,
		"main job should still exit 0 when all fetches fail; stderr:\n%s", stderr)
	assert.Empty(t, strings.TrimSpace(stdout),
		"no events should be produced when all fetches fail")
	assert.Contains(t, stderr, "simulated failure",
		"stderr should contain fetch error messages")
}

func TestRealTransformPipeline(t *testing.T) {
	dir := t.TempDir()

	pipelineRunJSON := `{"apiVersion":"tekton.dev/v1","kind":"PipelineRun","metadata":{"name":"test-run","namespace":"test-ns","creationTimestamp":"2026-01-01T00:00:00Z"},"status":{"startTime":"2026-01-01T00:00:00Z","completionTime":"2026-01-01T00:05:00Z","conditions":[{"type":"Succeeded","status":"True","reason":"Succeeded"}]},"spec":{"pipelineRef":{"name":"build"}}}`

	testfixture.WriteStub(t, dir, "fetch-tekton-records.sh",
		"#!/bin/bash\necho '"+pipelineRunJSON+"'\n")
	testfixture.WriteStub(t, dir, "fetch-konflux-op-records.sh",
		"#!/bin/bash\n")
	testfixture.WriteStub(t, dir, "fetch-namespace-records.sh",
		"#!/bin/bash\n")
	testfixture.WriteStub(t, dir, "fetch-component-records.sh",
		"#!/bin/bash\n")

	realScriptsDir, err := filepath.Abs("../scripts")
	require.NoError(t, err)

	// Symlink real get-konflux-public-info.sh and tekton-to-segment.sh
	// so they run under kcov in the same session as tekton-main-job.sh.
	require.NoError(t, os.Symlink(
		filepath.Join(realScriptsDir, "get-konflux-public-info.sh"),
		filepath.Join(dir, "get-konflux-public-info.sh"),
	))
	require.NoError(t, os.Symlink(
		filepath.Join(realScriptsDir, "tekton-to-segment.sh"),
		filepath.Join(dir, "tekton-to-segment.sh"),
	))
	// tekton-to-segment.sh sources jq scripts via SELFDIR; symlink the jq dir.
	require.NoError(t, os.Symlink(
		filepath.Join(realScriptsDir, "jq"),
		filepath.Join(dir, "jq"),
	))

	testfixture.WriteStub(t, dir, "segment-mass-uploader.sh",
		"#!/bin/bash\ncat\n")

	// kubectl stub that provides minimal cluster info for get-konflux-public-info.sh.
	testfixture.WriteStub(t, dir, "kubectl", `#!/bin/bash
if [[ "$*" == *"configmap"* ]]; then
  exit 1
elif [[ "$*" == *"namespace kube-system"* ]]; then
  echo "test-cluster-uid"
  exit 0
fi
exit 1
`)

	script := linkMainJob(t, dir)
	env := append(os.Environ(),
		"SEGMENT_WRITE_KEY=test-key",
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
		"HEARTBEAT_TIMESTAMP=2026-01-01T00:10:00Z",
	)
	stdout, stderr, exitCode := runMainJobWithEnv(t, script, env)

	assert.Equal(t, 0, exitCode,
		"real-pipeline test should exit 0; stderr:\n%s", stderr)

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var nonEmpty []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(line))
		}
	}
	// PipelineRun produces 2 events (Started + Completed) + 1 heartbeat = 3
	require.GreaterOrEqual(t, len(nonEmpty), 3,
		"expected at least 3 output lines (Started + Completed + Heartbeat)")

	for _, line := range nonEmpty {
		var event map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &event),
			"each output line must be valid JSON")
	}
}

func TestNoSegmentWriteKey(t *testing.T) {
	dir := t.TempDir()

	testfixture.WriteStub(t, dir, "fetch-tekton-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"tekton\"}'\n")
	testfixture.WriteStub(t, dir, "fetch-konflux-op-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"op\"}'\n")
	testfixture.WriteStub(t, dir, "fetch-namespace-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"ns\"}'\n")
	testfixture.WriteStub(t, dir, "fetch-component-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"comp\"}'\n")

	testfixture.WriteStub(t, dir, "get-konflux-public-info.sh",
		"#!/bin/bash\nexec \"$@\"\n")
	testfixture.WriteStub(t, dir, "tekton-to-segment.sh",
		"#!/bin/bash\ncat\n")

	// No segment-mass-uploader.sh stub — segment_sink should drain to /dev/null.

	script := linkMainJob(t, dir)

	// Omit SEGMENT_WRITE_KEY so the else-branch (cat > /dev/null) is exercised.
	env := append(os.Environ(), "SEGMENT_BATCH_API=https://example.com/v1/batch")
	stdout, stderr, exitCode := runMainJobWithEnv(t, script, env)

	assert.Equal(t, 0, exitCode,
		"main job should exit 0 when SEGMENT_WRITE_KEY is not set; stderr:\n%s", stderr)
	assert.Empty(t, strings.TrimSpace(stdout),
		"no upload output expected when SEGMENT_WRITE_KEY is absent")
	assert.Contains(t, stderr, "No SEGMENT_WRITE_KEY configured",
		"stderr should warn about missing write key")
}
