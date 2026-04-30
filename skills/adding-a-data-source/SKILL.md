---
name: adding-a-data-source
description: >-
  Guides coordinated addition of fetch scripts that read the Kubernetes or Tekton
  Results APIs and emit NDJSON for segment-bridge. Covers shell scripts under
  scripts/, kwok + containerfixture tests, and .yamllint.yaml ignores; optionally
  wires scripts into the production image, tekton-main-job.sh, and README.md
  architecture diagram. Use when the user asks to add a fetch script, add a new
  data source, or create a script that queries cluster or Tekton Results data.
---

# Adding a data source

## Decision point (before any edits)

**Do not assume production.** Wiring into `scripts/tekton-main-job.sh` and the image runs the script on every CronJob across clusters.

Ask: **Will this script be part of the production pipeline?** Then use the **AskUserQuestion** tool with structured options (use these labels verbatim):

- **Production pipeline** — Script ships in the container image, is wired into `scripts/tekton-main-job.sh`, and runs automatically as part of the CronJob pipeline.
- **Standalone / utility** — Script lives in the repo under `scripts/` with tests and fixtures; it is **not** in the production image and is **not** run by the Tekton main job.

If intent is ambiguous, ask again. Then follow **Core track** always; add **Pipeline track** only when the user chose **Production pipeline**.

---

## Core track (both answers)

### 1. Shell script in `scripts/`

Add the new script (e.g. `scripts/fetch-<resource>-records.sh`).

- `set -o pipefail -o errexit -o nounset`
- Shellcheck-clean; file header comment like the header block at the top of `scripts/fetch-component-records.sh`: purpose, NDJSON stdout, pipeline note if applicable, environment variables.
- Resolve the client at runtime: prefer `oc`, else `kubectl`; error if neither — same kube client resolution pattern as in `scripts/fetch-component-records.sh`.
- **Stdout:** NDJSON (one JSON object per line); no JSON array wrapper.
- **Stderr:** diagnostics, warnings.
- **Exit 0** when the data source is absent or not applicable (e.g. CRD not installed): warn on stderr, print nothing to stdout when that is correct so callers do not abort.
- RBAC-style errors should still fail (non-zero).

### 2. Test module at repo root

Create a directory matching the script topic, e.g. `fetch-<topic>-records/` (same style as `fetch-component-records/`).

**Fixtures:** `fetch-<topic>-records/testdata/` (or `sample/`) with realistic YAML exported from a real cluster when possible — follow `fetch-component-records/testdata/component-samples/`.

### 3. Go tests (`package main`)

Add `fetch-<topic>-records/fetch_<topic>_records_test.go` (underscores in filename per existing packages).

- `containerfixture.WithServiceContainer(t, kwok.KwokServiceManifest, func(deployment containerfixture.FixtureInfo) { ... })`
- In the callback: `kwok.SetKubeconfigWithPort(deployment.WebPort)`; apply fixture YAML with the dynamic client (see `fetch-component-records/fetch_component_records_test.go` for imports and setup patterns).
- Run the script with `scripts.AssertExecuteScriptWithEnv` or `testfixture.RunRepoScript`; `scriptPath` typically `../scripts/<your-script>.sh` (follow the same test helper usage as the happy-path cases in `fetch_component_records_test.go`).
- Add a **negative** case where applicable (e.g. exit 0 when API missing) — see `TestFetchComponentRecordsExitsZeroWhenComponentCRDNotInstalled` in the same file.

Reference: `fetch-namespace-records/fetch_namespace_records_test.go` for a second full example.

### 4. Yamllint ignore

If fixture YAML has long lines or non-standard formatting, append the fixture path under `ignore:` in `.yamllint.yaml` (same style as `fetch-namespace-records/testdata/` and `fetch-component-records/testdata/`).

---

## Pipeline track (only if user chose **Production pipeline**)

### A. Register script for container-mode tests

`testfixture/run_repo_script.go` — add the script’s basename to `bundledScriptBaseNames` near the other bundled script names. Required for `SEGMENT_BRIDGE_TEST_IMAGE` runs.

### B. Image install

`Dockerfile` — add `scripts/<your-script>.sh` to the multi-line `COPY` into `/usr/local/bin/` alongside the other script copies; keep ownership and mode flags consistent with existing entries.

### C. Tekton main job

`scripts/tekton-main-job.sh` — inside the brace group that already uses `set +e`, append a call to the new script before `true; }`. Fetches are **best-effort**: failures must not stop other fetches or the pipe into `get-konflux-public-info.sh`.

### D. README diagram

If the script is another input to `get-konflux-public-info.sh`, update the mermaid `flowchart` section near the top of `README.md`: add a node for the script, wire it to `A1` (Tekton Results API) or `A2` (Kubernetes API) and to `get-konflux-public-info.sh` / subgraph B as appropriate.

---

## Verification checkpoint

After all applicable steps:

1. `go test ./fetch-<topic>-records/...` (use the actual test package path).
2. `pre-commit run --all-files` or `mise run pre-commit` (shellcheck, yamllint, golangci-lint).
3. Stop and show results before committing.

---

## Key conventions

| Topic | Rule |
|--------|------|
| **NDJSON** | One JSON object per line on stdout; no enclosing array. |
| **Pipeline** | Fetches run under `set +e` in `tekton-main-job.sh`; stderr for errors; pipeline continues. |
| **oc / kubectl** | Prefer `oc` if `command -v oc`, else `kubectl`; require one of them (see `fetch-component-records.sh`). |
| **Env vars** | Prefix matches the data source (e.g. `COMPONENT_RECENT_HOURS`, `COMPONENT_NOW_ISO`); document in the script header. |
