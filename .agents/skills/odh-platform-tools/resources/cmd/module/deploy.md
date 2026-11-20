# Deploy a module package (live)

`module deploy` is the one-shot live installer: it loads one
`PlatformModuleDefinition`, renders its Helm or Kustomize backend,
injects images and platform configuration, publishes the definition
owner, and applies the rendered controller package to one cluster.
It never creates the module custom resource (the instance) — that is
applied separately after deploy.

This recipe writes to a cluster. Shared setup, the pinned `go run`
invocation, connection-safety rules, and the `ODH_TOOLS_REF` pin are
defined in [the module onboarding](../module.md); read it first.

## Prerequisites and inputs

- A lint-clean definition file. Generate and lint it first:
  see [generate](generate.md) and [lint](lint.md).
- Explicit user approval to write to the target cluster, and a named
  target. Never assume which cluster a kubeconfig points at: confirm
  it with the same connection flags you will pass to deploy.
- The target namespace must already exist. Deploy does not create it.
- Prerequisite APIs the package depends on (for example cert-manager)
  must already be installed — deploy applies only what the definition
  renders plus the definition owner and platform ConfigMap.
- RBAC for the invoking identity. Applies are server-side applies, so
  the required verbs are `get`, `create`, and `patch` — never only
  `create`/`update`: CRD establishment polling gets the
  `CustomResourceDefinition`, and with `--wait` the rollout
  observation gets the controller Deployments. Concretely: `get`,
  `create`, and `patch` on the cluster-scoped
  `CustomResourceDefinition` and `PlatformModuleDefinition` objects;
  `get`, `create`, and `patch` on every rendered package resource —
  namespaced kinds (Deployments, ServiceAccounts, Roles,
  RoleBindings, ConfigMaps) in the target namespace, and
  cluster-scoped kinds (ClusterRoles, ClusterRoleBindings, and the
  module's own CRD) cluster-wide.
- The controller image referenced by the definition must be pullable
  by the cluster's nodes. Private registries need an explicitly
  chosen credential approach — do not embed credentials in the
  definition, the related-images file, or rendered values.
- Current values for every image source the definition declares,
  either in the process environment or in a current
  `--related-images-file` (see below).
- The platform semantic version (`--platform-version`), parsed once
  and shared by the platform ConfigMap and the deploy pass.

## Invocation

Use the pinned `go run` form with an explicitly configured
`ODH_TOOLS_REF` (a released version or commit accepted by Go — never
`main` or `latest`):

```sh
go run "github.com/lburgazzoli/odh-platform-tools/cmd@${ODH_TOOLS_REF:?pin ODH_TOOLS_REF}" \
  module deploy \
  --config <definition.yaml> \
  --platform-version <semver> \
  --related-images-file <related-images.yaml> \
  --kubeconfig <kubeconfig> --context <context> --namespace <namespace> \
  --wait
```

Connection flags are inherited from the root command with kubectl
semantics (`--kubeconfig`, `--context`, `--namespace`, auth flags).
The namespace resolves as: explicit flag, else the context's
namespace, else `default`. Pass explicit flags for every deploy of a
named target; do not rely on ambient context.

Key flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--config` | — | Definition file (required) |
| `--platform-version` | — | Platform semantic version (required) |
| `--related-images-file` | — | YAML file of related image sources |
| `--wait` | `false` | Wait for the controller Deployment rollout after apply |
| `--timeout` | `5m` | Overall invocation budget, connection included |
| `--wait-timeout` | `30s` | Rollout wait budget; capped by remaining `--timeout` |
| `--field-manager` | `platform.opendatahub.io` | SSA field manager (≤63 chars, label value) |
| `--monitoring-namespace` | — | Injected as `MONITORING_NAMESPACE` |
| `--platform-type` | `OpenDataHub` | Injected as `ODH_MODULE_OPERATOR_PLATFORM_TYPE` |
| `--output` | `text` | Report format: `text` or `json` |

### Related-image resolution

`--related-images-file` takes a YAML file regardless of extension;
dotenv files are not accepted:

```yaml
relatedImages:
- name: RELATED_IMAGE_EXAMPLE_CONTROLLER
  value: quay.io/example/example-controller:v1.0.0
```

- Entry names are case-sensitive and must match the definition's
  `spec.relatedImages` names and `{{VARIABLE}}` image tokens.
- A matching process environment variable takes precedence over the
  file value; an explicitly empty environment variable masks the file
  value. Use masking deliberately — an empty value means "resolve
  from the environment, ignore the file" for that name.
- Missing, null, and empty entry values supply no file value. Omit
  the flag to resolve sources from the environment alone.
- Generation copies only the names, never the values, into the
  definition; deploy resolves values from the current environment or
  the file passed now. Configure the real controller image in the
  chart or Kustomize source before generating, not at deploy time —
  deploy never rewrites controller images.
- Keep operand image values (related images) distinct from the
  controller image: the controller image comes from the definition's
  `spec.controller.deployment.container.image` (literal or
  `{{VARIABLE}}` token); related images become env vars on the
  controller container.

## What deploy writes

In order:

1. **Definition owner.** Deploy applies the embedded
   `PlatformModuleDefinition` CRD (cluster-scoped), waits for it to
   be established, then applies a sanitized copy of your definition
   object.
2. **Rendered package.** The framework deploy action applies the
   rendered resources, with:
   - image env injection (related images, `MONITORING_NAMESPACE`,
     platform type) into the controller containers;
   - a platform ConfigMap named `odh-<definition-name>-config` in the
     target namespace with `data.platformVersion` set to the parsed
     `--platform-version`. An existing ConfigMap with that exact
     identity is merged: its data is preserved except
     platform-managed cert-manager keys, which are removed.

Both passes use Server-Side Apply under `--field-manager` with forced
ownership. The default manager is `platform.opendatahub.io`; a custom
one must be a nonempty Kubernetes label value of at most 63
characters.

Deploy is **not atomic and performs no rollback**. The apply pass
continues on per-resource errors and is retried (up to five attempts
with backoff) for retriable server errors, so a failed invocation can
leave partial writes behind. The report names the attempted and
completed stages and whether the apply completed; a missing owner in
the report means the owner identity is unconfirmed, not that nothing
was written. Inspect the cluster before retrying.

## Budgets: overall versus rollout wait

- `--timeout` (default `5m`) is the overall invocation budget,
  including connection setup, rendering, apply, and any wait.
- `--wait-timeout` (default `30s`) is the rollout wait budget used
  only when `--wait` is enabled, and it is capped by the remaining
  overall budget — the first deadline wins.
- `--wait` waits only for the controller **Deployment rollout**.
  Rollout is not module readiness: the module is ready when its
  custom resource reports a ready status, which cannot happen until
  the instance exists. A green `--wait` is permission to continue,
  not success.
- During a wait, one failed observation is a problem, not a verdict:
  polling continues under the wait budget, and a failed wait reports
  the budget outcome plus the most recent observation problem. A
  problem superseded by a later clean observation is not reported.

## Apply the instance separately

Deploy never creates the module CR. After the controllers start,
apply the instance manifest with an explicitly selected target:

```sh
kubectl --kubeconfig <kubeconfig> --context <context> apply -f <instance.yaml>
```

The instance must match the definition's `spec.instance` identity
(name defaults to the definition name; type is the module GVK) and is
typically cluster-scoped. The bundled `examples/helm/instance.yaml`
and `examples/kustomize/instance.yaml` are examples specific to the
tools repository checkout — use your own instance manifest.

## Observable results

- Success: report `status: applied`, confirmed owner identity, and
  with `--wait` a completed wait state. Exit 0.
- Failure: report `status: failed` with attempted/completed stages,
  `applyCompleted`, and a classified failure block; one summary line
  on stderr. Exit 1 for runtime, connection, apply, wait, timeout, or
  cancellation failures; exit 2 for usage, local definition, or
  image-file errors.
- Through `go run`, a nonzero CLI exit normally surfaces as wrapper
  exit 1 with `exit status N` on stderr — preserve the failure, do
  not branch on `$? == 2`. Go compilation and module download time is
  outside the CLI's own timeout.

## Common failures and recovery

| Symptom | Cause | Recovery |
|---------|-------|----------|
| Exit 2, `definition` category | The definition file fails loading or validation | Fix the file; re-run [lint](lint.md) first |
| Exit 2, `invalid-options` | Bad `--platform-version` (not parseable semver) or empty `--field-manager` | Correct the flag value |
| Exit 2, `image-file` category | Malformed related-images YAML, or a required `{{VARIABLE}}` image cannot be resolved | Fix the file or export the value; masked empty values are deliberate |
| Forbidden on CRD or definition apply | Missing cluster-scoped RBAC | Grant the invoking identity CRD and definition permissions |
| Namespace-related apply errors | Target namespace does not exist | Create it (or fix the flag), then re-run deploy; SSA re-apply is the normal repair path |
| ImagePullBackOff after apply | Controller or operand image not pullable by the nodes | Fix the image reference or registry access; re-run deploy |
| Wait failed under budget | Deployment rollout not complete within `--wait-timeout` (capped by `--timeout`) | Inspect the Deployment; raise `--wait-timeout`/`--timeout` and re-run if legitimate |
| Unclear classified message | Raw causes withheld from the summary | Re-run with `--debug` to print the full error chain to stderr |

Partial writes from a failed deploy are repaired by fixing the input
and re-running deploy — SSA re-applies owned fields — but resources
no longer in the rendered set are not pruned by deploy.

## Next step

Apply the instance, then check the running module read-only with
[validate](validate.md).
