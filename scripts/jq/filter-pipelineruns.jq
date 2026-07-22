# filter-pipelineruns.jq
# Filter Tekton Results API response: select PipelineRun records, base64-decode
# the payload, and output one PipelineRun JSON object per line.
#
# When $cursor is non-empty, records with createTime <= $cursor are skipped
# (already processed in a previous run). Records without createTime always pass.
# Caller must supply --arg cursor "..." (use "" to disable cursor filtering).
#
# Input: JSON response from Tekton Results HTTP REST API

.records[]?
| select(
    ($cursor | length) == 0 or
    .createTime == null or
    .createTime > $cursor
  )
| select(.data.type == "tekton.dev/v1.PipelineRun")
| .data.value
| @base64d
| fromjson
