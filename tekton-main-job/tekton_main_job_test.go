package main

import (
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

// writeStub creates an executable shell script in dir with the given content.
func writeStub(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o755))
}

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
			absScript, _ := filepath.Abs(mainJobScript)
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
	writeStub(t, dir, "fetch-tekton-records.sh",
		"#!/bin/bash\necho 'ERROR: simulated tekton-results failure' >&2\nexit 1\n")

	// fetch-konflux-op-records.sh: succeeds, outputs marker
	writeStub(t, dir, "fetch-konflux-op-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"op\"}'\n")

	// fetch-namespace-records.sh: succeeds, outputs marker
	writeStub(t, dir, "fetch-namespace-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"ns\"}'\n")

	// fetch-component-records.sh: succeeds, outputs marker
	writeStub(t, dir, "fetch-component-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"comp\"}'\n")

	// -- Downstream stubs (passthrough) --
	writeStub(t, dir, "get-konflux-public-info.sh",
		"#!/bin/bash\nexec \"$@\"\n")

	writeStub(t, dir, "tekton-to-segment.sh",
		"#!/bin/bash\ncat\n")

	writeStub(t, dir, "segment-mass-uploader.sh",
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

	writeStub(t, dir, "fetch-tekton-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"tekton\"}'\n")
	writeStub(t, dir, "fetch-konflux-op-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"op\"}'\n")
	writeStub(t, dir, "fetch-namespace-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"ns\"}'\n")
	// Last fetch fails
	writeStub(t, dir, "fetch-component-records.sh",
		"#!/bin/bash\necho 'ERROR: component API missing' >&2\nexit 1\n")

	writeStub(t, dir, "get-konflux-public-info.sh",
		"#!/bin/bash\nexec \"$@\"\n")
	writeStub(t, dir, "tekton-to-segment.sh",
		"#!/bin/bash\ncat\n")
	writeStub(t, dir, "segment-mass-uploader.sh",
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
	writeStub(t, dir, "fetch-tekton-records.sh", failStub)
	writeStub(t, dir, "fetch-konflux-op-records.sh", failStub)
	writeStub(t, dir, "fetch-namespace-records.sh", failStub)
	writeStub(t, dir, "fetch-component-records.sh", failStub)

	writeStub(t, dir, "get-konflux-public-info.sh",
		"#!/bin/bash\nexec \"$@\"\n")
	writeStub(t, dir, "tekton-to-segment.sh",
		"#!/bin/bash\ncat\n")
	writeStub(t, dir, "segment-mass-uploader.sh",
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

func TestNoSegmentWriteKey(t *testing.T) {
	dir := t.TempDir()

	writeStub(t, dir, "fetch-tekton-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"tekton\"}'\n")
	writeStub(t, dir, "fetch-konflux-op-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"op\"}'\n")
	writeStub(t, dir, "fetch-namespace-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"ns\"}'\n")
	writeStub(t, dir, "fetch-component-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"comp\"}'\n")

	writeStub(t, dir, "get-konflux-public-info.sh",
		"#!/bin/bash\nexec \"$@\"\n")
	writeStub(t, dir, "tekton-to-segment.sh",
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
