---
name: adding-a-kwok-test-fixture
description: >-
  Guides creation of kwok-based integration tests for shell scripts. Covers
  fixture YAML under testdata/, Go test setup with containerfixture + kwok,
  and yamllint configuration. Use when the user asks to add tests for a shell
  script, create a kwok fixture, or set up integration tests.
---

# Adding a kwok test fixture

## What kwok tests look like

Each `fetch-*.sh` script has a sibling test directory at the repo root
(e.g. `fetch-component-records/`) containing Go integration tests that:

1. Start a kwok container (lightweight K8s API simulator)
2. Apply fixture YAML to the simulated cluster
3. Run the shell script against it
4. Assert on the NDJSON output

## Canonical example

- **Test file:** `fetch-component-records/fetch_component_records_test.go`
- **Fixtures:** `fetch-component-records/testdata/component-samples/*.yaml`

## Steps

### 1. Create the test directory

```
fetch-<topic>-records/
├── testdata/
│   └── <resource>-samples/
│       └── sample-resource.yaml   # exported from a real cluster
└── fetch_<topic>_records_test.go
```

### 2. Export fixture YAML

Use `kubectl get <resource> -o yaml` on a real cluster to get realistic
fixtures. Place them under `testdata/`.

### 3. Write the Go test

```go
package main

import (
    "testing"
    "github.com/redhat-appstudio/segment-bridge.git/containerfixture"
    "github.com/redhat-appstudio/segment-bridge.git/kwok"
    "github.com/redhat-appstudio/segment-bridge.git/scripts"
)

func TestFetchRecords(t *testing.T) {
    containerfixture.WithServiceContainer(t,
        kwok.KwokServiceManifest,
        func(deployment containerfixture.FixtureInfo) {
            kwok.SetKubeconfigWithPort(deployment.WebPort)
            // Apply fixtures, run script, assert output
        },
    )
}
```

Follow `fetch-component-records/fetch_component_records_test.go` for the
full pattern including dynamic client setup and fixture application.

### 4. Add a negative test case

Test graceful exit (exit 0, no stdout) when the CRD is not installed.
See `TestFetchComponentRecordsExitsZeroWhenComponentCRDNotInstalled`.

### 5. Yamllint ignore

If fixture YAML has long lines, add the testdata path to `.yamllint.yaml`
under `ignore:`:

```yaml
ignore:
  - fetch-<topic>-records/testdata/
```

## Verification checkpoint

1. `go test ./fetch-<topic>-records/...`
2. `pre-commit run --all-files`
3. Stop and show results before committing.
