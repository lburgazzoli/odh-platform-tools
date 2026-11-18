# Testing guide

## Suites

| Suite | Build tag | Target | Needs cluster |
|-------|-----------|--------|---------------|
| Unit | — | `make test` | no |
| Integration | `integration` | `make test-integration` | no (offline) |
| e2e | `e2e` | `make test-e2e-kind` / `make test-e2e-external` | yes |

All Go targets run with `-race -count=1`.

## Native Linux Go suites

The process-supervision tests read Linux `/proc`. On a macOS host, run the
Go suites in a true native linux/arm64 Go container and treat emulated runs
as non-authoritative. Inspect the actual builder architecture first:

```
podman image inspect docker.io/library/golang:1.27 --format '{{.Os}}/{{.Architecture}}'
```

Then run the gates in the container (here `test`; substitute other targets
as needed):

```
podman run --init --rm --platform linux/arm64 \
  -v "$PWD":/work:Z -w /work \
  -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOMODCACHE=/tmp/gomodcache \
  -e GOFLAGS=-p=1 \
  docker.io/library/golang:1.27 \
  make -j1 test
```

- `--init` reaps orphaned test processes.
- `HOME`, `GOCACHE`, and `GOMODCACHE` must point at writable scratch paths.
- `make` is the container command, so its exit status is the container's.

## Style

- Vanilla `testing.T` with dot-imported Gomega; `g := NewWithT(t)` per test.
- JSON assertions use `github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq`;
  split member checks into `SatisfyAll(jq.Match(...), ...)`.
- Every multiline YAML/JSON fixture is one self-contained named package-level
  `const name = \`...\`` — no concatenation, inline documents, or `const (...)`
  fixture groups.
- Assert focused members, not entire reports/objects.
- Test observable behavior, not stdlib, generated DeepCopy, or trivial getters.
- Use typed Go object builders for dynamic Kubernetes identities; decode
  fixture batches with the shared `pkg/k8s.DecodeResources`.

## e2e

The suite fails hard — never skips — when live prerequisites are missing.

```
ODH_PLATFORM_TOOLS_E2E_ENGINE              kind (default) | external
ODH_PLATFORM_TOOLS_E2E_NODE_IMAGE          overrides the recorded node image pin
ODH_PLATFORM_TOOLS_E2E_IMAGE               required with the external engine
ODH_PLATFORM_TOOLS_E2E_EXTERNAL_KUBECONFIG required with the external engine
ODH_PLATFORM_TOOLS_E2E_EXTERNAL_CONTEXT    required with the external engine
```

- `make test-e2e-kind` provisions an isolated Kind cluster on the pinned
  node image. It builds one uniquely tagged native tools image with
  Podman, exports it, and loads it into the owned nodes. A smoke Pod
  executes `module mock --help` from the loaded image with pulling
  disabled. Teardown removes the run-owned cluster, tag, and archive.
  Use `KIND_EXPERIMENTAL_PROVIDER=podman make test-e2e-kind` for Podman
  nodes, or configure the Docker provider for the Kind nodes while
  keeping Podman available for the build.
- `make test-e2e-external` requires an explicit kubeconfig path, context,
  and image reference accessible to the cluster nodes. The suite uses
  that image without building, loading, or publishing it. The test
  identity needs permission to install the packages, list and watch the
  scoped test identities for cleanup, and exercise status and event
  RBAC. The shared definition CRD stays installed.
- `make test-e2e-compile` checks the suite compiles without cluster setup.

The live suite deploys both example packages, applies their module
instances separately, and checks persisted status, ConfigMap updates,
and recovery under the packaged ServiceAccounts. It also exercises a
real status-write denial and checks the controller's Kubernetes event.
