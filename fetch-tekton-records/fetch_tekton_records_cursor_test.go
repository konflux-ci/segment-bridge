package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTektonRecordsCursorFilters(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-cursor-mixed.json")

	env := baseEnv(server.URL)
	env["TEKTON_CURSOR"] = "2024-01-01T12:00:00Z"
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-new"}, names,
		"cursor must filter out records with createTime <= cursor")
}

func TestFetchTektonRecordsCursorExactMatch(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-cursor-mixed.json")

	env := baseEnv(server.URL)
	env["TEKTON_CURSOR"] = "2024-01-01T14:00:00Z"
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	lines := nonEmptyLines(out)
	assert.Empty(t, lines,
		"cursor equal to newest createTime must produce no output")
}

func TestFetchTektonRecordsColdStart(t *testing.T) {
	server, _ := startMockResultsAPIWithCapture(t, "testdata/records-cursor-mixed.json")

	out, err := runFetchTekton(t, baseEnv(server.URL))
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-new", "pr-old", "pr-oldest"}, names,
		"cold start (no cursor) must emit all PipelineRuns")
}

func TestFetchTektonRecordsCursorStopsPagination(t *testing.T) {
	server, handler := startPaginatedMockAPI(t, map[string]string{
		"":           "testdata/records-page1.json",
		"page2token": "testdata/records-page2.json",
	})

	env := baseEnv(server.URL)
	env["TEKTON_CURSOR"] = "2024-01-01T11:30:00Z"
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-1"}, names,
		"page 1 records are newer than cursor, page 1 has nextPageToken "+
			"but no record <= cursor yet, so page 2 is fetched; "+
			"page 2 record (11:00) <= cursor stops pagination and is filtered out")

	reqs := handler.getRequests()
	assert.Len(t, reqs, 2,
		"page 2 is needed because page 1 has no record with createTime <= cursor")
}

func TestFetchTektonRecordsCursorHitsOnFirstPage(t *testing.T) {
	server, handler := startPaginatedMockAPI(t, map[string]string{
		"":           "testdata/records-cursor-mixed-page1.json",
		"page2token": "testdata/records-page2.json",
	})

	env := baseEnv(server.URL)
	env["TEKTON_CURSOR"] = "2024-01-01T12:00:00Z"
	out, err := runFetchTekton(t, env)
	require.NoError(t, err)

	names := pipelineRunNames(t, out)
	assert.Equal(t, []string{"pr-new"}, names,
		"cursor hit on first page stops pagination early")

	reqs := handler.getRequests()
	assert.Len(t, reqs, 1,
		"must not fetch page 2 when cursor is reached on page 1")
}
