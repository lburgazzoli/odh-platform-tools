# AGENTS.md

CLI tooling for OpenDataHub platform modules. Lint controller packages
offline, install a package in one pass, and run a status-only mock module
controller.

## Layout

- `cmd/` — binary, Cobra commands, and each subcommand's default
  service composition (`Action`); no other business logic. Root in
  `cmd/main.go`, subcommands in `cmd/module/{lint,deploy,generate,mock}`.
- `pkg/` — reusable services. Packages under `pkg/` never import Cobra
  and never compose command defaults.
- `pkg/module/api` — reusable module types and generated deepcopy methods.
- `pkg/module/api/v1alpha1` — versioned APIs, Scheme, generated code only.
- `pkg/module/mock` — status-only controller with runtime GVK registration.
- `config/crd` — embedded generated CRD manifests (`make manifests`); the
  generated CRD under `config/crd/bases/` is tracked in git.
- `hack/scripts/` — POSIX sh helpers for image builds, called by Make.
- `tests/{integration,e2e,support}` — offline integration suite (build tag
  `integration`) and live e2e suite (build tag `e2e`).
- `examples/` — Helm and Kustomize sample modules.
- No `internal/` tree.

## Conventions

- File roles: `<name>.go`, `<name>_support.go`, `<name>_opts.go`, `<name>_test.go`.
- Functional options via `pkg/util/option` (`Option[T]`): settings struct +
  `WithXxx` functions; presets on value receivers; constructors take
  `opts ...Option[T]`.
- One type per parameter — never group same-type parameter declarations.
- Every `cmd/module/$name` defines `type Run` (the injectable boundary)
  and `func Action` (the default composition of pkg services);
  `cmd/main.go` passes the four Actions to the command group. Actions
  step compose → run → `moduleerrors.WrapStage`: classified errors
  pass through untouched, raw ones gain a stage prefix.
- Command exit failures return `pkg/util/exit.Error`; the
  consumer-side `exitCoder` interface stays in `cmd/main.go`.
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
make manifests    # CRD (tracked; rerun after API edits)
make image        # native-platform container image (Podman only)
make image/multiarch  # local linux/amd64 + linux/arm64 manifest list
```

## Image builds

- Podman only; no Docker path, no push target — images stay local.
- `IMAGE_REGISTRY`/`IMAGE_NAME`/`IMAGE_TAG` override the default reference
  `quay.io/lburgazzoli/odh-platform-tools:dev`.
- Make recipes stay one-line delegations to `hack/scripts/image.sh`; new
  image logic goes there (POSIX sh), never in Make or a `.mk` include.
- Use distinct tags for native and multiarch results; `multiarch` refuses a
  reference held by a single-architecture image.

## Hygiene

- Never commit `.context/`.
- Lint configuration is tracked in `.golangci.yml`.
- Exit codes: 0 success, 1 runtime/cancellation, 2 usage/definition.

See `docs/` for development, testing, architecture, module authoring, and
CLI reference.
