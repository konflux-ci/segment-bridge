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

// telemetryToggleVars must not leak from the host into script tests. A
// developer or CI worker may have FETCH_*=false or EMIT_HEARTBEAT=false set.
var telemetryToggleVars = []string{
	"FETCH_PIPELINERUNS",
	"FETCH_OPERATOR",
	"FETCH_NAMESPACES",
	"FETCH_COMPONENTS",
	"FETCH_APPLICATIONS",
	"FETCH_RELEASES",
	"EMIT_HEARTBEAT",
}

func environWithoutTelemetryToggles(extra ...string) []string {
	drop := make(map[string]struct{}, len(telemetryToggleVars))
	for _, k := range telemetryToggleVars {
		drop[k] = struct{}{}
	}
	env := os.Environ()
	out := make([]string, 0, len(env)+len(extra))
	for _, e := range env {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if _, skip := drop[key]; skip {
			continue
		}
		out = append(out, e)
	}
	return append(out, extra...)
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
	env := environWithoutTelemetryToggles(
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

	// fetch-application-records.sh: succeeds, outputs marker
	testfixture.WriteStub(t, dir, "fetch-application-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"app\"}'\n")

	// fetch-release-records.sh: succeeds, outputs marker
	testfixture.WriteStub(t, dir, "fetch-release-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"rel\"}'\n")

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
	assert.Contains(t, stdout, `{"marker":"app"}`,
		"output should contain events from fetch-application-records (succeeded)")
	assert.Contains(t, stdout, `{"marker":"rel"}`,
		"output should contain events from fetch-release-records (succeeded)")

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
	testfixture.WriteStub(t, dir, "fetch-component-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"comp\"}'\n")
	testfixture.WriteStub(t, dir, "fetch-application-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"app\"}'\n")
	// Last fetch fails
	testfixture.WriteStub(t, dir, "fetch-release-records.sh",
		"#!/bin/bash\necho 'ERROR: release API missing' >&2\nexit 1\n")

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
	assert.Contains(t, stdout, `{"marker":"comp"}`)
	assert.Contains(t, stdout, `{"marker":"app"}`)
	assert.NotContains(t, stdout, "release",
		"failing fetch should not produce stdout output")
}

func TestAllFetchSourcesFail(t *testing.T) {
	dir := t.TempDir()

	failStub := "#!/bin/bash\necho 'ERROR: simulated failure' >&2\nexit 1\n"
	testfixture.WriteStub(t, dir, "fetch-tekton-records.sh", failStub)
	testfixture.WriteStub(t, dir, "fetch-konflux-op-records.sh", failStub)
	testfixture.WriteStub(t, dir, "fetch-namespace-records.sh", failStub)
	testfixture.WriteStub(t, dir, "fetch-component-records.sh", failStub)
	testfixture.WriteStub(t, dir, "fetch-application-records.sh", failStub)
	testfixture.WriteStub(t, dir, "fetch-release-records.sh", failStub)

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
	testfixture.WriteStub(t, dir, "fetch-application-records.sh",
		"#!/bin/bash\n")
	testfixture.WriteStub(t, dir, "fetch-release-records.sh",
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
	env := environWithoutTelemetryToggles(
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
	testfixture.WriteStub(t, dir, "fetch-application-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"app\"}'\n")
	testfixture.WriteStub(t, dir, "fetch-release-records.sh",
		"#!/bin/bash\necho '{\"marker\":\"rel\"}'\n")

	testfixture.WriteStub(t, dir, "get-konflux-public-info.sh",
		"#!/bin/bash\nexec \"$@\"\n")
	testfixture.WriteStub(t, dir, "tekton-to-segment.sh",
		"#!/bin/bash\ncat\n")

	// No segment-mass-uploader.sh stub — segment_sink should drain to /dev/null.

	script := linkMainJob(t, dir)

	// Omit SEGMENT_WRITE_KEY so the else-branch (cat > /dev/null) is exercised.
	env := environWithoutTelemetryToggles("SEGMENT_BATCH_API=https://example.com/v1/batch")
	stdout, stderr, exitCode := runMainJobWithEnv(t, script, env)

	assert.Equal(t, 0, exitCode,
		"main job should exit 0 when SEGMENT_WRITE_KEY is not set; stderr:\n%s", stderr)
	assert.Empty(t, strings.TrimSpace(stdout),
		"no upload output expected when SEGMENT_WRITE_KEY is absent")
	assert.Contains(t, stderr, "No SEGMENT_WRITE_KEY configured",
		"stderr should warn about missing write key")
}

func writeMarkerFetchStubs(t *testing.T, dir string) {
	t.Helper()
	stubs := map[string]string{
		"fetch-tekton-records.sh":      "tekton",
		"fetch-konflux-op-records.sh":  "op",
		"fetch-namespace-records.sh":   "ns",
		"fetch-component-records.sh":   "comp",
		"fetch-application-records.sh": "app",
		"fetch-release-records.sh":     "rel",
	}
	for name, marker := range stubs {
		testfixture.WriteStub(t, dir, name,
			"#!/bin/bash\necho '{\"marker\":\""+marker+"\"}'\n")
	}
}

func writePassthroughDownstream(t *testing.T, dir string) {
	t.Helper()
	testfixture.WriteStub(t, dir, "get-konflux-public-info.sh",
		"#!/bin/bash\nexec \"$@\"\n")
	testfixture.WriteStub(t, dir, "tekton-to-segment.sh",
		"#!/bin/bash\ncat\n")
	testfixture.WriteStub(t, dir, "segment-mass-uploader.sh",
		"#!/bin/bash\ncat\n")
}

func runMainJobWithToggleEnv(t *testing.T, extraEnv ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	writeMarkerFetchStubs(t, dir)
	writePassthroughDownstream(t, dir)
	script := linkMainJob(t, dir)
	env := environWithoutTelemetryToggles(
		"SEGMENT_WRITE_KEY=test-key",
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
	)
	env = append(env, extraEnv...)
	return runMainJobWithEnv(t, script, env)
}

func TestTelemetrySourceToggles(t *testing.T) {
	const defaultLog = "Effective telemetry toggles: FETCH_PIPELINERUNS=true FETCH_OPERATOR=true FETCH_NAMESPACES=true FETCH_COMPONENTS=true FETCH_APPLICATIONS=true FETCH_RELEASES=true EMIT_HEARTBEAT=true"

	tests := []struct {
		name        string
		extraEnv    []string
		wantMarkers []string
		notMarkers  []string
		wantStderr  []string
	}{
		{
			name:        "all enabled by default",
			wantMarkers: []string{"tekton", "op", "ns", "comp", "app", "rel"},
			wantStderr:  []string{defaultLog},
		},
		{
			name:        "single disabled",
			extraEnv:    []string{"FETCH_NAMESPACES=false"},
			wantMarkers: []string{"tekton", "op", "comp", "app", "rel"},
			notMarkers:  []string{`"marker":"ns"`},
			wantStderr: []string{
				"FETCH_NAMESPACES=false",
				"FETCH_PIPELINERUNS=true",
			},
		},
		{
			name: "all disabled",
			extraEnv: []string{
				"FETCH_PIPELINERUNS=false",
				"FETCH_OPERATOR=false",
				"FETCH_NAMESPACES=false",
				"FETCH_COMPONENTS=false",
				"FETCH_APPLICATIONS=false",
				"FETCH_RELEASES=false",
			},
			notMarkers: []string{"marker"},
			wantStderr: []string{
				"FETCH_PIPELINERUNS=false",
				"FETCH_OPERATOR=false",
				"FETCH_NAMESPACES=false",
				"FETCH_COMPONENTS=false",
				"FETCH_APPLICATIONS=false",
				"FETCH_RELEASES=false",
				"EMIT_HEARTBEAT=true",
			},
		},
		{
			name:        "case-insensitive false",
			extraEnv:    []string{"FETCH_OPERATOR=FALSE"},
			wantMarkers: []string{"tekton", "ns", "comp", "app", "rel"},
			notMarkers:  []string{`"marker":"op"`},
			wantStderr:  []string{"FETCH_OPERATOR=false"},
		},
		{
			name:        "invalid value fail-open",
			extraEnv:    []string{"FETCH_COMPONENTS=nope"},
			wantMarkers: []string{"tekton", "op", "ns", "comp", "app", "rel"},
			wantStderr: []string{
				"WARNING: unrecognized value 'nope' for FETCH_COMPONENTS; treating as enabled",
				"FETCH_COMPONENTS=true",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exitCode := runMainJobWithToggleEnv(t, tc.extraEnv...)
			assert.Equal(t, 0, exitCode,
				"main job should exit 0; stderr:\n%s", stderr)

			for _, marker := range tc.wantMarkers {
				assert.Contains(t, stdout, `{"marker":"`+marker+`"}`,
					"expected marker %q in stdout", marker)
			}
			for _, marker := range tc.notMarkers {
				assert.NotContains(t, stdout, marker)
			}
			for _, want := range tc.wantStderr {
				assert.Contains(t, stderr, want)
			}
		})
	}
}

func TestTelemetryTogglesIgnoreHostEnvironment(t *testing.T) {
	t.Setenv("FETCH_NAMESPACES", "false")
	t.Setenv("EMIT_HEARTBEAT", "false")

	stdout, stderr, exitCode := runMainJobWithToggleEnv(t)
	assert.Equal(t, 0, exitCode,
		"main job should exit 0; stderr:\n%s", stderr)
	assert.Contains(t, stdout, `{"marker":"ns"}`,
		"host FETCH_NAMESPACES=false must not disable the default-on fetch")
	assert.Contains(t, stderr, "FETCH_NAMESPACES=true")
	assert.Contains(t, stderr, "EMIT_HEARTBEAT=true")
}

func TestHeartbeatOffThroughMainJob(t *testing.T) {
	dir := t.TempDir()

	pipelineRunJSON := `{"apiVersion":"tekton.dev/v1","kind":"PipelineRun","metadata":{"name":"test-run","namespace":"test-ns","creationTimestamp":"2026-01-01T00:00:00Z"},"status":{"startTime":"2026-01-01T00:00:00Z","completionTime":"2026-01-01T00:05:00Z","conditions":[{"type":"Succeeded","status":"True","reason":"Succeeded"}]},"spec":{"pipelineRef":{"name":"build"}}}`

	testfixture.WriteStub(t, dir, "fetch-tekton-records.sh",
		"#!/bin/bash\necho '"+pipelineRunJSON+"'\n")
	for _, name := range []string{
		"fetch-konflux-op-records.sh",
		"fetch-namespace-records.sh",
		"fetch-component-records.sh",
		"fetch-application-records.sh",
		"fetch-release-records.sh",
	} {
		testfixture.WriteStub(t, dir, name, "#!/bin/bash\n")
	}

	realScriptsDir, err := filepath.Abs("../scripts")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(
		filepath.Join(realScriptsDir, "get-konflux-public-info.sh"),
		filepath.Join(dir, "get-konflux-public-info.sh"),
	))
	require.NoError(t, os.Symlink(
		filepath.Join(realScriptsDir, "tekton-to-segment.sh"),
		filepath.Join(dir, "tekton-to-segment.sh"),
	))
	require.NoError(t, os.Symlink(
		filepath.Join(realScriptsDir, "jq"),
		filepath.Join(dir, "jq"),
	))
	testfixture.WriteStub(t, dir, "segment-mass-uploader.sh",
		"#!/bin/bash\ncat\n")
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
	env := environWithoutTelemetryToggles(
		"SEGMENT_WRITE_KEY=test-key",
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
		"HEARTBEAT_TIMESTAMP=2026-01-01T00:10:00Z",
		"EMIT_HEARTBEAT=false",
	)
	stdout, stderr, exitCode := runMainJobWithEnv(t, script, env)

	assert.Equal(t, 0, exitCode,
		"heartbeat-off test should exit 0; stderr:\n%s", stderr)
	assert.Contains(t, stderr, "EMIT_HEARTBEAT=false")
	assert.NotContains(t, stdout, "Segment Bridge Heartbeat")

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var nonEmpty []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(line))
		}
	}
	require.Len(t, nonEmpty, 2,
		"expected Started + Completed only when heartbeat is disabled")
}
