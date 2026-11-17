# Testing guide

## Suites

| Suite | Build tag | Target | Needs cluster |
|-------|-----------|--------|---------------|
| Unit | — | `make test` | no |
| Integration | `integration` | `make test-integration` | no (offline) |
| e2e | `e2e` | `make test-e2e-kind` / `make test-e2e-external` | yes |
| Shell contract | — | `sh hack/scripts/image_test.sh` | no |

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

## Shell contract tests

`sh hack/scripts/image_test.sh` runs the dependency-free POSIX contract
suite for `hack/scripts/image.sh` from any working directory. It places a
fake `podman` on a controlled `PATH`, so it needs no container engine,
network, or Go toolchain. There is no Make target for it.

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
ODH_PLATFORM_TOOLS_E2E_EXTERNAL_KUBECONFIG required with the external engine
ODH_PLATFORM_TOOLS_E2E_EXTERNAL_CONTEXT    required with the external engine
```

- `make test-e2e-kind`: the engine creates and tears down an isolated Kind
  cluster (node image pinned to the Kubernetes line this repo builds against).
- `make test-e2e-external`: runs against the cluster named by the env vars.
- `make test-e2e-compile` checks the suite compiles without any setup.
