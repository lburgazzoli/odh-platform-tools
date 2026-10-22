# odh-platform-tools

Offline lint tooling for OpenDataHub platform module definitions.

`module lint` loads one `PlatformModuleDefinition`, renders its selected
manifest backend (Helm or Kustomize) with the actual platform framework
actions, and validates the rendered objects against the packaging rules
the ODH operator enforces at integration time — all without a cluster,
credentials, network access, containers, or external renderer binaries.

## Quickstart

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd module lint \
  --config examples/helm/definition.yaml
```

```
module lint examples/helm/definition.yaml: pass
```

The Kustomize example works the same way:

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd module lint \
  --config examples/kustomize/definition.yaml
```

Both examples under [`examples/`](examples/) are complete, self-contained
module roots: the definition sits at the root and every referenced file
(chart, kustomization, sibling base) lives inside it. Neither example
implies a runnable controller or a cluster.

## The module definition

A module is described by one file, `definition.yaml`, typed as plain
data (`config.opendatahub.io/v1`, kind `PlatformModuleDefinition`). It
is a file-only document: no Kubernetes Scheme, no generated deepcopy, no
cluster API submission. Loading is strict — unknown members, duplicate
YAML keys, and structural violations are rejected with file and line
positions.

```yaml
apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: example                      # module identity; also the Helm release name
spec:
  type:                              # the Module custom resource the controller owns
    group: components.platform.opendatahub.io
    version: v1alpha1
    kind: Example
  manifests:
    format: helm                     # helm | kustomize; exactly one backend block
    helm:
      path: ./manifests/chart        # chart root, relative to the module root
  relatedImages:                     # optional, see below
  - RELATED_IMAGE_EXAMPLE_CONTROLLER
  controller:
    deployment:
      name: example-controller
      container:                     # the main container; initContainer uses the same type
        image: quay.io/example/example-controller:v1.0.0
        env:
        - name: LOG_LEVEL
          value: debug
```

### Fields

| Field | Notes |
| --- | --- |
| `spec.type` | Group, version, and kind of the Module custom resource. All three are required. |
| `spec.manifests.format` | `helm` or `kustomize`. The block matching the format is required; the other must be absent. |
| `spec.manifests.helm.path` | Chart directory relative to the module root. Must contain `Chart.yaml`. The release name is `metadata.name`; no repositories, credentials, or extra values are involved. Chart dependencies must be vendored under `charts/`. |
| `spec.manifests.kustomize.path` | Directory containing `kustomization.yaml` (or `.yml`), relative to the module root. Sibling bases and resource files inside the module root are allowed; there is no `default/` fallback. |
| `spec.controller.deployment.name` | The controller Deployment the rendered manifests must contain exactly once. |
| `spec.controller.deployment.container` | The main container. `name` defaults to `manager` when omitted; `image` is required and may be a literal or a whole-value `{{VARIABLE}}` reference. |
| `spec.controller.deployment.initContainer` | Optional init container; same type as `container` but requires an explicit `name`. There is no inheritance between the two placements. |
| `spec.controller.deployment.*.env` | Explicit environment variables. `value` is required and non-null; an explicit empty string is valid. |
| `spec.relatedImages` | Optional list of portable, unique environment-variable-style names. A future runner will inject them into both selected containers; lint only checks uniqueness and collision with explicit `env` names. |

Lint neither resolves nor injects declarations: `{{VARIABLE}}` image
references and `relatedImages` names are validated for shape only, and
the rendered manifests keep whatever image and env the chart or
kustomize tree authors. No replacement sources, environment discovery,
or settings files exist in this delivery.

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
  "definition": "examples/helm/definition.yaml",
  "status": "findings",
  "hasErrors": true,
  "hasWarnings": false,
  "diagnostics": [
    {
      "id": "DEPLOYMENT_MISSING",
      "severity": "error",
      "phase": "deployment",
      "message": "controller deployment not found in rendered objects"
    }
  ]
}
```

`status` is `pass`, `findings`, or `failed`. `diagnostics` is omitted
when empty; `failure` (with `category` and `message`) is present only
when execution did not complete.

## What the linter checks

All rules are fixed and upstream-based; there are no custom policies.

**Packaging** (`resources` phase): every rendered object must be a
supported kind at its pinned version and scope (`apps/v1 Deployment`,
core `Service`/`ServiceAccount`/`ConfigMap`, `rbac.authorization.k8s.io/v1`
Role/RoleBinding/ClusterRole/ClusterRoleBinding, `apiextensions.k8s.io/v1`
CustomResourceDefinition, admissionregistration and cert-manager kinds,
`networking.k8s.io/v1` NetworkPolicy). SecurityContextConstraints are
allowed only for the `ray` module. List objects are unsupported, and
duplicate objects are findings.

**Controller** (`deployment` phase): exactly one `apps/v1` Deployment
with the declared name; the selected main (and init) container must
exist exactly once per list; `matchLabels` must be covered by the pod
template labels; a non-default `serviceAccountName` must resolve
against the bundle.

**References** (`rbac` phase): RoleBinding `roleRef` and subjects must
resolve within the rendered bundle. Empty namespaces are interpreted as
`default` for these local comparisons only; objects are never mutated.

**Module CRD** (`crd` phase): one cluster-scoped CRD for the declared
group/kind with a correct plural-derived name, unique version names,
exactly one storage version, the configured version served, a status
subresource, and a structural schema whose `status` demonstrably
exposes `observedGeneration`, the standard condition fields, and
`releases` items with `name` and `version`.

**Singleton evidence** (`singleton` phase): the module CR must be
enforced to a fixed name. Recognized static evidence is an
unconditional CEL equality on `self.metadata.name` (or equivalent
anchor, operand-order insensitive) against a literal valid name, or a
single-value name enum. Otherwise, a relevant validating webhook
(`failurePolicy: Fail`, create on the exact group/version/plural,
cluster scope, referencing a bundled Service) downgrades the outcome to
the warning `SINGLETON_WEBHOOK_UNVERIFIED`; no evidence at all is the
error `SINGLETON_EVIDENCE_MISSING`.

## Architecture

```
             ┌─────────────────────────── cmd/main.go ───────────────────────────┐
             │  signal context, Cobra execution, process exit codes              │
             └───────────────────────────────┬───────────────────────────────────┘
                                             │
             ┌─────────────────────────── pkg/cli ──────────────────────────────┐
             │  `module lint`: --config/--output flags → plain linter.Options,  │
             │  report formatting, exit-code classification                     │
             └───────────────────────────────┬───────────────────────────────────┘
                                             │ module.NewLinter().Run(ctx, options)
             ┌──────────────────── pkg/module/linter (action) ───────────────────┐
             │  options → load → select backend → render → pure validate        │
             └───────┬────────────────────────────────────────┬──────────────────┘
                     │ loader.New().Load                      │ helm.Render / kustomize.Render
     ┌───────────────▼──────────────┐        ┌────────────────▼───────────────────┐
     │ pkg/module/loader            │        │ pkg/module/renderer/{helm,kustomize}│
     │ strict YAML → typed config,  │        │ source capture + containment guard,│
     │ entry resolution, validation │        │ framework action wrappers          │
     └──────────────────────────────┘        └────────────────▼───────────────────┘
                                                             │ []unstructured.Unstructured
                                             ┌───────────────▼────────────────────┐
                                             │ pkg/module/linter (pure validator) │
                                             │ in-memory rules, no I/O            │
                                             └────────────────────────────────────┘
```

- **Cobra configures options only.** All orchestration lives in the
  Cobra-free action; the command holds no business logic, and no
  `pkg/` package imports Cobra.
- **Plain JSON, no Scheme.** The definition document is plain data with
  JSON tags — no runtime.Object, no Scheme registration, no generated
  deepcopy. The framework's renderer interface requirement is met by a
  renderer-private, non-owning in-memory adapter; the definition type
  is never turned into a cluster object.
- **Services stay reusable.** The loader and both renderer wrappers
  remain independently callable; the linter's pure validator accepts a
  context, the typed definition, and an object list.
- **Offline by construction.** Transitive renderer inputs are captured
  through root-contained reads and validated before the framework
  actions run; escaping paths and symlinks, remote/Git/OCI sources,
  external generators, exec plugins, and un-vendored chart dependencies
  fail closed. Local-only inputs are the offline boundary; ordinary
  framework rendering semantics are kept.

## Limits

- Lint validates rendered objects, not raw template text: a backend
  that filters or normalizes documents determines what the linter sees.
  Framework render errors are propagated as execution failures, never
  presented as a partial success.
- Framework actions are not interruptible mid-call; cancellation is
  honored before and after each call.
- Empty object namespaces are treated as `default` for local reference
  comparisons only. No cluster state is read or written.
- Webhook and CEL presence is static evidence with explicit proof
  limits, not semantic certification; CEL expressions are parsed, never
  executed.
- Missing bundle references are findings, not assumed external
  prerequisites.

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
