---
name: Bug report
about: Report a defect in the segment-bridge CronJob, scripts, or upload pipeline
title: "[BUG] "
labels: bug
assignees: ''
---

## What happened

<!-- A clear description of the unexpected behavior. -->

## What you expected to happen

<!-- What should have happened instead. -->

## Steps to reproduce

<!--
If reproducible from a CronJob run, include the job name and timestamp
so the logs can be retrieved. If reproducible locally, include the
exact `podman run` or `go test` invocation.
-->

1.
2.
3.

## Environment

- Image tag or git SHA:
- Cluster (e.g. Konflux prod / staging / local kwok):
- `TEKTON_RESULTS_API_ADDR`:
- `SEGMENT_BATCH_API`:
- Relevant `mise.toml` versions if running locally
  (`mise --version` + `mise list`):

## Logs

<!--
Paste the smallest log excerpt that shows the failure. Redact any
tokens, write keys, or netrc material.
-->

```text

```

## Additional context

<!-- Links to related Jira tickets (KFLUXVNGD-*, RHTAPWATCH-*),
similar past incidents, or upstream Segment / Tekton bugs. -->
