package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"

	"github.com/lithammer/dedent"
	"github.com/redhat-appstudio/segment-bridge.git/scripts"
	"github.com/redhat-appstudio/segment-bridge.git/stats"
	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/redhat-appstudio/segment-bridge.git/webfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Our uploader script shouldn't really care about the contents of the records
// we send so we can get away with emulating them as simple string maps
type testRecord map[string]string

type testCase struct {
	name         string
	data         []testRecord
	dataJsonSize int
	maxBatchSize int
	shouldSplit  bool
}

// Structure of a Segment batch record - for decoding JSON
type segmentBatch struct {
	Batch []map[string]any
}

// requireTools verifies that the external tools needed by the upload scripts
// are available. GNU coreutils split is required because the uploader uses
// --line-bytes and --filter which BSD split does not support.
func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("Required tool %q not found in PATH", tool)
		}
	}
	if _, err := exec.LookPath("split"); err != nil {
		t.Fatal("Required tool \"split\" not found in PATH")
	}
	// GNU split prints version info and exits 0; BSD split rejects --version.
	if err := exec.Command("split", "--version").Run(); err != nil {
		t.Fatal("GNU coreutils split required (need --line-bytes and --filter support)")
	}
}

func requireCurlAndJq(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("Required tool %q not found in PATH", tool)
		}
	}
}

func requireJq(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatalf("Required tool %q not found in PATH", "jq")
	}
}

func runScriptWithFileInput(t *testing.T, scriptPath, inputContent string, env []string) ([]byte, error) {
	t.Helper()
	// Host execution: temp stdin/netrc paths and httptest URLs are not
	// bind-mounted when SEGMENT_BRIDGE_TEST_IMAGE is set (unit_tests CI).
	t.Setenv(testfixture.EnvTestImage, "")
	f, err := os.CreateTemp("", "test-input-*")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	_, err = f.WriteString(inputContent)
	require.NoError(t, err)
	_, err = f.Seek(0, 0)
	require.NoError(t, err)
	defer f.Close()
	return testfixture.RunRepoScript(scriptPath, f, env)
}

func TestMkSegmentBatchPayload(t *testing.T) {
	requireJq(t)
	script, err := scripts.LookPath("mk-segment-batch-payload.sh")
	require.NoError(t, err, "Failed to find script to test")

	output, err := runScriptWithFileInput(
		t, script, "{\"event\":\"test1\"}\n{\"event\":\"test2\"}\n", nil,
	)
	require.NoError(t, err)

	var result segmentBatch
	requireJSONDecode(
		t, string(output), &result,
		"Failed to decode batch payload",
	)
	require.Len(t, result.Batch, 2)
	assert.Equal(t, "test1", result.Batch[0]["event"])
	assert.Equal(t, "test2", result.Batch[1]["event"])
}

func TestMkSegmentBatchPayloadEmptyInput(t *testing.T) {
	requireJq(t)
	script, err := scripts.LookPath("mk-segment-batch-payload.sh")
	require.NoError(t, err, "Failed to find script to test")

	output, err := runScriptWithFileInput(t, script, "", nil)
	require.NoError(t, err)

	var result segmentBatch
	requireJSONDecode(
		t, string(output), &result,
		"Failed to decode empty batch payload",
	)
	assert.Empty(t, result.Batch)
	assert.Equal(t, "{\"batch\":[]}", strings.TrimSpace(string(output)))
}

func TestSegmentUploaderDirect(t *testing.T) {
	requireCurlAndJq(t)
	script, err := scripts.LookPath("segment-uploader.sh")
	require.NoError(t, err, "Failed to find script to test")

	inputContent := "{\"event\":\"test1\"}\n{\"event\":\"test2\"}\n"

	reqs := webfixture.TraceRequestsFrom(func(url string, _ *http.Client) {
		t.Setenv("SEGMENT_BATCH_API", url)
		t.Setenv("SEGMENT_RETRIES", "1")

		netrcPath := filepath.Join(t.TempDir(), ".netrc")
		require.NoError(t, os.WriteFile(
			netrcPath,
			[]byte("machine 127.0.0.1 login test password \"\"\n"),
			0o600,
		))
		t.Setenv("CURL_NETRC", netrcPath)

		_, err := runScriptWithFileInput(t, script, inputContent, nil)
		require.NoError(t, err)
	})

	require.Len(t, reqs, 1)
	assert.Equal(t, "POST", reqs[0].Method)

	var reqData segmentBatch
	requireJSONDecode(
		t, reqs[0].Body, &reqData,
		"Failed to decode sent request JSON",
	)
	require.Len(t, reqData.Batch, 2)
	assert.Equal(t, "test1", reqData.Batch[0]["event"])
	assert.Equal(t, "test2", reqData.Batch[1]["event"])
}

func TestSegmentUploaderMissingNetrc(t *testing.T) {
	requireCurlAndJq(t)
	script, err := scripts.LookPath("segment-uploader.sh")
	require.NoError(t, err, "Failed to find script to test")

	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer svr.Close()

	t.Setenv("SEGMENT_BATCH_API", svr.URL)
	t.Setenv("SEGMENT_RETRIES", "1")
	t.Setenv("CURL_NETRC", filepath.Join(t.TempDir(), "missing-netrc"))

	_, err = runScriptWithFileInput(t, script, "{\"event\":\"test\"}\n", nil)
	require.Error(t, err)
}

func TestUploader(t *testing.T) {
	requireTools(t)
	script, err := scripts.LookPath("segment-mass-uploader.sh")
	if err != nil {
		t.Fatalf("Failed to find script to test: %v", err)
	}
	testCases := mkTestCases(t)
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			reqs := webfixture.TraceRequestsFrom(func(url string, _ *http.Client) {
				t.Setenv("SEGMENT_BATCH_API", url)
				t.Setenv("SEGMENT_BATCH_DATA_SIZE", fmt.Sprintf("%d", tt.maxBatchSize))

				withScriptStdin(t, script, func(stdin io.WriteCloser) {
					err := streamAsJsonLines(stdin, tt.data)
					require.NoError(t, err)
				})
			})

			if tt.shouldSplit {
				assert.Greater(t, len(reqs), 1, "Data should be split to batches")
			}

			requestRecords := 0
			for _, request := range reqs {
				assert.Equal(t, "POST", request.Method, "HTTP method must be POST")

				var reqData segmentBatch
				requireJSONDecode(
					t, request.Body, &reqData,
					"Failed to decode sent request JSON. "+
						"Perhaps not sent with the right structure?",
				)

				requestRecords += len(reqData.Batch)

				// split limits NDJSON line bytes; the uploader wraps each chunk in {"Batch":[...]},
				// so the HTTP body is slightly larger than the raw line sum (commas, brackets).
				slack := 64 + len(reqData.Batch)*4
				assert.LessOrEqual(t, len(request.Body), tt.maxBatchSize+slack,
					"POST body includes Batch JSON beyond split --line-bytes payload")
			}
			assert.Equal(t, len(tt.data), requestRecords, "Wrong number of records sent")
		})
	}
}

func streamAsJsonLines(stream io.Writer, data []testRecord) error {
	enc := json.NewEncoder(stream)
	for _, record := range data {
		if err := enc.Encode(record); err != nil {
			return err
		}
		if _, err := io.WriteString(stream, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func requireJSONDecode(t *testing.T, data string, v any, msgAndArgs ...any) {
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(v)
	require.NoError(t, err, msgAndArgs...)
	require.False(t, decoder.More())
}

func withScriptStdin(t *testing.T, script string, tFunc func(io.WriteCloser)) {
	var cmd *exec.Cmd
	if kcovDir := strings.TrimSpace(os.Getenv(testfixture.EnvKcovOutputDir)); kcovDir != "" {
		if _, err := exec.LookPath("kcov"); err == nil {
			absScript, _ := filepath.Abs(script)
			cmd = exec.Command("kcov",
				"--include-path="+filepath.Dir(absScript),
				kcovDir,
				absScript,
			)
		}
	}
	if cmd == nil {
		cmd = exec.Command(script)
	}
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err, "Failed to connect to script STDIN")
	require.NoError(t, cmd.Start(), "Failed to start script")
	defer func() {
		stdin.Close()
		require.NoError(t, cmd.Wait(), "Script did not finish cleanly")
	}()
	tFunc(stdin)
}

// Use the quick module to generate random test data
func mkTestCases(t *testing.T) (cases []testCase) {
	var records, bytes stats.Series[int]
	err := quick.Check(func(data []testRecord) bool {
		jsonData, err := json.Marshal(data)
		if !assert.NoError(t, err, "Failed to convert test sample to JSON") {
			return true
		}
		cases = append(cases, testCase{
			name:         fmt.Sprintf("records-%d-bytes-%d", len(data), len(jsonData)),
			data:         data,
			dataJsonSize: len(jsonData),
		})
		records.Add(len(data))
		bytes.Add(len(jsonData))
		return true
	}, nil)
	require.NoError(t, err, "Failed to generate test data")
	t.Logf(dedent.Dedent(`
		%d test cases
		Records: %5d
		Bytes:   %5d
	`), records.Len(), records, bytes)
	// Well set the maximum batch size to 45% of the maximum test data chunk
	// so that some samples will get split into 2 and 3 batches.
	maxBatchSize := bytes.Max() * 45 / 100
	for i := range cases {
		cases[i].maxBatchSize = maxBatchSize
		cases[i].shouldSplit = (cases[i].dataJsonSize > maxBatchSize)
	}
	return
}
