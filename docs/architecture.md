# Architecture

One binary, two commands, shared services. `cmd/` wires Cobra commands to
injected action functions; `pkg/` holds the reusable services and never
imports Cobra.

## Package map

| Package | Responsibility |
|---------|----------------|
| `cmd/main.go` | Root command, signal→context, exit-code contract |
| `cmd/module` | `module` command group |
| `cmd/module/lint` | `module lint` command, report output, exit classification |
| `cmd/module/deploy` | `module deploy` command, report output, exit classification |
| `pkg/module` | Facade wiring the default linter (`NewLinter`) |
| `pkg/module/api/v1alpha1` | `PlatformModuleDefinition` API, Scheme, generated deepcopy |
| `pkg/module/loader` | Decode one definition document (unknown fields dropped) + placement defaults |
| `pkg/module/renderer` | Render engine: backend selection, ordering, cancellation, reserved-resource guard |
| `pkg/module/renderer/helm` | Helm backend (chart + literal values) |
| `pkg/module/renderer/kustomize` | Kustomize backend |
| `pkg/module/renderer/source` | Manifest path resolution shared by backends |
| `pkg/module/linter` | Lint action: load → render → checks |
| `pkg/module/linter/checks` | Check composition (`And`) |
| `pkg/module/linter/checks/{types,deployments,crd}` | Manifest checks |
| `pkg/module/deployer` | Deploy orchestration: staged Load/Render/Deploy/Wait |
| `pkg/module/deployer/adapt` | Image/env injection and platform-config ConfigMap; framework ownership bridge |
| `pkg/module/deployer/cluster` | Connection and namespace resolution from a REST config getter |
| `pkg/module/deployer/images` | Related-image snapshot from dotenv file + environment |
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

## deploy pipeline

`Deployer` is staged and single-use: `Load → Render → Deploy → Wait`
(`Apply` runs them under one timeout; failures are terminal).

```
Load    validate options, parse platform version, load definition,
        snapshot related images (env over dotenv file)
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

## Design rules

- `pkg/` never imports Cobra; commands configure options and call actions.
- The render engine owns backend selection, apply-ordering, cooperative
  cancellation, and the reserved-resource guard; backends only wrap native
  Helm/Kustomize renderers.
- Errors are classified by `pkg/module/errors` category identity
  (`errors.Is`), never by message text; commands map categories to exit
  codes (0 success, 1 runtime/cancellation, 2 usage/definition).
