package main

import (
	"encoding/json"
	"testing"

	"github.com/redhat-appstudio/segment-bridge.git/testfixture"
	"github.com/stretchr/testify/assert"
)

const (
	scriptPath      = "../scripts/splunk-to-segment.sh"
	filePathSuccess = "sample/fetchujrecordsPass"
	filePathFail    = "sample/fetchujrecordsFail"
)

type ExpectedOutput struct {
	MessageID  string                 `json:"messageId"`
	Timestamp  string                 `json:"timestamp"`
	Namespace  string                 `json:"namespace"`
	Type       string                 `json:"type"`
	UserID     interface{}            `json:"userId"`
	Event      string                 `json:"event"`
	Properties map[string]interface{} `json:"properties"`
	Context    map[string]interface{} `json:"context"`
}

func isValidOutput(output []byte) bool {
	var result ExpectedOutput
	err := json.Unmarshal(output, &result)
	if err != nil {
		return false
	}

	if result.MessageID == "" || result.Timestamp == "" || result.Namespace == "" ||
		result.Type == "" || result.Event == "" || result.UserID == nil ||
		result.Properties == nil || len(result.Properties) == 0 ||
		result.Context == nil || len(result.Context) == 0 {
		return false
	}

	for _, mapValue := range []map[string]interface{}{result.Properties, result.Context} {
		for _, value := range mapValue {
			if value == nil {
				return false
			}
		}
	}

	return true
}

func runAndValidateScript(t *testing.T, filePath, scriptPath string) bool {
	output, err := testfixture.RunScriptWithInputFile(filePath, scriptPath)
	if err != nil {
		return false
	}

	return isValidOutput(output)
}

func TestSplunkToSegment(t *testing.T) {

	t.Run("PassPath", func(t *testing.T) {
		assert.True(t, runAndValidateScript(t, filePathSuccess, scriptPath), "Script validation failed for PassPath")
	})

	t.Run("FailPath", func(t *testing.T) {
		assert.False(t, runAndValidateScript(t, filePathFail, scriptPath), "Script validation did not fail for FailPath as expected")
	})
}
