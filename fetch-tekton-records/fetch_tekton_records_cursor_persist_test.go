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
