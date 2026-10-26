package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTektonRecordsLargePageStaysWithinMemoryLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("virtual-memory ulimit is only enforced consistently on Linux")
	}
	const recordCount = 1000
	const payloadBytes = 16 * 1024
	const memoryLimitKiB = 128 * 1024

	payload := strings.Repeat("a", payloadBytes)
	type record struct {
		Name       string `json:"name"`
		CreateTime string `json:"createTime"`
		Data       struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"data"`
	}
	records := make([]record, 0, recordCount)
	for i := 0; i < recordCount; i++ {
		pipelineRun := fmt.Sprintf(
			`{"apiVersion":"tekton.dev/v1","kind":"PipelineRun","metadata":{"name":"pr-%04d","annotations":{"payload":%q}},"status":{"startTime":"2026-10-23T00:00:00Z","completionTime":"2026-10-23T00:01:00Z"}}`,
			i, payload,
		)
		var item record
		item.Name = fmt.Sprintf("scale-ns/results/1/records/%d", i)
		item.CreateTime = "2026-10-23T00:00:00Z"
		item.Data.Type = "tekton.dev/v1.PipelineRun"
		item.Data.Value = base64.StdEncoding.EncodeToString([]byte(pipelineRun))
		records = append(records, item)
	}
	response, err := json.Marshal(map[string]interface{}{
		"records": records,
	})
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response)
	}))
	t.Cleanup(server.Close)

	script := fetchTektonScript
	var stderr bytes.Buffer
	cmd := exec.Command("bash", "-c", `ulimit -v "$MEMORY_LIMIT_KIB" 2>/dev/null || exit 125; exec "$FETCH_SCRIPT"`)
	cmd.Env = append(os.Environ(),
		"FETCH_SCRIPT="+script,
		"MEMORY_LIMIT_KIB="+strconv.Itoa(memoryLimitKiB),
		"TEKTON_RESULTS_API_ADDR="+server.URL,
		"TEKTON_RESULTS_TOKEN=test-token",
		"TEKTON_RESULTS_INSECURE=true",
		"KUBECTL=",
		"TEKTON_LIMIT=1000",
		"TEKTON_MAX_PAGES=1",
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	err = cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 125 {
		t.Skip("this Linux environment does not support ulimit -v")
	}
	require.NoError(t, err, "script exceeded the 128Mi regression limit:\n%s", stderr.String())
	assert.Contains(t, stderr.String(), "TEKTON_RESULTS_INSECURE=true", "test must exercise the non-empty TLS argument path")
}
