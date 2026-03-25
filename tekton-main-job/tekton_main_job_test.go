package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

// copyMainJob copies tekton-main-job.sh into dir so SELFDIR points at the stubs.
func copyMainJob(t *testing.T, dir string) string {
	t.Helper()
	src, err := filepath.Abs(mainJobScript)
	require.NoError(t, err)
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	dst := filepath.Join(dir, "tekton-main-job.sh")
	require.NoError(t, os.WriteFile(dst, data, 0o755))
	return dst
}

// runMainJob executes tekton-main-job.sh from dir with SEGMENT_WRITE_KEY set
// so that segment_sink invokes the stub segment-mass-uploader.sh (passthrough).
func runMainJob(t *testing.T, scriptPath string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"SEGMENT_WRITE_KEY=test-key",
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
	)
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

	script := copyMainJob(t, dir)
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

	script := copyMainJob(t, dir)
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

	script := copyMainJob(t, dir)
	stdout, stderr, exitCode := runMainJob(t, script)

	assert.Equal(t, 0, exitCode,
		"main job should still exit 0 when all fetches fail; stderr:\n%s", stderr)
	assert.Empty(t, strings.TrimSpace(stdout),
		"no events should be produced when all fetches fail")
	assert.Contains(t, stderr, "simulated failure",
		"stderr should contain fetch error messages")
}
