# Sample data for tekton-to-segment

This directory holds sample Tekton/Kubernetes resources and the expected Segment event output used to test the `scripts/tekton-to-segment.sh` transformer.

## Layout

| Path | Description |
|------|-------------|
| **`input/`** | YAML files: PipelineRuns and related resources (Applications, Components, etc.) used as raw input. |
| **`expected/`** | YAML files: expected Segment events corresponding to the resources in `input/`. |
| **`input.json`** | NDJSON: one JSON object per line, built from all `input/*.yaml`. Consumed by the test and by the transformer script. |
| **`expected.json`** | NDJSON: one JSON object per line, built from all `expected/*.yaml`. Used as the golden output in tests. |

## Scripts

All scripts expect **yq** ([mikefarah/yq](https://github.com/mikefarah/yq)) and **jq**. Run them from any directory; paths are relative to this `sample/` folder.

- **`prepare-input.sh`**  
  Converts every `input/*.yaml` file into a single compact JSON line and writes **`input.json`** (one line per file). Use this after adding or changing YAML in `input/` so the test and script see the updates.

- **`prepare-expected.sh`**  
  Converts every document in `expected/*.yaml` (multi-doc YAML supported) into one JSON line and writes **`expected.json`**, with keys sorted for stable comparison. Use this after editing `expected/*.yaml` so the test uses the new golden output.

## When to use what

1. **Running tests**  
   Tests read `sample/input.json` and `sample/expected.json`. Ensure both exist and are up to date:
   - After changing `input/*.yaml` → run **`prepare-input.sh`**.
   - After changing `expected/*.yaml` → run **`prepare-expected.sh`**.

2. **Adding new sample input**  
   Add the new YAML to `input/`, run **`prepare-input.sh`**, then add the expected Segment events under `expected/` and run **`prepare-expected.sh`** to refresh **`expected.json`**.

## Tests

The test in `tekton-to-segment/tekton_to_segment_test.go`:

- Sets `CLUSTER_ID=test-cluster`
- Feeds `sample/input.json` (one JSON object per line) into `scripts/tekton-to-segment.sh`
- Compares the script’s stdout (one JSON line per Segment event) to `sample/expected.json` line-by-line

The input includes a mix of resource kinds (e.g. Application, Component, PipelineRun); the transformer emits Segment events for the subset it supports, and the test compares that output to the golden `expected.json`.
