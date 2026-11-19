# Architecture

The tools binary provides four module commands. `cmd/` wires Cobra
commands to action functions. `pkg/` holds the reusable services and
never imports Cobra.

## Package map

| Package | Responsibility |
|---------|----------------|
| `cmd/main.go` | Root command, signal→context, exit-code contract |
| `cmd/module` | `module` command group |
| `cmd/module/lint` | `module lint` command, report output, exit classification |
| `cmd/module/deploy` | `module deploy` command, report output, exit classification |
| `cmd/module/generate` | `module generate` command, result output, exit classification |
| `cmd/module/mock` | `module mock` command, option validation and cluster connection |
| `pkg/module` | Facade wiring the default linter (`NewLinter`) |
| `pkg/module/api` | Related-image types and the platform status object and list |
| `pkg/module/api/v1alpha1` | `PlatformModuleDefinition` API, Scheme, generated deepcopy |
| `pkg/module/loader` | Decode one definition document (unknown fields dropped) + placement defaults |
| `pkg/module/renderer` | Render engine: backend selection, ordering, cancellation, reserved-resource guard |
| `pkg/module/renderer/helm` | Helm backend (chart + literal values) |
| `pkg/module/renderer/kustomize` | Kustomize backend |
| `pkg/module/renderer/source` | Manifest path resolution shared by backends |
| `pkg/module/linter` | Lint action: load → render → checks |
| `pkg/module/generator` | Generate action: strict discovery → minimal document → create-once publish |
| `pkg/module/generator/values` | Helm values boundary: file plus ordered set expressions → JSON literals |
| `pkg/module/linter/checks` | Check composition (`And`) |
| `pkg/module/linter/checks/{types,deployments,crd}` | Manifest checks |
| `pkg/module/deployer` | Deploy orchestration: staged Load/Render/Deploy/Wait |
| `pkg/module/deployer/adapt` | Image/env injection and platform-config ConfigMap; framework ownership bridge |
| `pkg/module/deployer/cluster` | Connection and namespace resolution from a REST config getter |
| `pkg/module/deployer/images` | Related-image snapshot from YAML and the environment |
| `pkg/module/mock` | Foreground framework controller, scoped watches, condition and release reporting |
| `pkg/module/errors` | Module error taxonomy (category sentinels) |
| `pkg/k8s` | Shared K8s helpers: GVK identity, resource decode, containers |
| `pkg/k8s/{configmap,deployment,crd}` | Typed builders/customizers, CRD establishment wait |
| `pkg/util/{jq,option,retry}` | JSON path checks, functional options, bounded retry |
| `config/crd` | Embedded generated CRD manifest |
| `tests/{integration,e2e,support}` | Offline integration and live e2e suites |

## lint pipeline

```
load (loader.LoadFromPath, decode + defaults)
  → render (renderer.Render, offline)
  → checks (types, deployments, crd)
  → report (text|json on stdout)
```

No cluster access at any point.

## generate pipeline

`Generator` infers one definition from a local source, offline:

```
resolve  source directory, backend detection, output destination
         (Helm destinations inside the chart need a .helmignore rule)
values   optional file plus ordered set expressions → JSON literals
render   bootstrap pass (neutral release name, empty GVK)
discover one CRD with one served storage version → GVK and name;
         one Deployment → manager/sole main container, mandatory
         named init selection
render   final pass under the inferred name and GVK; drift rechecked
validate reserved-resource guard plus the lint checks
publish  minimal document (loader-defaulted fields omitted) to
         stdout, or a create-once 0600 file
```

The emitted document omits exactly the fields the loader defaults, so
reloading it yields the definition the generator validated.

## deploy pipeline

`Deployer` is staged and single-use: `Load → Render → Deploy → Wait`
(`Apply` runs them under one timeout; failures are terminal).

```
Load    validate options, parse platform version, load definition,
        snapshot related images (environment takes precedence over YAML)
Render  renderer.Render with adapt post-renderers:
        EnvInjector (resolved images, namespace/platform env) and
        PlatformConfigInjector (odh-<module>-config ConfigMap)
Deploy  publishDefinition: apply embedded CRD, wait for establishment,
        apply the definition (cluster-scoped owner)
        applyResources: framework deploy action owns and applies the
        rendered payload (SSA, retries with backoff)
Wait    optional (--wait): kubectl rollout semantics on the controller
        Deployments under one shared budget
```

The framework dependency (`odh-platform-utilities/framework`) supplies the
deploy action, resource apply/decode helpers, and the release/ownership
model; `adapt.ControllerBridge` supplies its ownership policy.

## Mock controller

Each controller registers the shared platform status object and its list
in a fresh scheme under the discovered GVK. It watches that module type
and the named ConfigMap in the resolved controller namespace. A
ConfigMap change queues the module instance through the same framework
reconciler as a module annotation change.

The action evaluates the condition annotation and platform version,
then supplies the current dependent conditions and release. The
framework reports the actual pipeline result as
`ProvisioningSucceeded`, drops stale conditions, and derives `Ready`
and phase. The post-status hook normalizes observation metadata before
the framework applies status. The controller leaves the instance spec
and lifecycle to the caller. [Mock status contract](mock.md#conditions-and-platform-version)

## Design rules

- `pkg/` never imports Cobra; commands configure options and call actions.
- The render engine owns backend selection, apply-ordering, cooperative
  cancellation, and the reserved-resource guard; backends only wrap native
  Helm/Kustomize renderers.
- Errors are classified by `pkg/module/errors` category identity
  (`errors.Is`), never by message text; commands map categories to exit
  codes (0 success, 1 runtime/cancellation, 2 usage/definition).
