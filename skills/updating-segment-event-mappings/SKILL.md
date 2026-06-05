---
name: updating-segment-event-mappings
description: >-
  Guides changes to the jq transforms in scripts/jq/ that transform NDJSON
  records into Segment batch events. Covers updating field mappings, adding new
  event properties, and regenerating the sample fixtures. Use when the user
  asks to change what data is sent to Segment, add a new event property, or
  modify the Segment event schema.
---

# Updating Segment event mappings

## How the transform works

`scripts/tekton-to-segment.sh` reads enriched NDJSON from stdin (output of
`get-konflux-public-info.sh`) and uses a `jq` pipeline to produce Segment
`track` events. Each input line becomes one output event.

The jq transform logic lives in **standalone `.jq` files** under
`scripts/jq/`, loaded by `tekton-to-segment.sh` via `jq -f`. Each file
handles a distinct record type:

| File | Event type |
|---|---|
| `scripts/jq/transform-record.jq` | PipelineRun (Started + Completed) |
| `scripts/jq/transform-konflux.jq` | Konflux operator record |
| `scripts/jq/transform-namespace.jq` | Namespace Created |
| `scripts/jq/transform-component.jq` | Component Created |
| `scripts/jq/heartbeat.jq` | Segment Bridge Heartbeat |
| `scripts/jq/filter-pipelineruns.jq` | Pre-filter: keep only PipelineRuns |

Canonical files:

- **Orchestration script:** `scripts/tekton-to-segment.sh`
- **jq transform files:** `scripts/jq/`
- **Sample input:** `tekton-to-segment/sample/input.json`
- **Expected output:** `tekton-to-segment/sample/expected.json`
- **Regeneration scripts:** `tekton-to-segment/sample/prepare-input.sh`,
  `tekton-to-segment/sample/prepare-expected.sh`

## Steps

### 1. Edit the jq transform

Identify which event type you are changing from the table above, then edit the
corresponding file in `scripts/jq/`. The orchestration script
`scripts/tekton-to-segment.sh` calls these files with `jq -f`; you do not
normally need to edit the shell script itself unless you are adding a brand-new
event type or changing the dispatch logic.

Keep the output schema compatible with Segment's `track` call: each event
needs `type`, `event`, `properties`, and `messageId`.

### 2. Regenerate sample fixtures

```bash
cd tekton-to-segment/sample
./prepare-input.sh      # regenerate input.json from source data
./prepare-expected.sh   # regenerate expected.json by running the transform
```

### 3. Run the integration test

```bash
go test ./tekton-to-segment/...
```

The test compares the script's output against `expected.json`.

### 4. CI drift check

The `tekton-sample-input` GitHub Actions workflow reruns the prepare scripts
and fails if committed samples don't match. The regeneration step above
prevents this.

## Verification checkpoint

1. `go test ./tekton-to-segment/...`
2. `git diff tekton-to-segment/sample/` — should show only your intended changes
3. `pre-commit run --all-files`
4. Stop and show results before committing.
