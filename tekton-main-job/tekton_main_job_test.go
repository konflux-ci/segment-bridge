package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/containerfixture"
	"github.com/redhat-appstudio/segment-bridge.git/kwok"
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

func environWithoutTelemetryToggles(additionalDrop []string, extra ...string) []string {
	drop := make(map[string]struct{}, len(telemetryToggleVars)+len(additionalDrop))
	for _, k := range telemetryToggleVars {
		drop[k] = struct{}{}
	}
	for _, k := range additionalDrop {
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

// withKwokCluster starts kwok via containerfixture and points KUBECONFIG at it
// for script tests that must use the project's containerized cluster helpers.
func withKwokCluster(t *testing.T, fn func(t *testing.T)) {
	t.Helper()
	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))
		fn(t)
	})
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
		[]string{"SEGMENT_WRITE_KEY", "SEGMENT_BATCH_API"},
		"SEGMENT_WRITE_KEY=test-key",
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
	)
	return runMainJobWithEnv(t, scriptPath, env)
}

func TestSourcedStartupHelperCannotReadSegmentWriteKey(t *testing.T) {
	const (
		syntheticKey  = "TEST_SEGMENT_SOURCE_PROBE_KEY"
		probeFileEnv  = "TEST_SEGMENT_SOURCE_PROBE_FILE"
		toggleFileEnv = "TEST_SEGMENT_TOGGLE_FILE"
	)
	scriptDir := t.TempDir()
	libDir := filepath.Join(scriptDir, "lib")
	require.NoError(t, os.Mkdir(libDir, 0o755))

	scriptSource, err := filepath.Abs(mainJobScript)
	require.NoError(t, err)
	script, err := os.ReadFile(scriptSource)
	require.NoError(t, err)
	scriptPath := filepath.Join(scriptDir, "tekton-main-job.sh")
	require.NoError(t, os.WriteFile(scriptPath, script, 0o755))

	toggleSource, err := filepath.Abs("../scripts/lib/toggle.sh")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(libDir, "toggle.sh"), []byte(`#!/bin/bash
if [[ -n "${SEGMENT_WRITE_KEY:-}" || -n "${segment_write_key:-}" ]]; then
  printf present > "$`+probeFileEnv+`"
else
  printf absent > "$`+probeFileEnv+`"
fi
source "$`+toggleFileEnv+`"
`), 0o755))

	testfixture.WriteStub(t, scriptDir, "get-konflux-public-info.sh", `#!/bin/bash
exec "$@"
`)
	testfixture.WriteStub(t, scriptDir, "tekton-to-segment.sh", `#!/bin/bash
cat > /dev/null
`)
	testfixture.WriteStub(t, scriptDir, "segment-mass-uploader.sh", `#!/bin/bash
exit 0
`)

	env := environWithoutTelemetryToggles(
		[]string{"PATH", "SEGMENT_WRITE_KEY", "SEGMENT_BATCH_API", probeFileEnv, toggleFileEnv, "SHELLOPTS"},
		"PATH="+scriptDir+":"+os.Getenv("PATH"),
		"SEGMENT_WRITE_KEY="+syntheticKey,
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
		probeFileEnv+"="+filepath.Join(scriptDir, "source-probe"),
		toggleFileEnv+"="+toggleSource,
		"FETCH_PIPELINERUNS=false",
		"FETCH_OPERATOR=false",
		"FETCH_NAMESPACES=false",
		"FETCH_COMPONENTS=false",
		"FETCH_APPLICATIONS=false",
		"FETCH_RELEASES=false",
		"EMIT_HEARTBEAT=false",
	)
	stdout, stderr, exitCode := runMainJobWithEnv(t, scriptPath, env)

	assert.Equal(t, 0, exitCode, "main job should finish; stderr:\n%s", stderr)
	probe, err := os.ReadFile(filepath.Join(scriptDir, "source-probe"))
	require.NoError(t, err, "read sourced helper probe")
	assert.Equal(t, "absent", string(probe), "sourced helper must not see either Segment key variable")
	assert.NotContains(t, stdout+stderr, syntheticKey)
}

func TestSegmentWriteKeyNotInheritedByStartupHelpers(t *testing.T) {
	const (
		syntheticKey     = "TEST_SEGMENT_PROBE_KEY"
		probeDirEnv      = "TEST_SEGMENT_PROBE_DIR"
		expectedNetrcEnv = "TEST_SEGMENT_EXPECTED_NETRC"
	)
	probeDir := t.TempDir()
	binDir := t.TempDir()
	expectedNetrc := filepath.Join(probeDir, "expected.netrc")
	require.NoError(t, os.WriteFile(
		expectedNetrc,
		[]byte("machine example.com login "+syntheticKey+" password \"\"\n"),
		0o600,
	))

	for _, helper := range []struct {
		name       string
		executable string
	}{
		{name: "dirname", executable: "/usr/bin/dirname"},
		{name: "readlink", executable: "/usr/bin/readlink"},
		{name: "mktemp", executable: "/usr/bin/mktemp"},
	} {
		testfixture.WriteStub(t, binDir, helper.name, `#!/bin/bash
if [[ -n "${SEGMENT_WRITE_KEY:-}" || -n "${segment_write_key:-}" ]]; then
  printf 'present' > "${`+probeDirEnv+`}/`+helper.name+`"
else
  printf 'absent' > "${`+probeDirEnv+`}/`+helper.name+`"
fi
exec `+helper.executable+` "$@"
`)
	}

	testfixture.WriteStub(t, binDir, "get-konflux-public-info.sh", `#!/bin/bash
exec "$@"
`)
	testfixture.WriteStub(t, binDir, "tekton-to-segment.sh", `#!/bin/bash
cat > /dev/null
`)
	testfixture.WriteStub(t, binDir, "segment-mass-uploader.sh", `#!/bin/bash
if [[ -n "${SEGMENT_WRITE_KEY:-}" || -n "${segment_write_key:-}" ]]; then
  echo "Segment write key inherited by uploader" >&2
  exit 1
fi
if [[ -z "${CURL_NETRC:-}" || ! -r "$CURL_NETRC" ]]; then
  echo "CURL_NETRC is not readable" >&2
  exit 1
fi
if ! /usr/bin/cmp -s "$CURL_NETRC" "${`+expectedNetrcEnv+`}"; then
  echo "CURL_NETRC contents did not match" >&2
  exit 1
fi
case "$(/usr/bin/uname -s)" in
  Darwin) mode=$(/usr/bin/stat -f '%Lp' "$CURL_NETRC") ;;
  *) mode=$(/usr/bin/stat -c '%a' "$CURL_NETRC") ;;
esac
if [[ "$mode" != 600 ]]; then
  echo "CURL_NETRC mode was not 0600" >&2
  exit 1
fi
printf 'uploader-ok\n'
`)

	script := linkMainJob(t, binDir)
	env := environWithoutTelemetryToggles(
		[]string{"PATH", "SEGMENT_WRITE_KEY", "SEGMENT_BATCH_API", probeDirEnv, expectedNetrcEnv, "SHELLOPTS"},
		"PATH="+binDir+":"+os.Getenv("PATH"),
		probeDirEnv+"="+probeDir,
		expectedNetrcEnv+"="+expectedNetrc,
		"SEGMENT_WRITE_KEY="+syntheticKey,
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
		"SHELLOPTS=allexport:xtrace",
		"FETCH_PIPELINERUNS=false",
		"FETCH_OPERATOR=false",
		"FETCH_NAMESPACES=false",
		"FETCH_COMPONENTS=false",
		"FETCH_APPLICATIONS=false",
		"FETCH_RELEASES=false",
		"EMIT_HEARTBEAT=false",
	)
	stdout, stderr, exitCode := runMainJobWithEnv(t, script, env)

	assert.Equal(t, 0, exitCode, "main job should finish; stderr:\n%s", stderr)
	for _, helper := range []string{"dirname", "readlink", "mktemp"} {
		probe, err := os.ReadFile(filepath.Join(probeDir, helper))
		require.NoError(t, err, "read %s environment probe", helper)
		assert.Equal(t, "absent", string(probe),
			"%s must not inherit either Segment write key variable", helper)
	}
	assert.Contains(t, stdout, "uploader-ok")
	assert.NotContains(t, stdout+stderr, syntheticKey,
		"the synthetic write key must not appear in captured output")
}

func TestBestEffortFetchSources(t *testing.T) {
	containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) {
		require.NoError(t, kwok.SetKubeconfigWithPort(deployment.WebPort))
		dir := t.TempDir()

		// -- Setup and fetch stubs --
		testfixture.WriteStub(t, dir, "mktemp", `#!/bin/bash
if [[ -n "${SEGMENT_WRITE_KEY:-}" ]]; then
  echo "SEGMENT_WRITE_KEY inherited by mktemp" >&2
  exit 1
fi
exec /usr/bin/mktemp "$@"
`)

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
			`#!/bin/bash
if [[ -n "${SEGMENT_WRITE_KEY:-}" ]]; then
  echo "SEGMENT_WRITE_KEY inherited by uploader" >&2
  exit 1
fi
cat
`)

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
		assert.NotContains(t, stderr, "test-key",
			"Segment write key should not appear in stderr")
	})
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

const samplePipelineRunJSON = `{"apiVersion":"tekton.dev/v1","kind":"PipelineRun","metadata":{"name":"test-run","namespace":"test-ns","creationTimestamp":"2026-01-01T00:00:00Z"},"status":{"startTime":"2026-01-01T00:00:00Z","completionTime":"2026-01-01T00:05:00Z","conditions":[{"type":"Succeeded","status":"True","reason":"Succeeded"}]},"spec":{"pipelineRef":{"name":"build"}}}`

// writeRealTransformPipeline stubs fetches (one PipelineRun, other sources
// empty) and symlinks the real get-konflux-public-info.sh, tekton-to-segment.sh,
// and jq/ so the main-job pipe runs the real transform without a cluster.
func writeRealTransformPipeline(t *testing.T, dir string) {
	t.Helper()
	testfixture.WriteStub(t, dir, "fetch-tekton-records.sh",
		"#!/bin/bash\necho '"+samplePipelineRunJSON+"'\n")
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
	for _, name := range []string{
		"get-konflux-public-info.sh",
		"tekton-to-segment.sh",
		"jq",
	} {
		require.NoError(t, os.Symlink(
			filepath.Join(realScriptsDir, name),
			filepath.Join(dir, name),
		))
	}
	testfixture.WriteStub(t, dir, "segment-mass-uploader.sh",
		"#!/bin/bash\ncat\n")
	// kubectl stub: get-konflux-public-info.sh reads kube-system uid.
	testfixture.WriteStub(t, dir, "kubectl", `#!/bin/bash
if [[ "$*" == *"configmap"* ]]; then
  exit 1
elif [[ "$*" == *"namespace kube-system"* ]]; then
  echo "test-cluster-uid"
  exit 0
fi
exit 1
`)
}

func runRealTransformPipeline(t *testing.T, extraEnv ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	writeRealTransformPipeline(t, dir)
	env := environWithoutTelemetryToggles(
		[]string{"SEGMENT_WRITE_KEY", "SEGMENT_BATCH_API", "HEARTBEAT_TIMESTAMP"},
		"SEGMENT_WRITE_KEY=test-key",
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
		"HEARTBEAT_TIMESTAMP=2026-01-01T00:10:00Z",
	)
	env = append(env, extraEnv...)
	return runMainJobWithEnv(t, linkMainJob(t, dir), env)
}

// TestRealTransformPipeline runs the main job through the real transform
// (heartbeat on). Stubbed fetches emit one PipelineRun; stdout should include
// Started, Completed, and a heartbeat.
func TestRealTransformPipeline(t *testing.T) {
	stdout, stderr, exitCode := runRealTransformPipeline(t)
	assert.Equal(t, 0, exitCode,
		"real-pipeline test should exit 0; stderr:\n%s", stderr)

	nonEmpty := testfixture.TrimNonEmptyLines(stdout)
	require.GreaterOrEqual(t, len(nonEmpty), 3,
		"expected at least 3 output lines (Started + Completed + Heartbeat)")

	for _, line := range nonEmpty {
		var event map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &event),
			"each output line must be valid JSON")
	}
}

// TestHeartbeatOffThroughMainJob is TestRealTransformPipeline with
// EMIT_HEARTBEAT=false. FETCH_* tests stub tekton-to-segment.sh as `cat`, so
// they cannot prove heartbeat suppression; this uses the real transform.
func TestHeartbeatOffThroughMainJob(t *testing.T) {
	withKwokCluster(t, func(t *testing.T) {
		stdout, stderr, exitCode := runRealTransformPipeline(t, "EMIT_HEARTBEAT=false")
		assert.Equal(t, 0, exitCode,
			"heartbeat-off test should exit 0; stderr:\n%s", stderr)
		assert.Contains(t, stderr, "EMIT_HEARTBEAT=false")
		assert.NotContains(t, stdout, "Segment Bridge Heartbeat")
		require.Len(t, testfixture.TrimNonEmptyLines(stdout), 2,
			"expected Started + Completed only when heartbeat is disabled")
	})
}

const noKeyHostSyntheticKey = "TEST_SEGMENT_HOST_SYNTHETIC_KEY"

func TestNoSegmentWriteKey(t *testing.T) {
	t.Setenv("SEGMENT_WRITE_KEY", noKeyHostSyntheticKey)
	runTestNoSegmentWriteKey(t)
}

func runTestNoSegmentWriteKey(t *testing.T) {
	t.Helper()
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

	// Drop any inherited key so the no-key branch is exercised deterministically.
	env := environWithoutTelemetryToggles(
		[]string{"SEGMENT_WRITE_KEY", "SEGMENT_BATCH_API"},
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
	)
	stdout, stderr, exitCode := runMainJobWithEnv(t, script, env)

	assert.Equal(t, 0, exitCode,
		"main job should exit 0 when SEGMENT_WRITE_KEY is not set; stderr:\n%s", stderr)
	assert.Empty(t, strings.TrimSpace(stdout),
		"no upload output expected when SEGMENT_WRITE_KEY is absent")
	assert.Contains(t, stderr, "No SEGMENT_WRITE_KEY configured",
		"stderr should warn about missing write key")
	assert.NotContains(t, stdout+stderr, noKeyHostSyntheticKey,
		"the synthetic host key must not appear in captured output")
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
		[]string{"SEGMENT_WRITE_KEY", "SEGMENT_BATCH_API"},
		"SEGMENT_WRITE_KEY=test-key",
		"SEGMENT_BATCH_API=https://example.com/v1/batch",
	)
	env = append(env, extraEnv...)
	return runMainJobWithEnv(t, script, env)
}

func TestTelemetrySourceToggles(t *testing.T) {
	withKwokCluster(t, func(t *testing.T) {
		runTelemetrySourceToggles(t)
	})
}

func runTelemetrySourceToggles(t *testing.T) {
	t.Helper()
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
				"WARNING: unrecognized value 'nope' for FETCH_COMPONENTS; treating as enabled (use false to disable)",
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
	withKwokCluster(t, func(t *testing.T) {
		t.Setenv("FETCH_NAMESPACES", "false")
		t.Setenv("EMIT_HEARTBEAT", "false")

		stdout, stderr, exitCode := runMainJobWithToggleEnv(t)
		assert.Equal(t, 0, exitCode,
			"main job should exit 0; stderr:\n%s", stderr)
		assert.Contains(t, stdout, `{"marker":"ns"}`,
			"host FETCH_NAMESPACES=false must not disable the default-on fetch")
		assert.Contains(t, stderr, "FETCH_NAMESPACES=true")
		assert.Contains(t, stderr, "EMIT_HEARTBEAT=true")
	})
}
