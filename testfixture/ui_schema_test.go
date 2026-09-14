package testfixture

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type uiSchema struct {
	Definitions struct {
		CommonFields commonFieldsSchema `json:"CommonFields"`
	} `json:"$defs"`
}

type commonFieldsSchema struct {
	Properties map[string]fieldSchema `json:"properties"`
	Required   []string               `json:"required"`
}

type fieldSchema struct {
	Type      string `json:"type"`
	MinLength int    `json:"minLength"`
}

func TestUISchemaClusterVersion(t *testing.T) {
	schema := loadUISchema(t)
	clusterVersion := schema.Definitions.CommonFields.Properties["clusterVersion"]

	assert.Equal(t, "string", clusterVersion.Type)
	assert.Equal(t, 1, clusterVersion.MinLength)
	assert.NotContains(t, schema.Definitions.CommonFields.Required, "clusterVersion")

	tests := []struct {
		name    string
		event   map[string]string
		wantErr string
	}{
		{
			name:  "preserves an available version",
			event: map[string]string{"clusterVersion": "4.16.1"},
		},
		{
			name:  "allows an unavailable version to be omitted",
			event: map[string]string{},
		},
		{
			name:    "rejects an empty version",
			event:   map[string]string{"clusterVersion": ""},
			wantErr: "clusterVersion must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantErr, validateClusterVersion(schema.Definitions.CommonFields, tt.event))
		})
	}
}

func loadUISchema(t *testing.T) uiSchema {
	t.Helper()

	contents, err := os.ReadFile("../schema/ui.json")
	require.NoError(t, err)

	var schema uiSchema
	require.NoError(t, json.Unmarshal(contents, &schema))
	return schema
}

func validateClusterVersion(schema commonFieldsSchema, event map[string]string) string {
	for _, field := range schema.Required {
		if field == "clusterVersion" {
			if _, present := event[field]; !present {
				return "clusterVersion is required"
			}
		}
	}

	version, present := event["clusterVersion"]
	if !present {
		return ""
	}
	if schema.Properties["clusterVersion"].MinLength > len(version) {
		return "clusterVersion must not be empty"
	}

	return ""
}
