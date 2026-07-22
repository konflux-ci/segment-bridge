# filter-pipelineruns.jq
# Filter Tekton Results API response: select PipelineRun records, base64-decode
# the payload, and output one PipelineRun JSON object per line.
#
# Input: JSON response from Tekton Results HTTP REST API

.records[]?
| select(.data.type == "tekton.dev/v1.PipelineRun")
| .data.value
| @base64d
| fromjson
