package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scaleRecord is a minimal Tekton Results record used to build a large,
// pre-sorted synthetic dataset (see startPagingMockResultsAPI).
type scaleRecord struct {
	name  string // PipelineRun/TaskRun name, also used to assert output order
	isRun bool   // true for PipelineRun, false for TaskRun
}

// encodeScaleRecord base64-encodes a minimal PipelineRun/TaskRun payload,
// mirroring the shape fetch-tekton-records.sh and filter-pipelineruns.jq
// expect (see testdata/records-pipelineruns.json for the real-world shape).
func encodeScaleRecord(r scaleRecord) (string, string) {
	kind := "TaskRun"
	if r.isRun {
		kind = "PipelineRun"
	}
	obj := map[string]interface{}{
		"apiVersion": "tekton.dev/v1",
		"kind":       kind,
		"metadata": map[string]string{
			"name":      r.name,
			"namespace": "scale-ns",
			"uid":       "uid-" + r.name,
		},
	}
	data, err := json.Marshal(obj)
	if err != nil {
		panic(err) // test-data construction only; a marshal failure here is a test bug
	}
	recordType := "tekton.dev/v1.TaskRun"
	if r.isRun {
		recordType = "tekton.dev/v1.PipelineRun"
	}
	return recordType, base64.StdEncoding.EncodeToString(data)
}

// startPagingMockResultsAPI starts an HTTP server that, unlike
// startMockResultsAPIWithCapture, actually honors the page_size query
// parameter: it serves only the first page_size entries of records (which
// callers must pre-sort newest-first, exactly as the real Tekton Results
// API is expected to do server-side for order_by=create_time desc). This
// lets tests verify the script+jq pipeline neither reorders, drops, nor
// duplicates records when the dataset exceeds TEKTON_LIMIT.
func startPagingMockResultsAPI(t *testing.T, records []scaleRecord) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageSize := len(records)
		if ps := r.URL.Query().Get("page_size"); ps != "" {
			n, err := strconv.Atoi(ps)
			require.NoError(t, err, "page_size must be an integer")
			pageSize = n
		}
		if pageSize > len(records) {
			pageSize = len(records)
		}

		type respRecord struct {
			Name string `json:"name"`
			Data struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"data"`
		}
		resp := struct {
			Records []respRecord `json:"records"`
		}{}
		for i := 0; i < pageSize; i++ {
			rec := records[i]
			recordType, value := encodeScaleRecord(rec)
			var rr respRecord
			rr.Name = fmt.Sprintf("scale-ns/results/1/records/%d", i)
			rr.Data.Type = recordType
			rr.Data.Value = value
			resp.Records = append(resp.Records, rr)
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestFetchTektonRecordsLargeDatasetPreservesOrderAndLimit builds a synthetic
// dataset larger than the default TEKTON_LIMIT (100), pre-sorted newest-first
// (as the real Tekton Results API is expected to return for
// order_by=create_time desc), interleaved with TaskRuns. It verifies that
// once the (mock) server applies page_size, the script+jq pipeline:
//   - emits exactly the PipelineRuns present in the first page_size records
//     (no more, no fewer — no accidental over-fetch or truncation),
//   - preserves their relative order end-to-end (no reordering introduced
//     by curl, jq, or the shell pipeline), and
//   - filters out every interleaved TaskRun, even at this larger scale.
//
// This test does NOT verify that the *real* Tekton Results API actually
// sorts by create_time desc across a full dataset larger than one page —
// that requires a real-cluster/live-API check (see PR review discussion).
// It only proves the client side is order- and count-faithful to whatever
// already-paginated, already-sorted response the server hands back.
func TestFetchTektonRecordsLargeDatasetPreservesOrderAndLimit(t *testing.T) {
	const totalRecords = 260
	const taskRunEvery = 4 // every 4th record is a TaskRun, interleaved among PipelineRuns
	const tektonLimit = 100

	records := make([]scaleRecord, 0, totalRecords)
	for i := 0; i < totalRecords; i++ {
		if i%taskRunEvery == 0 {
			records = append(records, scaleRecord{name: fmt.Sprintf("tr-%03d", i), isRun: false})
		} else {
			records = append(records, scaleRecord{name: fmt.Sprintf("pr-%03d", i), isRun: true})
		}
	}

	// Expected output: names of PipelineRuns among the first tektonLimit
	// records only, in the same order — mirrors exactly what a page_size-
	// honoring server would hand back, and what the script must pass through
	// unchanged.
	var wantNames []string
	for i := 0; i < tektonLimit; i++ {
		if records[i].isRun {
			wantNames = append(wantNames, records[i].name)
		}
	}
	require.Less(t, len(wantNames), tektonLimit, "sanity: dataset must include interleaved TaskRuns")
	require.Greater(t, totalRecords, tektonLimit, "sanity: dataset must exceed TEKTON_LIMIT")

	server := startPagingMockResultsAPI(t, records)

	out, err := runFetchTekton(t, map[string]string{
		"TEKTON_RESULTS_TOKEN":    "test-token",
		"TEKTON_RESULTS_API_ADDR": server.URL,
		"TEKTON_LIMIT":            strconv.Itoa(tektonLimit),
	})
	require.NoError(t, err)

	lines := nonEmptyLines(out)
	require.Len(t, lines, len(wantNames),
		"expected exactly the PipelineRuns within the first %d (page_size-limited) records, got %d lines", tektonLimit, len(lines))

	gotNames := make([]string, len(lines))
	for i, line := range lines {
		var obj map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &obj), "line %d must be valid JSON", i)
		assert.Equal(t, "PipelineRun", obj["kind"], "line %d: TaskRun leaked into output", i)
		metadata, ok := obj["metadata"].(map[string]interface{})
		require.True(t, ok, "line %d: missing metadata", i)
		gotNames[i] = metadata["name"].(string)
	}

	assert.Equal(t, wantNames, gotNames,
		"output order/content must exactly match the newest-first, page_size-limited PipelineRuns — "+
			"any mismatch means the pipeline reordered, dropped, or duplicated records at scale")
}
