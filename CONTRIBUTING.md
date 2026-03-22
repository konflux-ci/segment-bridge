# Contributing

This document provides guidelines for contributing to this repository.

## Development Environment Setup

### Prerequisites
* Basic tools: `curl`, `jq`, `oc`
* Go: version in [`go.mod`](go.mod) (see also [`mise.toml`](mise.toml) for a pinned toolchain via [mise](https://mise.jdx.dev/))
* Container engine: `podman`

## Running a test environment

### Kwok Container
"Kwok" is a Kubernetes SIGs-hosted project. KWOK is an abbreviation for
Kubernetes Without Kubelet. Kwok simply simulates the node's behaviour.
As a result, it can mimic a high number of nodes and pods while consuming
only a small amount of memory.

To run the Kwok container with the Kwok Kubernetes tool, follow these steps:

1. Build the kwok container using the following command:
   ```
   podman build -t kwok -f kwok/Dockerfile kwok
   ```
2. Bring the clusters up by running the following command from the 
   repo's root directory:
    ```
    podman kube play kwok/kwok_container_default.yml
    ```

3. Check `podman pod list` should list the below pod
    ```
    POD ID        NAME            STATUS      CREATED        INFRA ID      # OF CONTAINERS
    e815836efc86  kwok-pod        Running     1 minutes ago  8466696a9956  2
    ```

4. Once the Kwok cluster is up and running, set the cluster details in the
   OpenShift client with the following commands:
    ```
    oc config set-cluster kwok --server=http://127.0.0.1:8080
    ```

5. Create a new context (you only need to set it once) for the Kwok
   cluster with the following command:
    ```
    oc config set-context kwok --cluster=kwok
    ```

6. Set the Kwok context as the current context, if you've previously switched
   to another cluster, with the following command:
    ```
    oc config use-context kwok
    ```

Now you can access the cluster using kubectl, e.g.: `kubectl get ns`.

> **Note**
>
> Alternatively, you can also use the kubeconfig file on the repository providing the
> context and the cluster inline with the command. For example:
>
> ```oc --kubeconfig=./kwok/kubeconfig --context=kwok get ns```

### Building and running the segment-bridge container image

The scripts in this repo can be built into a container image to enable
scheduling and running them on K8s clusters.

To build the image locally, one needs to be logged in to a `redhat.com` account
(With e.g `podman login`) in order to access the base image and then the image
can be built with:
```
podman build -t segment-bridge .
```

The image runs [`tekton-main-job.sh`](scripts/tekton-main-job.sh), which needs
Tekton Results API access, Kubernetes API access (service account or kubeconfig),
and Segment credentials for upload. Typical variables include
`TEKTON_RESULTS_API_ADDR`, `TEKTON_NAMESPACE`, `SEGMENT_BATCH_API`, and either
`SEGMENT_WRITE_KEY` or a `.netrc` mounted for `CURL_NETRC`. See comments in the
[`Dockerfile`](Dockerfile) for a full example `podman run`.

Kubernetes deployment examples use Kustomize under [`config/`](config/).

### Unit Tests
Go unit tests are included in various packages within the repository.
Go unit tests are located within the tests directory, with filenames ending with
_tests.go and with .go.

#### Running the Unit Tests Locally
1. Clone your fork of the project.
2. Navigate to the project's root directory
3. To run all the Go unit tests in the repository, from the repo root (with Go
   on your PATH, or via `mise exec --` if you use [`mise.toml`](mise.toml)):
   `go clean -testcache && go test ./...`
   A similar output is expected (package list may vary):

    ```
    ?   	github.com/redhat-appstudio/segment-bridge.git/containerfixture	[no test files]
    ?   	github.com/redhat-appstudio/segment-bridge.git/kwok	[no test files]
    ok  	github.com/redhat-appstudio/segment-bridge.git/fetch-konflux-op-records	0.002s
    ok  	github.com/redhat-appstudio/segment-bridge.git/fetch-namespace-records	0.002s
    ok  	github.com/redhat-appstudio/segment-bridge.git/get-konflux-public-info	0.002s
    ok  	github.com/redhat-appstudio/segment-bridge.git/scripts	0.002s
    ok  	github.com/redhat-appstudio/segment-bridge.git/segment	0.002s
    ok  	github.com/redhat-appstudio/segment-bridge.git/tekton-to-segment	0.002s
    ```
4. If you want to run tests for a specific package, use its import path, for
   example:
    ```
    go test ./scripts
    ```

#### Running shell-script tests inside the bridge image (optional)

CI builds the image from [`Dockerfile`](Dockerfile) and sets `SEGMENT_BRIDGE_TEST_IMAGE`
so script-based tests execute with `/usr/local/bin/*.sh` inside that image (matching
production layout). Locally, after building (see the Dockerfile header for
`prepare-oc-client-for-build.sh` and the `podman build -v ... deps ...` invocation),
you can run the same tests with:

```
export SEGMENT_BRIDGE_TEST_IMAGE=segment-bridge:test
go test ./...
```

Optional: `SEGMENT_BRIDGE_TEST_CONTAINER_RUNTIME` selects `podman` or `docker` when
both are installed; otherwise `podman` is tried first, then `docker`.

#### Test Coverage
[TBD]

### Integration Tests
[TBD]

#### Running the Integration Tests
[TBD]

### Before submitting the PR

1. The repository enforces pre-commit checks. Install dependencies and run hooks
   on all files before committing, for example:
   `mise run pre-commit`
   (equivalent to a project-local venv from `requirements.lock` plus
   `pre-commit run --all-files`; see [`mise.toml`](mise.toml).)
2. Ensure to run `gofmt` to format your code.
3. Make sure all unit tests are passing.

### Commit Messages
We use [gitlint](https://jorisroovers.com/gitlint/) to standardize commit messages,
following the [Conventional commits](https://www.conventionalcommits.org/en/v1.0.0/) format.

If you include a Jira ticket identifier (e.g., RHTAPWATCH-387) in the commit message,
PR name, or branch name, it will link to the Jira ticket.

```
feat(RHTAPWATCH-387): Include the UserAgent field

Include the UserAgent field in all events sent to Segment.

Signed-off-by: Your Name <your-email@example.com>

```

### Pull Request Description
When creating a Pull Request (PR), use the commit message as a starting point,
and add a brief explanation. Include what changes you made and why.
This helps reviewers understand your work without needing to investigate
deeply. Clear information leads to a smoother review process.

### Code Review Guidelines
* Each PR should be approved by at least 2 team members. Those approvals are only
relevant if given since the last major change in the PR content.

* All comments raised during code review should be addressed (fixed/replied).
  * Reviewers should resolve the comments that they've raised once they think
    they were properly addressed.
  * If a comment was addressed by the PR author but the reviewer did not resolve or
    reply within 1 workday (reviewer's workday), then the comment can be resolved by
    the PR author or by another reviewer.

* All new and existing automated tests should pass.

* A PR should be open for at least 1 workday at all time zones within the team. i.e.
team members from all time zones should have an opportunity to review the PR within
their working hours.

* When reviewing a PR, verify that the PR addresses these points:
  * Edge cases
  * Race conditions
  * All new functionality is covered by unit tests
  * It should not be necessary to manually run the code to see if a certain part works,
    a test should cover it
  * The commits should be atomic, meaning that if we revert it, we don't lose something
    important that we didn't intend to lose
  * PRs should have a specific focus. If it can be divided into smaller standalone
    PRs, then it needs to be split up. The smaller the better
  * Check that the added functionality is not already possible with an existing
    part of the code
  * The code is maintainable and testable
  * The code and tests do not introduce instability to the testing framework
