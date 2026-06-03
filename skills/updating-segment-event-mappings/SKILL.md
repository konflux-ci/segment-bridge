---
name: updating-segment-event-mappings
description: >-
  Guides changes to the jq pipeline in scripts/tekton-to-segment.sh that
  transforms NDJSON records into Segment batch events. Covers updating field
  mappings, adding new event properties, and regenerating the sample fixtures.
  Use when the user asks to change what data is sent to Segment, add a new
  event property, or modify the Segment event schema.
---

# Updating Segment event mappings

## How the transform works

`scripts/tekton-to-segment.sh` reads enriched NDJSON from stdin (output of
`get-konflux-public-info.sh`) and uses a `jq` pipeline to produce Segment
`track` events. Each input line becomes one output event.

Canonical files:

- **Transform script:** `scripts/tekton-to-segment.sh`
- **Sample input:** `tekton-to-segment/sample/input.json`
- **Expected output:** `tekton-to-segment/sample/expected.json`
- **Regeneration scripts:** `tekton-to-segment/sample/prepare-input.sh`,
  `tekton-to-segment/sample/prepare-expected.sh`

## Steps

### 1. Edit the jq pipeline

Modify the jq expression in `scripts/tekton-to-segment.sh`. Keep the output
schema compatible with Segment's `track` call: each event needs `type`,
`event`, `properties`, and `messageId`.

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
