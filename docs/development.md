# Development guide

## Prerequisites

- Go 1.27.3
- Podman or Docker + Kind for the live e2e suite

## Commands

Run gates serialized on constrained hosts: `GOFLAGS="-p=1" make -j1 <target>`.

| Target | Purpose |
|--------|---------|
| `make build` | Build all packages |
| `make fmt` / `make fmt-check` | Format / check formatting |
| `make vet` | `go vet ./...` |
| `make lint` | golangci-lint (config in local, untracked `.golangci.yml`) |
| `make test` | Unit tests (`-race -count=1`) |
| `make test-integration` | Offline integration suite (build tag `integration`) |
| `make test-e2e-compile` | Compile the e2e suite without a cluster |
| `make test-e2e-kind` | e2e against an engine-owned Kind cluster |
| `make test-e2e-external` | e2e against an external cluster |
| `make coverage` | Unit tests with coverage profile |
| `make vulncheck` | Vulnerability check |
| `make verify` | fmt-check + vet + lint + test |
| `make generate` | Deepcopy code (controller-gen) |
| `make manifests` | Module definition CRD (controller-gen) |

## Layout

```
cmd/                    binary + Cobra commands (no business logic)
  main.go               root command, exit-code contract
  module/lint           module lint command + reports
  module/deploy         module deploy command + reports
pkg/
  module/api/v1alpha1   APIs, Scheme, generated code only
  module/loader         decode (unknown fields dropped) + default definitions
  module/renderer       shared render engine (helm/, kustomize/, source/)
  module/linter         lint action + checks/{types,deployments,crd}
  module/deployer       deploy orchestration (adapt/, cluster/, images/)
  module/errors         module error taxonomy
  k8s/                  shared Kubernetes helpers (configmap, deployment, crd)
  util/                 jq, option, retry
config/crd              embedded generated CRD manifests
tests/                  integration (tag: integration), e2e (tag: e2e)
examples/               Helm and Kustomize sample modules
```

## Conventions

- `pkg/` packages never import Cobra; commands under `cmd/` configure options
  and call injected action functions.
- File roles: `<name>.go`, `<name>_support.go`, `<name>_opts.go`,
  `<name>_test.go`.
- Functional options via `pkg/util/option`; presets on value receivers.
- One type per parameter; never `func f(a, b string)`.
- Errors: `pkg/module/errors` — one `ModuleError` with category sentinels;
  construct with `moduleerrors.New`, classify with `errors.Is`.
- New JSON code uses `encoding/json/v2`.
- Embedded YAML/JSON documents are individually named package-level consts.
- Retries go through `pkg/util/retry` (bounded attempts, backoff, predicate).
- Apache-2.0 header from `hack/boilerplate.go.txt` on source files.

## Hygiene

- Never commit `.context/`.
- `.golangci.yml` is local-only (untracked); `make lint` picks it up when
  present and falls back to golangci-lint defaults otherwise.
- Generated code (`zz_generated.*`, `config/crd/bases`) is regenerated, never
  hand-edited.
