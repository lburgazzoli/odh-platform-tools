# Development guide

## Prerequisites

- Go 1.27.3
- Podman for building tools images and running Go suites on native Linux.
- Kind for the managed live e2e suite. The managed runner builds its
  tools image with Podman and loads it through Kind's configured
  provider. [Live test setup](testing.md#e2e)

## Commands

Run gates serialized on constrained hosts: `GOFLAGS="-p=1" make -j1 <target>`.

| Target | Purpose |
|--------|---------|
| `make build` | Build all packages |
| `make fmt` / `make fmt-check` | Format / check formatting |
| `make vet` | `go vet ./...` |
| `make lint` | golangci-lint (config: `.golangci.yml`) |
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
| `make image` | Container image for the host platform (Podman) |
| `make image/multiarch` | Local linux/amd64 + linux/arm64 manifest list |

## Container images

Local CLI images are built with Podman only. There is no Docker path, no
push target, and nothing is ever published — images and manifest lists stay
in local storage. Public base images and Go modules need network access on
first use; "offline" elsewhere in this repo refers to runtime lint behavior,
not an uncached image build.

The root `Containerfile` has two stages:

- **Builder** — official `docker.io/library/golang:1.27`, pinned to the build
  host's platform so the toolchain and the pinned `make manifests` CRD
  regeneration always run natively. `TARGETOS`/`TARGETARCH` steer the final
  static `CGO_ENABLED=0 -trimpath` cross-compile, and the runtime `HOME` is
  prepared here, so the final stage contains no `RUN` instruction and
  foreign-architecture builds never execute emulated code.
- **Runtime** — `registry.access.redhat.com/ubi10/ubi-minimal` with no Go
  toolchain, non-root UID 1001, writable `HOME=/home/default`, and the
  exec-form entrypoint `/usr/local/bin/odh-platform-tools`.

`make image` builds for the host's native platform; `make image/multiarch`
builds a local manifest list with exactly one linux/amd64 and one
linux/arm64 entry. The Make variables `IMAGE_REGISTRY`, `IMAGE_NAME`, and
`IMAGE_TAG` default to `quay.io/lburgazzoli`, `odh-platform-tools`, and
`dev`, and compose the image reference `<registry>/<name>:<tag>`.

Both targets are thin one-line recipes that delegate to
`hack/scripts/image.sh`, where all branching and validation live:

```
hack/scripts/image.sh build    --registry <registry> --name <name> --tag <tag>
hack/scripts/image.sh multiarch --registry <registry> --name <name> --tag <tag>
```

New image logic belongs in `hack/scripts/` (POSIX `sh`, Apache-2.0 header),
never in Make recipes or a separate `.mk` include — none exists. The script
resolves the repository root from its own location, so it works from any
directory.

`multiarch` builds into a private temporary list and moves it onto the
requested reference, so repeated runs never accumulate platform entries. A
reference already held by a single-architecture image is refused rather
than overwritten — use distinct tags for native and multiarch results.

Smoke-testing the CLI against the example modules (read-only mounts):

```
podman run --rm -v "$PWD/examples:/work/examples:ro,Z" \
  quay.io/lburgazzoli/odh-platform-tools:<tag> \
  module lint --config /work/examples/helm/chart/module.yaml

podman run --rm -v "$PWD/examples:/work/examples:ro,Z" \
  quay.io/lburgazzoli/odh-platform-tools:<tag> \
  module lint --config /work/examples/kustomize/module.yaml
```

Deploying from the container mounts the module and a kubeconfig read-only
and points the CLI at the kubeconfig through the standard `KUBECONFIG`
environment variable. `--config` and `--platform-version` are required; the
examples below mount the Helm sample module and set its controller image
source. The cluster nodes must have access to that image. The API server
address in the kubeconfig must be reachable inside the container. For a
local cluster that advertises the host loopback address, run the host CLI
as in the mock walkthrough. Exec-auth kubeconfigs require their configured
credential helper. Set `controller_image` to the image available to the
cluster before using either command below. [Mock walkthrough](mock.md)

```
podman run --rm \
  -v "$PWD/examples/helm/chart:/work/module:ro,Z" \
  -v "$HOME/.kube/config:/work/kubeconfig:ro,Z" \
  -e KUBECONFIG=/work/kubeconfig \
  -e RELATED_IMAGE_MOCK_CONTROLLER="$controller_image" \
  quay.io/lburgazzoli/odh-platform-tools:<tag> \
  module deploy \
    --config /work/module/module.yaml \
    --platform-version <semver>
```

An owner-only (mode 0600) kubeconfig remains readable by runtime UID 1001
without loosening host file permissions by mapping the invoking user onto
the image user through a rootless user namespace:

```
podman run --rm \
  --userns=keep-id:uid=1001,gid=1001 \
  -v "$PWD/examples/helm/chart:/work/module:ro,Z" \
  -v "$HOME/.kube/config:/work/kubeconfig:ro,Z" \
  -e KUBECONFIG=/work/kubeconfig \
  -e RELATED_IMAGE_MOCK_CONTROLLER="$controller_image" \
  quay.io/lburgazzoli/odh-platform-tools:<tag> \
  module deploy \
    --config /work/module/module.yaml \
    --platform-version <semver>
```

## Layout

```
cmd/                    binary + Cobra commands (no business logic)
  main.go               root command, exit-code contract
  module/lint           module lint command + reports
  module/deploy         module deploy command + reports
  module/generate       module generate command + result output
  module/mock           module mock command + flag validation
  module/validate       module validate command + reports
pkg/
  module/api            related images and shared platform status types
  module/api/v1alpha1   versioned APIs, Scheme, generated code only
  module/loader         decode (unknown fields dropped) + default definitions
  module/generator      generate action (strict discovery, document, publish)
  module/renderer       shared render engine (helm/, kustomize/, source/)
  module/linter         lint action + checks/{types,deployments,crd}
  module/deployer       deploy orchestration (adapt/, cluster/, images/)
  module/mock           framework mock controller and status handling
  module/validator      live read-only validation checks and polling
  module/errors         module error taxonomy
  k8s/                  shared Kubernetes helpers (configmap, deployment, crd)
  util/                 jq, option, retry
config/crd              embedded generated CRD manifests
hack/scripts/           POSIX helpers for image builds (called from Make)
tests/                  integration (tag: integration), e2e (tag: e2e)
examples/               Helm and Kustomize sample modules
```

## Conventions

- `pkg/` packages never import Cobra; commands under `cmd/` configure options
  and call injected action functions.
- File roles: `<name>.go`, `<name>_support.go`, `<name>_opts.go`,
  `<name>_test.go`.
- Functional options combine a settings struct and functions — see below.
- One type per parameter; never `func f(a, b string)`.
- Errors: `pkg/module/errors` — one `ModuleError` with category sentinels;
  construct with `moduleerrors.New`, classify with `errors.Is`.
- New JSON code uses `encoding/json/v2`.
- Embedded YAML/JSON documents are individually named package-level consts.
- Retries go through `pkg/util/retry` (bounded attempts, backoff, predicate).
- Apache-2.0 header from `hack/boilerplate.go.txt` on source files.

## Functional options

Optional configuration always pairs a plain settings struct with `WithXxx`
function options over it, via `pkg/util/option`:

- **Struct**: an exported `XxxOptions` struct holds the settings. It is the
  single target every option mutates.
- **Functions**: each setting gets a `WithXxx` constructor returning
  `option.FunctionalOption[XxxOptions]`; an `XxxOption =
  option.Option[XxxOptions]` alias names the parameter type.
- **Presets**: the struct itself implements `ApplyTo` on a value receiver, so
  a complete configuration can be passed as one option.
- Constructors take `opts ...XxxOption`; nil options are ignored; options
  apply in order, last write wins.
- Option code lives in `<name>_opts.go`.

```go
type RenderOptions struct{ Namespace string }

// Preset: the struct is itself an option.
func (opts RenderOptions) ApplyTo(target *RenderOptions) { *target = opts }

type RenderOption = option.Option[RenderOptions]

// Function option.
func WithNamespace(namespace string) RenderOption {
	return option.FunctionalOption[RenderOptions](func(target *RenderOptions) {
		target.Namespace = namespace
	})
}
```

Both forms compose in one call: `renderer.Render(ctx, def, file,
RenderOptions{Namespace: ns}, WithPostRenderer(p))`.

## Hygiene

- Never commit `.context/`.
- Generated code (`zz_generated.*`, `config/crd/bases`) is regenerated, never
  hand-edited. The generated CRD
  `config/crd/bases/config.opendatahub.io_platformmoduledefinitions.yaml` is
  tracked so clean checkouts build; regenerate it with the pinned
  `make manifests` after API edits. Host build and test targets never
  regenerate it automatically, and the container build regenerates it inside
  the builder stage.
