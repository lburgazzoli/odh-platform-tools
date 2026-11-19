# CLI reference

Binary: `odh-platform-tools`. Command group: `module`.

## odh-platform-tools module lint

Loads one definition, renders its manifest backend offline, validates the
rendered objects, and writes a report to stdout. No cluster access.

```
odh-platform-tools module lint --config <definition.yaml> [--output text|json]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | — | Module definition file (required) |
| `--output` | `text` | Report format: `text` or `json` |

Report: status (`pass` | `findings` | `failed`), diagnostics with severity,
and on execution failure a classified `failure` block. Error-severity
findings exit 1 without an execution error.

## odh-platform-tools module deploy

Loads one definition, renders and adapts it, publishes the definition owner
(CRD + object), and applies the controller package through the framework
deploy action. One-shot installer; it never creates the module CR.

```
odh-platform-tools module deploy \
  --config <definition.yaml> \
  --platform-version <semver> \
  [--related-images-file related-images.yaml] [--wait] ...
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | — | Module definition file (required) |
| `--platform-version` | — | Platform version, shared by platform config and deploy (required) |
| `--related-images-file` | — | YAML file of related image sources (no automatic discovery) |
| `--monitoring-namespace` | — | Injected as `MONITORING_NAMESPACE` |
| `--platform-type` | `OpenDataHub` | Injected as `ODH_MODULE_OPERATOR_PLATFORM_TYPE` |
| `--field-manager` | `platform.opendatahub.io` | SSA field manager (≤63 chars, label value) |
| `--timeout` | `5m` | Overall invocation budget, connection included |
| `--wait` | `false` | Wait for the controller Deployment rollout after apply |
| `--wait-timeout` | `30s` | Shared rollout wait budget; capped by remaining `--timeout` |
| `--output` | `text` | Report format: `text` or `json` |

Standard kubeconfig/context/namespace/auth flags are inherited from the root
command (kubectl semantics: explicit flag, else context namespace, else
`default`).

Report: status (`applied` | `failed`), confirmed owner identity, attempted
and completed stages, `applyCompleted`, wait state, and classified
`failure`. A missing owner means unconfirmed, not that no write occurred.

### Related-images file

`--related-images-file` accepts YAML regardless of the file name extension.
This is a breaking change for existing dotenv files. Convert assignments to
entries under `relatedImages`.

```yaml
relatedImages:
- name: RELATED_IMAGE_EXAMPLE_CONTROLLER
  value: quay.io/example/example-controller:v1.0.0
```

Entry names are case-sensitive. A matching process environment variable
takes precedence, and an explicitly empty variable masks its file value.
Missing, null, and empty entry values supply no file value. Omit the flag
to resolve sources from the environment alone.

The loader uses Viper's default decoding and keeps text such as `${VARIABLE}`
literal. File linting and image-reference validation are outside this format
migration.

## odh-platform-tools module generate

Renders the local Helm chart or Kustomize tree at `SOURCE_DIR` offline and
infers a minimal `PlatformModuleDefinition` from the rendered objects, then
validates it with the same checks as `module lint` before writing anything.
No cluster access; the inherited kubeconfig/context flags are accepted and
ignored.

```
odh-platform-tools module generate <SOURCE_DIR> [--output <path|stdout>] ...
```

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | — | Module name; defaults to the lowercase CRD kind |
| `--init-container` | — | Controller init container name; required when the rendered Deployment has init containers |
| `--values` | — | Ordered Helm set expression, each preserved verbatim including commas; repeatable (Helm sources only) |
| `--values-file` | — | Helm values file; a single nonempty occurrence (Helm sources only) |
| `--related-images-file` | — | YAML file of related image sources; only the names are copied |
| `--output` | `stdout` | `stdout` or a file path created once with 0600 permissions |

`--values-file` may be given once and must be nonempty; `--name`,
`--init-container`, and `--output` reject explicit empty values. Repeating
`--values` is intended and applied in order.

### Discovery

Discovery is strict; ambiguous sources fail (exit 2) instead of guessing:

- The source must render exactly one `CustomResourceDefinition`, and that
  CRD must declare exactly one served storage version — it supplies the
  module GVK. Missing, multiple, or dual-storage CRDs fail.
- The module name is the lowercase CRD kind. `--name` overrides it and is
  also the fallback when inference cannot run; an explicit name must be a
  valid DNS label acceptable as a Helm release name.
- The source must render exactly one Deployment. Its main container is the
  unique container named `manager`, or the only regular container; two
  regular containers without a `manager` fail.
- Whenever the Deployment has init containers, `--init-container` must name
  one of them exactly.

Helm sources are rendered twice: a bootstrap pass under a neutral release
name discovers the CRD and Deployment, then the final pass renders under the
inferred name and GVK and the result is rechecked, so charts that bind
Deployment names or images to `.Release.Name` infer correctly. Bootstrap
failures mention `--name`; charts whose discovery depends on the release
name in other ways may still need an explicit `--name`. Not every chart can
be inferred without one.

Main and init container images are copied into the definition unchanged;
they are never rewritten to `{{VARIABLE}}` related-image tokens.

### Values

`--values-file` loads first, then each `--values` flag in order; later
entries override earlier ones. Expressions use Helm `set` syntax — including
commas and escapes, which reach the parser verbatim. Values are JSON
compatible literals and persist in `spec.manifests.helm.values` of the
generated file: booleans, numbers, nested mappings, lists, and explicit
nulls keep their shapes. Integers are stored exactly as authored; reloading
a definition still follows the existing loader behavior where out-of-int64
numbers degrade to float64.

A values file that is not a mapping, or a flag that conflicts with a file
entry's type, fails with exit 2; the file's contents never appear in the
diagnostic. Values options are rejected for Kustomize sources (exit 2).

```sh
odh-platform-tools module generate chart \
  --values-file values-prod.yaml \
  --values replicas=3 \
  --values 'gateway.host=example.com'
```

The file loads first, then each `--values` flag in order: `replicas=3`
overrides the file's `replicas`, and later flags override earlier ones.
Whatever wins is stored in plaintext in the generated file.

### Related images

`--related-images-file` reads the same YAML format as `module deploy` but
copies only the entry names — sorted and deduplicated — into
`spec.relatedImages`. Values and the file path are never stored. Deployment
resolves the names the unchanged way, from the environment or a
`--related-images-file` passed again to `module deploy`.

### Output

`--output stdout` (the default) writes the document to stdout and nothing
else; file successes write no stdout. Relative manifest paths in the
document are resolved against the definition file's directory, so save
stdout in the working directory of the invocation — or adjust `path`
afterwards — before linting or deploying.

A file path must have an existing parent directory; the file is created once
with mode 0600 (subject to umask) and never overwritten. An existing file —
or a path whose symlink target exists — fails with exit 1, leaving the
original bytes untouched.

For Helm sources, a destination inside the chart must be excluded by the
chart's existing `.helmignore`; this protects charts whose templates consume
chart files (for example through `.Files`), which would otherwise render
their own definition. The command never edits ignore files. Shell
redirection (`> chart/gen.yaml`) bypasses this guard and is discouraged.
Kustomize sources have no ignore policy and allow colocated output.

The generated file is an editable starting point. It is not synchronized
with the manifests or image files afterwards; there is no merge, force, or
sync mode.

Generation failures print one classified diagnostic to stderr.
Native renderer failures are reported by stage and source path only —
their raw text is withheld because it may quote chart or values
content. Native libraries may load external bases or schemas
referenced by local sources and print their own warnings, potentially
including supplied values; no network or diagnostic sandbox is
promised.

## odh-platform-tools module mock

Runs the mock controller in the foreground until the command is cancelled.
It uses the inherited Kubernetes connection flags and watches a
discoverable, cluster-scoped custom resource. Its CRD supplies the
singleton constraint and a compatible platform status schema.

```
odh-platform-tools module mock \
  --api-version <group/version> --kind <Kind> --platform-configmap <name>
```

| Flag | Description |
|------|-------------|
| `--api-version` | Custom resource group and version (required) |
| `--kind` | Custom resource kind (required) |
| `--platform-configmap` | Name of the ConfigMap providing `data.platformVersion` (required) |
| `--namespace` | Optional inherited override for the ConfigMap namespace |

The ConfigMap namespace defaults to the Pod namespace in-cluster.
The example Deployments pass it as `POD_NAMESPACE` using the Downward
API. The in-cluster ServiceAccount namespace file is the fallback.
Outside the cluster, the inherited kubeconfig/context settings supply
the namespace, with `default` as the fallback.

Run one replica. Process health and readiness probes listen on port
8081. Status controls and the example startup procedure are documented
in the [mock walkthrough](mock.md).

## odh-platform-tools module validate

Loads one definition and checks the running module it describes against
the platform contract. Read-only: it never renders, applies, or mutates
anything — a matching report is evidence about what the cluster serves,
not a change to it. Waiting is automatic under one overall `--timeout`
budget; there is no wait toggle.

```
odh-platform-tools module validate --config <definition.yaml> [--timeout 30s] [--output text|json]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | — | Module definition file (required) |
| `--timeout` | `30s` | Overall validation budget — connection, discovery, and waiting included; must be positive; an earlier caller deadline shortens it |
| `--output` | `text` | Report format: `text` or `json` |

Standard kubeconfig/context/namespace/auth flags are inherited from the
root command (kubectl semantics: explicit flag, else context namespace,
else `default`). The namespace is resolved inside the timed action, so
it counts against the budget.

### Required access

Read-only access is enough: API discovery for the module's
group/version, plus `get` on the module custom resource, the controller
Deployment, and the platform ConfigMap in the resolved namespace. No
CRD object is read and nothing is written.

### Checks

Every invocation reports the same seven checks, each `pass`, `fail`
(an observed defect or a not-yet-healthy state), `skipped` (a required
input is unavailable, for example the custom resource is missing so its
status cannot be judged), or `error` (the read itself failed).

| ID | Requires |
|----|----------|
| `MODULE_API` | The module group/version is discoverable, serves the module kind cluster-scoped, and advertises a `/status` subresource |
| `MODULE_EXISTS` | The module custom resource exists and is not terminating |
| `MODULE_CONTRACT` | The observed status is structurally valid: phase, observedGeneration, conditions, and releases have the contract's types and shapes |
| `MODULE_READY` | Current `status.observedGeneration`, `Ready=True`, `ProvisioningSucceeded=True`, and `status.phase == Ready` |
| `DEPLOYMENT` | The controller Deployment exists, is not terminating or scaled to zero, has a current generation, all replicas updated/ready/available, none unavailable, and no `ProgressDeadlineExceeded` |
| `PLATFORM_CONFIG` | The `odh-<definition-name>-config` ConfigMap exists with a valid semantic version at `data.platformVersion` |
| `PLATFORM_CONSUMED` | The module reports a `platform` release whose normalized version equals the ConfigMap's normalized `data.platformVersion` |

A matching platform release is read-only consistency evidence. It does
not prove that the module read this particular ConfigMap, watches its
updates, or consumes every key.

### Waiting and the deadline

The first observation cycle runs immediately; success requires every
check to pass in the same cycle. Ordinary findings — a missing API,
custom resource, Deployment, or ConfigMap, malformed or stale status,
an unready or scaled-zero Deployment, a mismatched version — are retried
each cycle until the budget expires, and the report always contains the
latest cycle, not a finding history.

The overall `--timeout` bounds every context-aware cluster operation:
connection, discovery, reads, and polling. Local setup — kubeconfig
loading, file reads, and exec-based credential plugins — is not
hard-interruptible and may block past the deadline; this standard
Kubernetes CLI limitation is documented, not worked around. The
inherited `--request-timeout` bounds each single server request
independently and never extends the invocation budget.

A missing group (discovery 404) or a missing object (GET NotFound) is a
finding and keeps waiting. Any other read error — forbidden, connection
failure — is a runtime execution failure: the invocation returns
promptly with the partial result instead of waiting out the budget.

### Report and exit codes

The JSON report has `definition`, `namespace`, overall `status`
(`pass` | `failed`), `checks` (always an array), and on an execution
failure a classified `failure {category, message}`. Check entries carry
`id`, `status`, object identity fields (`group`, `version`, `kind`,
`namespace`, `name`), and where applicable `field` and `message`; empty
identity fields are omitted. The text report prints every check,
including passes, skips, and the checks retained by an execution
failure, followed by the failure line. Execution failures also print
one summary line to stderr; observed findings stay in the report.

Exit 0 only when every check passes. Exit 1 for findings, runtime,
timeout, or cancellation failures (categories `runtime`, `timeout`,
`cancelled`; timeout and cancellation identities take precedence). Exit
2 for usage, invalid options, or definition input errors (categories
`invalid-options`, `definition`). An expired invocation still prints
its latest checks with the timeout failure. If writing the report
itself fails, the invocation exits 1 preserving the action's cause.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Runtime, connection, apply, wait, timeout, cancellation failures; lint error-severity findings; generation render, validation, reserved-resource, or output failures; validate findings or report failures |
| 2 | Usage, invalid options, definition, or image-file errors |

Cancellation (SIGINT/SIGTERM) is classified as exit 1.
