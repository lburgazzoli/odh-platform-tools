# AGENTS.md

CLI tooling for OpenDataHub platform module definitions: offline lint and
one-shot deploy of a module's controller package.

## Layout

- `cmd/` — binary and Cobra commands only; no business logic.
  Root in `cmd/main.go`, subcommands in `cmd/module/{lint,deploy}`.
- `pkg/` — reusable services. Packages under `pkg/` never import Cobra.
- `pkg/module/api/v1alpha1` — APIs, Scheme, generated code only.
- `config/crd` — embedded generated CRD manifests (`make manifests`).
- `tests/{integration,e2e,support}` — offline integration suite (build tag
  `integration`) and live e2e suite (build tag `e2e`).
- `examples/` — Helm and Kustomize sample modules.
- No `internal/` tree.

## Conventions

- File roles: `<name>.go`, `<name>_support.go`, `<name>_opts.go`, `<name>_test.go`.
- Functional options via `pkg/util/option` (`Option[T]`); presets on value
  receivers; constructors take `opts ...Option[T]`.
- One type per parameter — never group same-type parameter declarations.
- Errors: `pkg/module/errors` — `moduleerrors.New(category, msg,
  moduleerrors.WithCause(err))`; classify with `errors.Is` on category
  sentinels, never message strings.
- New JSON code uses `encoding/json/v2`.
- Embedded YAML/JSON documents are individually named package-level consts.
- Source files carry the Apache-2.0 header (`hack/boilerplate.go.txt`).

## Commands

```
GOFLAGS="-p=1" make -j1 fmt
GOFLAGS="-p=1" make -j1 build verify test-integration test-e2e-compile
make generate     # deepcopy
make manifests    # CRD
```

## Hygiene

- Never commit `.context/`.
- Lint configuration is tracked in `.golangci.yml`.
- Exit codes: 0 success, 1 runtime/cancellation, 2 usage/definition.

See `docs/` for development, testing, architecture, module authoring, and
CLI reference.
