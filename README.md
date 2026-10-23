# odh-platform-tools

Offline lint tooling for OpenDataHub platform module definitions.

`module lint` loads one `PlatformModuleDefinition`, renders its selected
manifest backend (Helm or Kustomize) with the actual platform framework
actions, and validates the rendered objects against the packaging rules
the ODH operator enforces at integration time — all without a cluster,
credentials, containers, or external renderer binaries.

## Quickstart

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd module lint \
  --config examples/helm/chart/module.yaml
```

```
module lint examples/helm/chart/module.yaml: pass
```

The Kustomize example works the same way:

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd module lint \
  --config examples/kustomize/module.yaml
```

Both examples under [`examples/`](examples/) are complete, self-contained
module roots using the co-located layout: each definition file sits
inside the entry it renders — next to `Chart.yaml` or
`kustomization.yaml` — so neither authors a manifests path. Neither
example implies a runnable controller or a cluster.

## The module definition

A module is described by one file, `definition.yaml`, typed as plain
data (`config.opendatahub.io/v1alpha1`, kind `PlatformModuleDefinition`). It
is a file-only document: no Kubernetes Scheme, no generated deepcopy, no
cluster API submission. Loading is strict at the decoding level —
unknown members, name-case mismatches, and type mismatches are rejected
with file and line positions. Structural validation beyond that is
deferred and will be added later as JSON Schema validation.

```yaml
apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: example                      # module identity; also the Helm release name
spec:
  instance:                          # the module's custom resource identity
    name: default-example            # optional; defaults to metadata.name when omitted
    type:                            # the Module custom resource the controller owns
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: Example
  manifests:
    format: helm                     # helm | kustomize; the backend block is optional
    helm:
      values:                        # optional literal values, over the chart defaults
        replicas: 2
  relatedImages:                     # optional, see below
  - RELATED_IMAGE_EXAMPLE_CONTROLLER
  controller:
    deployment:
      name: example-controller
      container:                     # the main container
        image: quay.io/example/example-controller:v1.0.0
        env:
        - name: LOG_LEVEL
          value: debug
```

The manifests entry may instead be co-located with the definition
file — the form both bundled examples use — in which case the backend
block or its path is omitted and the entry defaults to the
definition file's own directory:

```yaml
  manifests:
    format: helm                     # Chart.yaml sits next to this file
```

### Fields

| Field | Notes |
| --- | --- |
| `spec.instance.type` | Group, version, and kind of the Module custom resource. |
| `spec.instance.name` | Optional name of the module's custom resource instance; defaults to `metadata.name` when omitted. Must be a valid object name (DNS subdomain). It is reserved for a future runner that creates and looks up the custom resource — it is not a rendering input. |
| `spec.manifests.format` | `helm` or `kustomize`, selecting the backend block to render. |
| `spec.manifests.helm.path` | Optional chart directory relative to the definition file. Must contain `Chart.yaml`. Omitted or empty — or the whole `helm` block omitted — defaults to the definition file's own directory (the co-located layout). The release name is `metadata.name`; no repositories or credentials are involved. Chart dependencies must be vendored under `charts/`. |
| `spec.manifests.helm.values` | Optional literal Helm values, merged over the chart defaults with the definition winning. No interpolation: `{{NAME}}`-looking text stays literal, like env values. Validated against the chart's `values.schema.json` when present. |
| `spec.manifests.kustomize.path` | Optional directory containing `kustomization.yaml` (or `.yml`), relative to the definition file. Omitted or empty — or the whole `kustomize` block omitted — defaults to the definition file's own directory. Sibling bases and resource files are allowed; there is no `default/` fallback. |
| `spec.controller.deployment.name` | The controller Deployment the rendered manifests must contain exactly once. |
| `spec.controller.deployment.container` | The main container. `name` defaults to `manager` when omitted; `image` may be a literal or a whole-value `{{VARIABLE}}` reference. |
| `spec.controller.deployment.initContainer` | Optional init container, selected by `name` only. A resolved main image overrides the named init container too; init-specific image or env settings do not exist. |
| `spec.controller.deployment.container.env` | Explicit environment variables for the main container. An explicit empty string is a valid value. |
| `spec.relatedImages` | Optional list of portable environment-variable-style names. A future runner will inject them into both selected containers; lint neither resolves nor injects them. |

The instance identity is descriptive data: nothing in this delivery
creates or looks up the custom resource, and rendering never uses
`spec.instance.name` — the in-memory module instance and the Helm
release name both come from `metadata.name`. Loading is strict about
the layout: the superseded top-level `spec.type` location is rejected
as an unknown member, including when it appears together with
`spec.instance`.

Lint neither resolves nor injects declarations: the rendered manifests
keep whatever image and env the chart or kustomize tree authors. No
replacement sources, environment discovery, or settings files exist in
this delivery.

## CLI reference

```
module lint --config <definition.yaml> [--output=text|json]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--config` | required | Path to the module definition file. |
| `--output` | `text` | Report format: `text` or `json`. |

Exactly one report is written to stdout for every executed lint
invocation; classified execution failures are additionally summarized
on stderr. Help and flag-parse failures are distinct from lint results
and print no report.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Lint succeeded; warning- or info-only findings allowed. |
| 1 | Error-severity findings, cancellation/deadline, or a renderer/internal execution failure. |
| 2 | Usage, invalid options, or definition configuration errors — including a missing `Chart.yaml` or `kustomization.yaml`. |

Classification is by identifiable error categories, never message
strings. Cancellation takes precedence over every other category;
process signals are handled through the command context.

### JSON report

`--output=json` writes one parseable document:

```json
{
  "definition": "examples/helm/chart/module.yaml",
  "status": "findings",
  "hasErrors": true,
  "hasWarnings": false,
  "diagnostics": [
    {
      "id": "DEPLOYMENT_MISSING",
      "severity": "error",
      "message": "module must bundle exactly one Deployment, found 0"
    }
  ]
}
```

`status` is `pass`, `findings`, or `failed`. `diagnostics` is omitted
when empty; `failure` (with `category` and `message`) is present only
when execution did not complete.

## What the linter checks

The default check set mirrors the compliance checks the
opendatahub-operator applies to its own module manifests
(`manifests_compliance_test.go`). Each check lives in its own package
under `pkg/module/linter/checks` and exposes a `Validate` function;
`checks.And` composes them, and the lint action wires the default
set.

**Allowed resource kinds** (`types`): every rendered object must be
of an allowed kind — `apps/v1 Deployment`, core
`Service`/`ServiceAccount`/`ConfigMap`, `rbac.authorization.k8s.io/v1`
Role/RoleBinding/ClusterRole/ClusterRoleBinding,
`apiextensions.k8s.io/v1` CustomResourceDefinition, the
admissionregistration and cert-manager kinds, and
`networking.k8s.io/v1` NetworkPolicy. An allowed kind is allowed for
every module: there are no per-module exceptions, so anything else —
including OpenShift SecurityContextConstraints — is the error
`RESOURCE_KIND_DISALLOWED`.

**Controller Deployment** (`deployments`): the bundle must contain
exactly one Deployment (`DEPLOYMENT_MISSING` /
`DEPLOYMENT_MULTIPLE`), and the containers the definition selects —
the main container, defaulting to `manager`, and the optional init
container when configured — must exist in the Deployment's pod spec
(`CONTAINER_MISSING`).

**Module CRD** (`crd`): the bundle must contain exactly one
CustomResourceDefinition declaring the module's own group and kind
(`MODULE_CRD_MISSING` / `MODULE_CRD_DUPLICATE`). A CRD declares its
type through `spec.group` and `spec.names.kind`, not its own
metadata.

Checks evaluate their objects with jq expressions through
`pkg/util/jq` (`Matchesf`, `Countf`): a missing field is a clean
non-match, while a malformed object is an evaluation error — never a
silent zero. A check that fails to evaluate aborts the run as an
execution error instead of reporting a finding.

## Architecture

```
             ┌─────────────────────────── cmd/main.go ───────────────────────────┐
             │  signal context, Cobra execution, process exit codes              │
             └───────────────────────────────┬───────────────────────────────────┘
                                             │
             ┌─────────────────────── cmd (root command) ─────────────────────────┐
             │  command tree wiring, exit-status mapping, signal context        │
             └───────────────────────────────┬───────────────────────────────────┘
                                             │
             ┌──────────────────── cmd/module (command group) ───────────────────┐
             │  `module` group: attaches the lint subcommand                     │
             └───────────────────────────────┬───────────────────────────────────┘
                                             │
             ┌─────────────────── cmd/module/lint (command) ───────────────────┐
             │  `module lint`: --config/--output flags → plain linter.Options,  │
             │  report formatting, exit-code classification                     │
             └───────────────────────────────┬───────────────────────────────────┘
                                             │ module.NewLinter().Run(ctx, options)
             ┌──────────────────── pkg/module/linter (action) ───────────────────┐
             │  options → load → select backend → render → pure validate        │
             └───────┬────────────────────────────────────────┬──────────────────┘
                     │ loader.LoadFromPath                     │ helm.Render / kustomize.Render
     ┌───────────────▼──────────────┐        ┌────────────────▼───────────────────┐
     │ pkg/module/loader            │        │ pkg/module/renderer/{helm,kustomize}│
     │ strict YAML → typed config,  │        │ request adapter + render wrappers,  │
     │ validation                   │        │ engine-faithful, restrictions off   │
     └──────────────────────────────┘        └────────────────▼───────────────────┘
                                                             │ []unstructured.Unstructured
                                             ┌───────────────▼────────────────────┐
                                             │ pkg/module/linter/checks           │
                                             │ operator-parity checks, no I/O     │
                                             └────────────────────────────────────┘
```

- **Cobra configures options only.** All orchestration lives in the
  Cobra-free action; the command holds no business logic, and no
  `pkg/` package imports Cobra.
- **Plain JSON, no Scheme.** The definition is plain data with
  JSON tags — no runtime.Object, no Scheme registration, no generated
  deepcopy. The framework's renderer interface requirement is met by a
  renderer-private, non-owning in-memory adapter; the definition type
  is never turned into a cluster object.
- **Services stay reusable.** The loader and both renderer wrappers
  remain independently callable; validation is a plain function of the
  typed definition and the rendered object list, and each check can
  run on its own.
- **Engine-faithful rendering.** The framework actions render the
  configured entry directly with engine load restrictions off, so
  rendering behaves like a plain `kustomize build` or `helm template`
  over the same tree: sibling bases, patches, and generators load as
  authored, and any input the engine rejects fails the lint with the
  engine's own error. No Kubernetes client, cluster access, release
  state, or action cache is attached; the definition's entry path is
  resolved against the definition file's location by the render
  request, and its existence and shape are the engine's to validate.

## Limits

- Lint validates rendered objects, not raw template text: a backend
  that filters or normalizes documents determines what the linter sees.
  Framework render errors are propagated as execution failures, never
  presented as a partial success.
- Framework actions are not interruptible mid-call; cancellation is
  honored before and after each call.
- Checks are static: they never read cluster state, resolve images,
  or execute policy. A container the definition does not select is
  not examined; the deployment name declared by the definition is not
  itself enforced.

## Deferred

The following are explicitly deferred and not stubbed: `module run` /
runner, the TCK, the synthetic test controller, container image
publishing, Kind/external-cluster harnesses, and configurable lint
exceptions or external-reference declarations. Unknown commands and
legacy descriptor override flags are rejected.

## Development

```sh
make build          # build all packages
make verify         # fmt-check, vet, lint, unit tests (race)
make test-integration  # offline integration suite (build tag: integration)
make coverage       # unit test coverage
make vulncheck      # vulnerability check
```

Layout: all code lives under `pkg/` with a singular `module` package
tree (`config`, `loader`, `renderer/{helm,kustomize}`, `linter`); the
parent `pkg/module` is the composition facade; `cmd/main.go` is only
the executable entrypoint. The offline integration suite lives under
`tests/integration` behind the `integration` build tag.
