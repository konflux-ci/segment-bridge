package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTektonRecordsCursorReadFromConfigMap(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-cursor-mixed.json")

	stubDir := t.TempDir()
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
case "$1" in
  get) echo -n "2024-01-01T12:00:00Z" ;;
  create) cat <<'YAML'
apiVersion: v1
kind: ConfigMap
data:
  last_processed_create_time: "ignored"
YAML
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-new"}, names,
		"cursor read from ConfigMap stub must filter old records")
}

func TestFetchTektonRecordsCursorWriteToConfigMap(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	logFile := filepath.Join(stubDir, "kubectl.log")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
echo "$@" >> "`+logFile+`"
case "$1" in
  get) exit 1 ;;
  create)
    for arg in "$@"; do
      case "$arg" in
        --from-literal=last_processed_create_time=*)
          VALUE="${arg#--from-literal=last_processed_create_time=}"
          printf 'apiVersion: v1\nkind: ConfigMap\ndata:\n  last_processed_create_time: "%s"\n' "$VALUE"
          ;;
      esac
    done
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	logStr := string(logData)

	assert.Contains(t, logStr, "last_processed_create_time=2024-01-01T11:05:29Z",
		"cursor must be advanced to max(createTime) minus 1 second (tie-break overlap backoff)")
}

// TestFetchTektonRecordsCursorWriteDateUnsupported verifies write_cursor's
// final fallback: when both the GNU (`date -d`) and BSD (`date -j`) forms
// for computing the 1-second overlap offset fail, the script must log a
// diagnostic and persist the exact (non-backed-off) cursor rather than
// aborting. A `date` stub that always exits 1 is prepended to PATH so
// neither form can succeed, regardless of which platform runs the test.
func TestFetchTektonRecordsCursorWriteDateUnsupported(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	testfixture.WriteStub(t, stubDir, "date", `#!/bin/bash
exit 1
`)
	logFile := filepath.Join(stubDir, "kubectl.log")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
echo "$@" >> "`+logFile+`"
case "$1" in
  get) exit 1 ;;
  create)
    for arg in "$@"; do
      case "$arg" in
        --from-literal=last_processed_create_time=*)
          VALUE="${arg#--from-literal=last_processed_create_time=}"
          printf 'apiVersion: v1\nkind: ConfigMap\ndata:\n  last_processed_create_time: "%s"\n' "$VALUE"
          ;;
      esac
    done
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	env["PATH"] = stubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	_, stderr, err := runFetchTektonWithStderr(t, env)
	require.NoError(t, err, "write_cursor date failure must remain best-effort and non-fatal")

	assert.Contains(t, string(stderr), "could not compute cursor overlap offset",
		"both GNU and BSD date forms failing must log the fallback diagnostic")

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	assert.Contains(t, string(logData), "last_processed_create_time=2024-01-01T11:05:30Z",
		"when date is unsupported, the exact (non-backed-off) cursor must be persisted, "+
			"not max(createTime) minus 1 second")
}

// TestFetchTektonRecordsCursorWriteReflectsMaxAcrossPages verifies that the
// PAGE_MAX tracking block correctly updates MAX_CREATE_TIME when a later
// page's max createTime exceeds an earlier page's, not just on page 1. The
// fixtures deliberately give page 2 a larger max createTime than page 1 so
// the "[[ "$PAGE_MAX" > "$MAX_CREATE_TIME" ]]" comparison's true branch is
// exercised, and the persisted cursor must reflect page 2's max, not
// page 1's.
func TestFetchTektonRecordsCursorWriteReflectsMaxAcrossPages(t *testing.T) {
	server, _ := startPaginatedMockAPI(t, map[string]string{
		"":              "testdata/records-multipage-max-page1.json",
		"maxpage2token": "testdata/records-multipage-max-page2.json",
	})

	stubDir := t.TempDir()
	logFile := filepath.Join(stubDir, "kubectl.log")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
echo "$@" >> "`+logFile+`"
case "$1" in
  get) exit 1 ;;
  create)
    for arg in "$@"; do
      case "$arg" in
        --from-literal=last_processed_create_time=*)
          VALUE="${arg#--from-literal=last_processed_create_time=}"
          printf 'apiVersion: v1\nkind: ConfigMap\ndata:\n  last_processed_create_time: "%s"\n' "$VALUE"
          ;;
      esac
    done
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-page1", "pr-page2"}, names,
		"both pages must be fetched and their PipelineRuns emitted")

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	assert.Contains(t, string(logData), "last_processed_create_time=2024-02-01T12:59:59Z",
		"persisted cursor must reflect page 2's max createTime (2024-02-01T13:00:00Z) minus "+
			"the 1-second overlap backoff, not page 1's smaller max")
}

func TestFetchTektonRecordsCursorWriteNoop(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-empty.json")

	stubDir := t.TempDir()
	logFile := filepath.Join(stubDir, "kubectl.log")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
echo "$@" >> "`+logFile+`"
case "$1" in
  get) exit 1 ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	lines := nonEmptyLines(logData)

	for _, line := range lines {
		assert.NotContains(t, line, "create configmap",
			"empty response must not attempt to write cursor")
	}
}

// TestFetchTektonRecordsCursorWriteSkippedOnMaxPages verifies that when
// pagination is truncated by TEKTON_MAX_PAGES (the run never reaches the
// end of the result set nor the cursor boundary), the script does NOT
// persist a cursor. The fixture records-page1.json always carries a
// nextPageToken, so with TEKTON_MAX_PAGES=2 the guard kicks in before
// pagination completes cleanly.
func TestFetchTektonRecordsCursorWriteSkippedOnMaxPages(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-page1.json")

	stubDir := t.TempDir()
	logFile := filepath.Join(stubDir, "kubectl.log")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
echo "$@" >> "`+logFile+`"
case "$1" in
  get) exit 1 ;;
  create)
    for arg in "$@"; do
      case "$arg" in
        --from-literal=last_processed_create_time=*)
          VALUE="${arg#--from-literal=last_processed_create_time=}"
          printf 'apiVersion: v1\nkind: ConfigMap\ndata:\n  last_processed_create_time: "%s"\n' "$VALUE"
          ;;
      esac
    done
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	env["TEKTON_MAX_PAGES"] = "2"
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	lines := nonEmptyLines(logData)

	for _, line := range lines {
		assert.NotContains(t, line, "create configmap",
			"truncated run (max pages hit) must not persist a cursor")
	}
}

// TestFetchTektonRecordsCursorSkipWarnsOnMaxPages verifies that when the
// cursor write is skipped due to TEKTON_MAX_PAGES truncation, a clear WARN
// diagnostic explaining why is emitted on stderr.
func TestFetchTektonRecordsCursorSkipWarnsOnMaxPages(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-page1.json")

	stubDir := t.TempDir()
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
case "$1" in
  get) exit 1 ;;
  create) cat <<'YAML'
apiVersion: v1
kind: ConfigMap
data:
  last_processed_create_time: "ignored"
YAML
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	env["TEKTON_MAX_PAGES"] = "2"
	_, stderr, err := runFetchTektonWithStderr(t, env)
	require.NoError(t, err)

	assert.Contains(t, string(stderr),
		"WARN fetch-tekton-records.sh: cursor NOT advanced",
		"truncated run must warn that the cursor was not advanced")
	assert.Contains(t, string(stderr), "TEKTON_MAX_PAGES",
		"warning must explain the cursor skip was caused by TEKTON_MAX_PAGES truncation")
}

func TestFetchTektonRecordsNoKubectl(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	env := baseEnv(server.URL)
	env["KUBECTL"] = ""
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	lines := nonEmptyLines(out)
	assert.Len(t, lines, 2,
		"without kubectl, cursor is disabled — all records output")
}

// TestFetchTektonRecordsAutoDetectUsesOcFallback verifies the KUBECTL
// auto-detection block's "elif command -v oc" branch: when KUBECTL is truly
// unset (not exported as "") and only an oc binary (not kubectl) is on
// PATH, the script must auto-detect and invoke oc to read the cursor
// ConfigMap. MinimalHostEnvWithOcOnly builds a hermetic PATH containing only
// the interpreters the script needs plus the oc stub, so this holds even on
// a dev machine where a real kubectl/oc is installed system-wide.
func TestFetchTektonRecordsAutoDetectUsesOcFallback(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-cursor-mixed.json")

	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "oc.log")
	env := testfixture.MinimalHostEnvWithOcOnly(t, `#!/bin/bash
echo "$@" >> "`+logFile+`"
case "$1" in
  get) echo -n "2024-01-01T12:00:00Z" ;;
  create) cat <<'YAML'
apiVersion: v1
kind: ConfigMap
data:
  last_processed_create_time: "ignored"
YAML
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)
	env = append(env,
		"TEKTON_RESULTS_TOKEN=test-token",
		"TEKTON_RESULTS_API_ADDR="+server.URL,
	)
	// KUBECTL is intentionally left unset (not set to "") so the
	// auto-detection block runs and must fall back to oc.

	out, err := testfixture.RunRepoScript(fetchTektonScript, nil, env)
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-new"}, names,
		"cursor read via the oc fallback must filter out records at/before the cursor, "+
			"proving the auto-detected oc binary was actually used")

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	assert.Contains(t, string(logData), "get configmap",
		"oc (not kubectl) must have been invoked to read the cursor ConfigMap")
}

// TestFetchTektonRecordsAutoDetectNeitherKubectlNorOc verifies the KUBECTL
// auto-detection block's final "else" branch: when KUBECTL is truly unset
// and neither kubectl nor oc is on PATH, cursor persistence must silently
// no-op (KUBECTL="") rather than error, and the script must still succeed.
func TestFetchTektonRecordsAutoDetectNeitherKubectlNorOc(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	env := testfixture.MinimalHostEnvWithoutKubectl(t)
	env = append(env,
		"TEKTON_RESULTS_TOKEN=test-token",
		"TEKTON_RESULTS_API_ADDR="+server.URL,
	)
	// KUBECTL is intentionally left unset (not set to "") so the
	// auto-detection block runs and must fall through to the "neither
	// found" branch.

	out, err := testfixture.RunRepoScript(fetchTektonScript, nil, env)
	require.NoError(t, err)

	lines := nonEmptyLines(out)
	assert.Len(t, lines, 2,
		"without kubectl or oc on PATH, cursor persistence must silently no-op — all records output")
}

// TestFetchTektonRecordsCursorBackoffPreventsDataLoss simulates two
// consecutive runs: the first persists a cursor backed off by 1 second from
// the true max(createTime), and the second (using that backed-off cursor via
// TEKTON_CURSOR) must still include the record that carried the original,
// pre-backoff max createTime. This proves the 1-second overlap window
// prevents the tie-break record from being permanently dropped.
func TestFetchTektonRecordsCursorBackoffPreventsDataLoss(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	logFile := filepath.Join(stubDir, "kubectl.log")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
echo "$@" >> "`+logFile+`"
case "$1" in
  get) exit 1 ;;
  create)
    for arg in "$@"; do
      case "$arg" in
        --from-literal=last_processed_create_time=*)
          VALUE="${arg#--from-literal=last_processed_create_time=}"
          printf 'apiVersion: v1\nkind: ConfigMap\ndata:\n  last_processed_create_time: "%s"\n' "$VALUE"
          ;;
      esac
    done
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	// Run 1 (cold start): the true max createTime in this fixture is
	// pr-2's 2024-01-01T11:05:30Z. The persisted cursor must be backed off
	// by 1 second to 2024-01-01T11:05:29Z.
	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	require.Contains(t, string(logData), "last_processed_create_time=2024-01-01T11:05:29Z",
		"run 1 must persist a cursor backed off by 1 second from the true max")

	// Run 2: use the backed-off cursor persisted by run 1. pr-2, whose
	// createTime equals the *original* (pre-backoff) max, must still be
	// emitted — it is not permanently excluded despite the cursor having
	// already "passed" its timestamp once.
	env2 := baseEnv(server.URL)
	env2["TEKTON_CURSOR"] = "2024-01-01T11:05:29Z"
	out2, err := runFetchTekton(t, env2)
	require.NoError(t, err)

	names := pipelineRunNames(t, out2)
	assert.Contains(t, names, "pr-2",
		"record at the original (pre-backoff) max createTime must not be dropped by the overlap window")
}

func TestFetchTektonRecordsCursorReadFailureLogsDiagnostic(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
case "$1" in
  get) exit 1 ;;
  create) cat <<'YAML'
apiVersion: v1
kind: ConfigMap
data:
  last_processed_create_time: "ignored"
YAML
    ;;
  label) cat ;;
  apply) cat > /dev/null ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, stderr, err := runFetchTektonWithStderr(t, env)
	require.NoError(t, err, "read_cursor failure must remain best-effort and non-fatal")

	assert.Contains(t, string(stderr), "could not read cursor ConfigMap",
		"read_cursor failure must be logged to stderr")
}

func TestFetchTektonRecordsCursorWriteFailureLogsDiagnostic(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
case "$1" in
  get) exit 1 ;;
  create) cat <<'YAML'
apiVersion: v1
kind: ConfigMap
data:
  last_processed_create_time: "ignored"
YAML
    ;;
  label) cat ;;
  apply) exit 1 ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, stderr, err := runFetchTektonWithStderr(t, env)
	require.NoError(t, err, "write_cursor failure must remain best-effort and non-fatal")

	assert.Contains(t, string(stderr), "could not persist cursor ConfigMap",
		"write_cursor failure must be logged to stderr")
}

// TestFetchTektonRecordsCursorWriteIncludesLabel verifies that write_cursor
// pipes the generated ConfigMap through "kubectl label --local" before
// applying it, so the ConfigMap actually sent to the cluster carries the
// standard app.kubernetes.io/name=segment-bridge label used by every other
// manifest under config/base/.
func TestFetchTektonRecordsCursorWriteIncludesLabel(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-pipelineruns.json")

	stubDir := t.TempDir()
	appliedFile := filepath.Join(stubDir, "applied.yaml")
	testfixture.WriteStub(t, stubDir, "kubectl", `#!/bin/bash
case "$1" in
  get) exit 1 ;;
  create) cat <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: segment-bridge-cursor
data:
  last_processed_create_time: "ignored"
YAML
    ;;
  label)
    cat
    echo "  app.kubernetes.io/name: segment-bridge"
    ;;
  apply) cat > "`+appliedFile+`" ;;
esac
`)

	env := baseEnv(server.URL)
	env["KUBECTL"] = filepath.Join(stubDir, "kubectl")
	_, err := runFetchTekton(t, env)
	require.NoError(t, err)

	applied, err := os.ReadFile(appliedFile)
	require.NoError(t, err)
	assert.Contains(t, string(applied), "app.kubernetes.io/name: segment-bridge",
		"the ConfigMap applied to the cluster must carry the standard segment-bridge label")
}
