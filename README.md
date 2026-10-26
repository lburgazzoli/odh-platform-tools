# odh-platform-tools

Offline tooling for OpenDataHub platform module definitions: `module
lint` validates a definition's rendered manifests against the
packaging rules the ODH operator enforces, and `module run` renders
and applies a definition to a real cluster with the operator's own
rendering and deployment framework — both without external renderer
binaries, containers, or credentials beyond the cluster connection.

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
example implies a runnable controller: the images are non-sensitive
placeholders.

## The module definition

A module is described by one file, `definition.yaml`, typed as plain
authored data (`config.opendatahub.io/v1alpha1`, kind
`PlatformModuleDefinition`). Loading goes through the framework's
document decoder and unstructured-to-typed conversion — the same
machinery the render actions use. Definition data is assumed correct:
members the typed conversion does not map are dropped, type mismatches
on mapped members are rejected, and duplicate keys fail. Placement
defaults belong to the loader:
an omitted instance name becomes `metadata.name`, and an omitted main
container name becomes `manager`. Structural validation beyond
decoding is deferred and will be added later as JSON Schema
validation.

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
  relatedImages:                     # optional, see image sources below
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
| `spec.instance.type` | Group, version, and kind of the Module custom resource. Descriptive identity: nothing in this tooling creates or looks up the custom resource, and rendering never uses the instance fields. |
| `spec.instance.name` | Optional name of the module's custom resource instance; the loader defaults it to `metadata.name` when omitted. It is not a rendering input. |
| `spec.manifests.format` | `helm` or `kustomize`, selecting the backend block to render. |
| `spec.manifests.helm.path` | Optional chart directory relative to the definition file. Must contain `Chart.yaml`. Omitted or empty — or the whole `helm` block omitted — defaults to the definition file's own directory (the co-located layout). The release name is `metadata.name`; no repositories or credentials are involved. Chart dependencies must be vendored under `charts/`. |
| `spec.manifests.helm.values` | Optional literal Helm values, merged over the chart defaults with the definition winning. No interpolation: `{{NAME}}`-looking text stays literal, like env values. Validated against the chart's `values.schema.json` when present. |
| `spec.manifests.kustomize.path` | Optional directory containing `kustomization.yaml` (or `.yml`), relative to the definition file. Omitted or empty — or the whole `kustomize` block omitted — defaults to the definition file's own directory. Sibling bases and resource files are allowed; there is no `default/` fallback. |
| `spec.controller.deployment.name` | The controller Deployment. When omitted, `module run` falls back to the module's `metadata.name` when selecting the controller Deployment for injection and waiting. |
| `spec.controller.deployment.container` | The main container. `name` defaults to `manager` when omitted (loader default); `image` may be a literal or a whole-value `{{VARIABLE}}` reference resolved only by `module run`. |
| `spec.controller.deployment.initContainer` | Optional init container, selected by `name` only. A resolved main image overrides the named init container too; init-specific image or env settings do not exist. |
| `spec.controller.deployment.container.env` | Explicit environment variables for the main container only. An explicit empty string is a valid value. |
| `spec.relatedImages` | Optional list of portable environment-variable-style names. `module run` resolves them from the file or environment and injects them into the main container only; lint neither resolves nor injects them. |

The instance identity is descriptive data: nothing in this tooling
creates or looks up the custom resource, and rendering never uses
`spec.instance.name` — the Helm release name comes from
`metadata.name`.

Lint neither resolves nor injects declarations: the rendered manifests
keep whatever image and env the chart or kustomize tree authors. Run
does both — see below.

## CLI reference

Both subcommands share the standard kubectl-style flags registered on
the root — `--kubeconfig`, `--context`, `--cluster`, `--server`,
`--namespace`, `--user`, `--token`, `--tls-*` and friends — inherited
from `genericclioptions.ConfigFlags`. Subcommands never register
their own kubeconfig/context/namespace flags, and legacy descriptor
override flags are rejected as unknown. Shell completion follows the
reused kubectl semantics.

### `module lint`

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

| Exit code | Meaning |
| --- | --- |
| 0 | Lint succeeded; warning- or info-only findings allowed. |
| 1 | Error-severity findings, cancellation/deadline, or a renderer/internal execution failure. |
| 2 | Usage, invalid options, or definition configuration errors — including a missing `Chart.yaml` or `kustomization.yaml`. |

Classification is by identifiable error categories, never message
strings. Cancellation takes precedence over every other category;
process signals are handled through the command context.

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

### `module run`

```
module run --config <definition.yaml> --platform-version <version> \
  [--kubeconfig <path>] [--context <name>] [--namespace <namespace>] [options]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--config` | required | Path to the module definition file. |
| `--platform-version` | required | Platform version, parsed once and shared by the platform config and deploy. |
| `--related-images-file` | none | Dotenv file of related image sources; no file is discovered automatically. |
| `--applications-namespace` | effective installation namespace | Namespace the module controller package renders into. |
| `--monitoring-namespace` | none | Monitoring namespace injected as `MONITORING_NAMESPACE`. |
| `--platform-type` | `OpenDataHub` | Platform identity injected as `ODH_MODULE_OPERATOR_PLATFORM_TYPE`. |
| `--timeout` | `5m` | Overall invocation budget; a longer `--wait-timeout` is capped by the remaining overall time (first deadline wins). |
| `--wait` | `false` | Wait for the module controller Deployment rollout after the apply. |
| `--wait-timeout` | `30s` | Shared rollout wait budget when `--wait` is enabled; positive, and never extending the remaining `--timeout`. |
| `--output` | `text` | Report format: `text` or `json`. |
| `--namespace` | kubeconfig context default | Effective installation namespace (standard inherited flag). |

| Exit code | Meaning |
| --- | --- |
| 0 | The run applied successfully (apply completion; with `--wait`, the wait also completed). |
| 1 | Classified runtime failure — render, connection, owner preparation, apply, or wait — or cancellation. |
| 2 | Usage, invalid options, definition configuration errors, or an unreadable related-images file. |

## Run semantics

`module run` composes three explicit stages — **Load**, **Render**,
**Deploy** — plus the optional **Wait**, and reports exactly how far
it got (`attemptedStage`/`completedStage` in the JSON report).

**Settings and flags.** The run resolves one REST configuration from
the standard inherited flags — the kubeconfig file, the explicit
context, and the effective installation namespace — through one
`ConfigFlags` getter. The effective installation namespace is where
the controller package renders; `--applications-namespace` overrides
it for the controller package only. Payload resources that author
their own namespace keep it.

**Rendering.** The run renders with the same engine-backed renderers
lint uses, cooperatively and without a hard render deadline: framework
render calls are not interruptible mid-call, and cancellation is
honored before and after each call while API calls stay bounded by the
remaining invocation deadline. The engine's builtin namespace plugin
places namespace-less resources into the effective applications
namespace and rewrites the references that name a namespace (for
example RoleBinding subjects); co-located same-named entries keep
their order through the engine's ApplyOrder post-renderer; authored
literal Helm values are merged over the chart defaults with the
definition winning.

**Image and env injection.** The controller main container's `image`
may be a literal or a whole-value `{{VARIABLE}}` reference. Reference
resolution is the operator's Viper-style source chain, minus secrets:
an explicit `--related-images-file` dotenv file, then the process
environment, with the environment winning over the file. A resolved
image replaces the main container's image and — when an init container
is configured — the named init container's image too. Declared
`relatedImages` names are injected as env vars into the main container
only. Explicit definition env values replace same-named chart env
vars, removing their `valueFrom`; the platform identity
(`ODH_MODULE_OPERATOR_PLATFORM_TYPE`), `APPLICATIONS_NAMESPACE`, and
`MONITORING_NAMESPACE` are injected the same way. Images are applied
exactly as resolved: no digest hashing or mutation.

**Default success means apply completion.** Without `--wait`, a
successful run means the payload apply returned success — nothing
more. A pending, absent, or never-converging workload causes no
failure, no monitoring call, and no readiness claim. `--wait=false`
is the default.

**The opt-in wait.** `--wait` adds an explicit Wait stage after
Deploy: it observes only the rendered controller Deployment(s) whose
name matches the configured controller deployment name (or the module
name fallback) — never the whole workload, never module health. The
shared wait budget defaults to `--wait-timeout=30s` and can be
raised (`--wait-timeout=5m`); the first deadline wins, so a longer
wait budget never extends the remaining overall `--timeout`. A
successful wait reports the rollout completion and the exact targets
(`wait.targets` in the JSON report); a failed or timed-out wait fails
the run as a runtime failure while the applied state, the confirmed
owner, and the completed deploy stage are retained — the wait is
explicit, not part of Deploy.

**JSON report.** `--output=json` writes one document:

```json
{
  "definition": "examples/helm/chart/module.yaml",
  "status": "applied",
  "owner": {
    "name": "example",
    "uid": "6f4f…",
    "apiVersion": "config.opendatahub.io/v1alpha1",
    "kind": "PlatformModuleDefinition"
  },
  "attemptedStage": "deploy",
  "completedStage": "deploy",
  "applyCompleted": true,
  "wait": {
    "requested": true,
    "completed": true,
    "targets": [{"namespace": "odh-example", "name": "example-controller"}]
  }
}
```

`status` is `applied` or `failed`. `owner` is the confirmed owner
identity — its absence means the identity is unconfirmed, never that
no write occurred: an ambiguous write is never claimed absent. `wait`
appears only when the wait was requested.

## The owner lifecycle

The run's persisted owner is the module definition itself: the single
generated `config.opendatahub.io/v1alpha1`
`PlatformModuleDefinition` CRD, cluster-scoped, carried in the CLI's
embedded CRD set.

- **Preparation and privileges.** Every run applies the embedded
  definition CRD — create-or-update server-side apply, the same path
  whether the server holds it or not, so an existing shared CRD is
  updated — waits for its establishment, then applies the definition
  itself the same way and uses the server-returned identity. It needs
  no namespace prechecks: a payload-supplied `Namespace` that does not
  exist is created by the framework apply before the namespaced
  resources; any other missing namespace fails through normal API
  behavior. Applying CRDs and cluster-scoped resources requires the
  invoking identity to hold those privileges.
- **Persistence.** The authored definition is persisted with
  standard object metadata and its authored literal-value spec —
  normalized placement defaults applied, references unexpanded: the
  stored spec keeps `{{VARIABLE}}` image references and literal Helm
  values as authored, never rendered output or source snapshots.
  Re-running with an updated definition updates the stored spec under
  the same identity.
- **Ownership.** Payload objects (except Namespaces, CRDs, and the
  cluster-scoped definition itself) receive an owner reference to
  the persisted definition. CRDs and Namespaces in the payload are
  deliberately left unowned.
- **Force apply and foreign objects.** The payload is applied with
  force server-side apply: an unowned pre-existing object the
  payload also renders is adopted — its payload fields land, its
  other fields survive the merge, and it gains the owner reference.
  Reserved identities are protected at render: a payload containing
  the configured module type's own kind, or the definition kind, is
  rejected before any write.
- **Partial failure, no rollback, no pruning.** A failed apply
  leaves the writes that preceded the failure standing as partial
  state; there is no rollback. Re-running into a different namespace
  renders and applies there without pruning or migrating the old
  one. Cleanup is manual: deleting the persisted definition (or the
  namespace) is the operator's garbage-collection path.
- **No module CR or operands.** The run never creates or looks up
  the module's own custom resource and never touches operands: it
  applies the rendered package and, when asked, waits for the
  controller Deployment rollout. Nothing else observes or reports
  module health.

## Image source examples

The bundled examples use a literal placeholder image. To use the
related-image sources, author the controller image as a whole-value
reference:

```yaml
  controller:
    deployment:
      container:
        image: '{{RELATED_IMAGE_EXAMPLE_CONTROLLER}}'
```

then resolve it from a dotenv file:

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd module run \
  --config examples/helm/chart/module.yaml \
  --platform-version 2.19.0 \
  --namespace odh-example \
  --related-images-file examples/images.env
```

or from the environment, which wins over the file:

```sh
RELATED_IMAGE_EXAMPLE_CONTROLLER=quay.io/example/example-controller:v1.0.0 \
  go run github.com/lburgazzoli/odh-platform-tools/cmd module run …
```

`examples/images.env` carries the non-sensitive placeholder. There are
no init-specific image or env settings, no `imageEnv`, and no
extra-env interpolation: the resolved image replaces the main and
named init container images, and env injection affects the main
container only.

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
             │  signal context, Cobra execution, exit codes, shared ConfigFlags  │
             └───────────────────────────────┬───────────────────────────────────┘
                                             │
             ┌──────────────────── cmd/module (command group) ───────────────────┐
             │  `module` group: attaches the lint and run subcommands            │
             └───────────────┬───────────────────────────────┬───────────────────┘
                             │                               │
        ┌────────────────────▼───────────────┐  ┌────────────▼─────────────────────┐
        │ cmd/module/lint                    │  │ cmd/module/run                    │
        │ --config/--output → linter.Options │  │ flags → runner.Options, report,   │
        │ report formatting, exit codes      │  │ exit classification               │
        └────────────────────┬───────────────┘  └────────────┬─────────────────────┘
                             │ linter.Run                    │ runner.Run (Load → Render
                             │                               │ → Deploy → optional Wait)
        ┌────────────────────▼───────────────┐  ┌────────────▼─────────────────────┐
        │ pkg/module/loader                  │  │ pkg/module/runner                │
        │ YAML → typed definition,           │  │ owner preparation, connection,   │
        │ placement defaults                 │  │ deploy, images, wait             │
        └────────────────────────────────────┘  └────────────┬─────────────────────┘
                                                                │ shared renderers
                                   ┌────────────────────────────▼──────────────────┐
                                   │ pkg/module/renderer/{helm,kustomize}           │
                                   │ engine-faithful render wrappers, restrictions   │
                                   │ off, namespace plugin and ApplyOrder           │
                                   └────────────────────────┬──────────────────────┘
                                                            │ []unstructured.Unstructured
                                   ┌────────────────────────▼──────────────────────┐
                                   │ pkg/module/linter/checks (lint path only)      │
                                   └───────────────────────────────────────────────┘
```

- **Cobra configures options only.** All orchestration lives in the
  Cobra-free actions; the commands hold no business logic, and no
  `pkg/` package imports Cobra.
- **Plain JSON, no Scheme.** The authored definition is plain data
  with JSON tags — no runtime.Object, no Scheme registration, no
  generated deepcopy. The persisted owner type is the generated
  v1alpha1 definition with its embedded CRD.
- **Services stay reusable.** The loader and both renderer wrappers
  remain independently callable; the runner's owner preparation,
  connection, deploy, image, and wait stages compose behind one
  action.
- **Engine-faithful rendering.** The framework actions render the
  configured entry directly with engine load restrictions off, so
  rendering behaves like a plain `kustomize build` or `helm template`
  over the same tree: sibling bases, patches, and generators load as
  authored, and any input the engine rejects fails with the engine's
  own error.

## Parity boundaries

The run shares the ODH operator's rendering and deployment machinery
— recorded at `odh-platform-utilities/framework
v0.2.1-0.20261021162150-201e66e4e90a`, `k8s-manifest-kit/engine
v0.2.1-0.20260909120554-85414598bec8`, `renderer-helm v0.4.0`,
`renderer-kustomize v0.1.1-0.20260909120749-5c7210e9322c` —
including the struct post-renderers, the builtin namespace plugin,
and ApplyOrder. It is deliberately not the operator:

- No config-hash restart: applied Deployments carry no injected
  checksum annotation and no rollout restart is forced.
- No automatic lint: `module run` does not run the lint checks.
- No gate handling: upgrade-gate labels in the payload are ordinary
  labels.
- No xKS bootstrap and no out-of-band namespace or dependency
  provisioning: namespaces and dependencies come from the payload
  apply or fail through normal API behavior.
- Helm literal definition values are supported; Viper/runtime-derived
  values and `NamespaceValueKey` are not.
- No module CR creation, status handshake, or operand reconciliation.

Movement on the framework `main` revision requires a parity review
before upgrading.

## Testing

- **Offline gates** — `make verify`, `make test-integration` — run
  the unit and offline integration suites; they never create a
  cluster and need no credentials.
- **`make test-e2e-compile`** compiles the live e2e suite without
  running it: no setup, no cluster, no credentials.
- **`make test-e2e-kind`** runs the live suite against an isolated,
  engine-owned Kind cluster: a uniquely named cluster with a private
  kubeconfig, created and deleted by the test engine, with the node
  image pinned by digest. No ambient context is used, nothing
  outside the exact cluster is purged, and no secrets are involved.
- **`make test-e2e-external`** runs the same suite against an
  explicitly confirmed external cluster
  (`ODH_PLATFORM_TOOLS_E2E_EXTERNAL_KUBECONFIG` and
  `ODH_PLATFORM_TOOLS_E2E_EXTERNAL_CONTEXT`, both required). The
  external cluster is never deleted by the suite.

The live suite fails hard — never skips — when its prerequisites are
missing, and its scenarios clean up only the objects they created,
by exact server identity. There is no synthetic controller build or
image publication workflow: live rollout waiting uses a pinned
generic busybox image with chart-authored init and main commands.

## Limits

- Lint validates rendered objects, not raw template text: a backend
  that filters or normalizes documents determines what the linter sees.
  Framework render errors are propagated as execution failures, never
  presented as a partial success.
- Framework actions are not interruptible mid-call; cancellation is
  honored before and after each call, and every cluster call stays
  bounded by the remaining invocation deadline.
- Checks are static: they never read cluster state, resolve images,
  or execute policy. A container the definition does not select is
  not examined; the deployment name declared by the definition is not
  itself enforced.
- Run reports apply completion, not workload health; only a
  requested, successful wait reports controller rollout completion.

## Deferred

The following are explicitly deferred and not stubbed: the TCK, the
synthetic test controller, container image publishing, and
configurable lint exceptions or external-reference declarations.
Unknown commands and legacy descriptor override flags are rejected.

## Development

```sh
make build              # build all packages
make generate           # regenerate deepcopy code (controller-gen)
make manifests          # regenerate the definition CRD (controller-gen)
make verify             # fmt-check, vet, lint, unit tests (race)
make test-integration   # offline integration suite (build tag: integration)
make test-e2e-compile   # compile the live e2e suite without running it
make test-e2e-kind      # live e2e suite against an engine-owned Kind cluster
make test-e2e-external  # live e2e suite against a confirmed external cluster
make coverage           # unit test coverage
make vulncheck          # vulnerability check
```

Layout: all code lives under `pkg/` with a singular `module` package
tree (`api/v1alpha1`, `loader`, `renderer/{helm,kustomize}`, `linter`,
`runner`); the parent `pkg/module` is the composition facade;
`cmd/main.go` is only the executable entrypoint. The offline
integration suite lives under `tests/integration` behind the
`integration` build tag; the live e2e suite lives under `tests/e2e`
behind the `e2e` tag, over the test-only cluster engines in
`tests/support/cluster`.
