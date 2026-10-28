# Testing guide

## Suites

| Suite | Build tag | Target | Needs cluster |
|-------|-----------|--------|---------------|
| Unit | — | `make test` | no |
| Integration | `integration` | `make test-integration` | no (offline) |
| e2e | `e2e` | `make test-e2e-kind` / `make test-e2e-external` | yes |

All targets run with `-race -count=1`.

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
