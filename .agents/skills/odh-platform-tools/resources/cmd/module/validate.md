# Validate a running module (live, read-only)

`module validate` loads one `PlatformModuleDefinition` and checks the
running module it describes against the platform contract. It is
strictly read-only — it never renders, applies, or mutates anything —
and it does **not** render the definition's manifests: rendering
sources and image tokens are not required, so the same definition
file used for deploy can be validated from a machine that has neither
the chart nor the Kustomize tree. A matching report is evidence about
what the cluster serves, not a change to it.

Shared setup, the pinned `go run` invocation, and connection-safety
rules are defined in [the module onboarding](../module.md).

## Prerequisites and inputs

- The module must be deployed and its instance applied: see
  [deploy](deploy.md).
- The definition file (the same one used for deploy).
- Read-only access on the target cluster: API discovery for the
  module's group/version, plus `get` on the module custom resource,
  the controller Deployment, and the platform ConfigMap
  (`odh-<definition-name>-config`) in the resolved namespace. No CRD
  object is read and nothing is written.
- An explicitly selected target: pass `--kubeconfig`, `--context`,
  and `--namespace` for every invocation rather than relying on
  ambient context. The namespace resolves with kubectl semantics
  (explicit flag, else context namespace, else `default`) inside the
  timed action.

Validate is safe to run without further approval because it only
reads, but it still names a cluster: confirm the target the same way
you would before deploy.

## Invocation

```sh
go run "github.com/lburgazzoli/odh-platform-tools/cmd@${ODH_TOOLS_REF:?pin ODH_TOOLS_REF}" \
  module validate \
  --config <definition.yaml> \
  --kubeconfig <kubeconfig> --context <context> --namespace <namespace>
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--config` | — | Definition file (required) |
| `--timeout` | `30s` | Overall budget: connection, discovery, reads, and waiting; must be positive |
| `--output` | `text` | Report format: `text` or `json` |

There is **no `--wait` flag**. Waiting is automatic: the first
observation cycle runs immediately, and cycles repeat at a one-second
interval under the single overall `--timeout` budget until every
check passes in one cycle or the budget expires. A bounded CI
pipeline that allows for slow rollouts raises the budget instead of
retrying forever:

```sh
# Bounded CI example: allow up to five minutes, still a hard deadline
go run "github.com/lburgazzoli/odh-platform-tools/cmd@${ODH_TOOLS_REF:?pin ODH_TOOLS_REF}" \
  module validate --config <definition.yaml> --timeout 5m \
  --kubeconfig <kubeconfig> --context <context> --namespace <namespace> \
  --output json
```

An earlier caller deadline shortens the budget. Local setup —
kubeconfig loading, file reads, exec-based credential plugins — is
not hard-interruptible and may block past the deadline; the inherited
`--request-timeout` bounds each single request and never extends the
budget.

## The ten checks

Every invocation reports the same ten checks, each `pass`, `fail`
(an observed defect or not-yet-healthy state), `skipped` (a required
input is unavailable), or `error` (the read itself failed):

| ID | Verifies | Skipped when |
|----|----------|--------------|
| `MODULE_API` | The module group/version is discoverable, serves the module kind cluster-scoped, and advertises `/status` | Never |
| `MODULE_EXISTS` | The module custom resource exists and is not terminating | No usable cluster-scoped API mapping |
| `MODULE_CONTRACT` | Observed status is structurally valid: phase, observedGeneration, conditions, releases | Mapping or custom resource unavailable |
| `MODULE_READY` | Current `status.observedGeneration`, `Ready=True`, `ProvisioningSucceeded=True`, `status.phase == Ready` | Mapping or custom resource unavailable |
| `DEPLOYMENT` | The controller Deployment exists, is not terminating or scaled to zero, has a current generation, all replicas updated/ready/available, and no `ProgressDeadlineExceeded` | Namespace unresolved |
| `CONTROLLER_IMAGE` | The controller container runs the defined image: literal must match exactly; a `{{SOURCE}}` token must have resolved to a nonempty image that is not the raw token | No defined controller image, or Deployment unavailable |
| `INIT_CONTAINER` | The configured init container exists and runs the controller image (or its own when the definition gives it one) | Definition configures no init container, or Deployment unavailable |
| `RELATED_IMAGES` | Every declared related image is a nonempty env var on the controller container | Definition declares none, or Deployment unavailable |
| `PLATFORM_CONFIG` | The `odh-<definition-name>-config` ConfigMap exists with a valid semantic version at `data.platformVersion` | Namespace unresolved |
| `PLATFORM_CONSUMED` | The module reports a `platform` release whose normalized version equals the ConfigMap's normalized version | Custom resource or structurally valid releases unavailable |

What the findings distinguish:

- **Stale or malformed status** — `MODULE_CONTRACT` shape violations
  and `MODULE_READY` findings such as an outdated
  `observedGeneration`, a non-True `Ready`/`ProvisioningSucceeded`,
  or a phase other than `Ready`.
- **Missing instance or API** — `MODULE_API` (discovery 404) and
  `MODULE_EXISTS` (GET NotFound) findings; downstream status checks
  are skipped, not failed.
- **Image mismatch** — `CONTROLLER_IMAGE`, `INIT_CONTAINER`, and
  `RELATED_IMAGES` findings, including an unresolved `{{SOURCE}}`
  token still running as the raw token text.
- **Deployment health** — `DEPLOYMENT` findings for unready,
  scaled-to-zero, or progress-deadline-exceeded controller rollouts.
- **Platform config/release consistency** — `PLATFORM_CONFIG`
  (missing or invalid version) versus `PLATFORM_CONSUMED` (module
  reports a different version). A matching platform release is
  read-only consistency evidence. It does **not** prove that the
  module read this particular ConfigMap, watches its updates, or
  consumes every key.

## Waiting, findings, and execution failures

- Ordinary findings — a missing API, custom resource, Deployment, or
  ConfigMap, malformed or stale status, an unready or scaled-zero
  Deployment, a mismatched version — are **retryable**: each new
  cycle re-observes them until the budget expires.
- Any other read error — forbidden (insufficient read permissions),
  connection failure, or a per-request timeout firing while the
  invocation budget is still live — is a **prompt runtime execution
  failure**: the invocation returns immediately with the partial
  result instead of waiting out the budget.
- The report always contains the **latest** cycle, not a finding
  history. Checks retained by an execution failure are included.
- An expired invocation still prints its latest checks with the
  timeout failure; cancellation (SIGINT/SIGTERM) is classified like
  any other failure.

## Report and exit codes

The JSON report has `definition`, `namespace`, overall `status`
(`pass` | `failed`), `checks` (always an array; entries carry `id`,
`status`, object identity, and where applicable `field` and
`message`), and on an execution failure a classified `failure
{category, message}`. The text report prints every check — passes,
skips, and retained checks included — followed by the failure line;
execution failures also print one summary line to stderr.

- Exit 0 only when every check passes.
- Exit 1 for findings, runtime, timeout (`timeout`), or cancellation
  (`cancelled`) failures.
- Exit 2 for usage, invalid options, or definition input errors
  (`invalid-options`, `definition`).
- Through `go run`, nonzero CLI exits normally surface as wrapper
  exit 1 with `exit status N` on stderr — do not branch on
  `$? == 2`; read the report instead.

## Common failures and recovery

| Symptom | Cause | Recovery |
|---------|-------|----------|
| `error` check statuses, runtime failure | Read failed: forbidden, connection, or per-request timeout | Check read permissions and cluster reachability; the report retains the partial cycle |
| Checks stay `fail` until timeout | Retryable findings: module not yet rolled out or ready | Wait for rollout; raise the bounded `--timeout` if legitimate — never loop forever |
| `MODULE_API`/`MODULE_EXISTS` fail | Instance not applied, or wrong group/version/kind in the definition | Apply the instance ([deploy](deploy.md)); fix the definition |
| `CONTROLLER_IMAGE` fail with raw `{{SOURCE}}` text | Image source unresolved at deploy time | Re-deploy with the value in the environment or `--related-images-file` |
| `PLATFORM_CONSUMED` fail | Module reports a different platform release than the ConfigMap | Re-deploy with the intended `--platform-version`, or wait for the module to converge |
| Unclear classified message | Raw causes withheld | Re-run with `--debug` to print the full error chain to stderr |

Diagnose with `--debug` and `--output json`; both are read-only and
safe to combine.

## Next step

For a module whose controller is not yet implemented — or to exercise
status behavior against a real contract — see [mock](mock.md).
