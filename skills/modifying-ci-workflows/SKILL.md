---
name: modifying-ci-workflows
description: >-
  Guides changes to GitHub Actions workflows under .github/workflows/. Covers
  action version conventions, caching patterns, and the pre-commit integration.
  Use when the user asks to add a CI check, modify a workflow, or update
  GitHub Actions.
---

# Modifying CI workflows

## Workflow inventory

| File | Triggers | Purpose |
|------|----------|---------|
| `pull_request_and_push.yaml` | push to main, PRs, merge group | Lint gate: runs pre-commit (shellcheck, golangci-lint, yamllint, actionlint, markdownlint) |
| `unit_tests.yaml` | push to main, PRs, merge group | Builds test image, runs `go test -short ./...`, uploads coverage |
| `e2e_tests.yaml` | push to main, PRs, merge group | Builds test image, runs `go test -tags=e2e` in `tekton-e2e/` |
| `gitlint.yaml` | PRs | Validates commit messages against `.gitlint` rules |
| `tekton-sample-input.yaml` | push to main, PRs | Ensures `tekton-to-segment/sample/` fixtures are up to date |
| `dependabot-auto-merge.yaml` | PR review events | Auto-merges minor/patch Dependabot and Konflux PRs |

## Conventions

- **Action versions:** pin to major tags — `actions/checkout@v6`,
  `actions/setup-go@v6`, `actions/setup-python@v6`.
- **Go version:** always use `go-version-file: 'go.mod'` (never hardcode).
- **Caching:** Go module cache via `actions/setup-go` `cache: true`.
  Python deps via `actions/setup-python` `cache: pip`.
- **Job names:** use descriptive names (`lint-and-checks`, `go-test`).
- **Pre-commit in CI:** the lint workflow runs `pre-commit run --verbose
  --all-files`. Adding a new linter means adding it to
  `.pre-commit-config.yaml`, not to the workflow directly.

## Steps for adding a new check

1. Prefer adding to `.pre-commit-config.yaml` if the tool supports it — this
   keeps local and CI checks in sync.
2. If a standalone workflow is needed, create a new file in
   `.github/workflows/` following the trigger pattern from
   `pull_request_and_push.yaml`.
3. Run `actionlint .github/workflows/<new-file>.yaml` locally.
4. Run `pre-commit run --all-files` to validate all hooks pass.

## Verification checkpoint

1. `actionlint .github/workflows/*.yaml`
2. `pre-commit run --all-files`
3. Stop and show results before committing.
