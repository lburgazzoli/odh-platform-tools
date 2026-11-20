# Lint a module definition offline

Load one `PlatformModuleDefinition`, render its manifest backend
(Helm chart or Kustomize tree) offline, validate the rendered objects,
and write a report to stdout. No cluster access and no cluster
credentials are required.

Lint is static evidence about the definition and its rendered
manifests. It does **not** validate a live cluster, does not prove the
module is deployed or ready, and does not resolve image values — for
that, use `module validate` after deployment.

Shared setup — the pinned `go run` invocation, `ODH_TOOLS_REF`, Go and
network requirements, and `go run` exit-code wrapper semantics — is
defined in [the module command overview](../module.md). Read it first;
the command below assumes it.

## Usage

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd@"$ODH_TOOLS_REF" module lint \
  --config <definition.yaml> [--output text|json]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | — | Module definition file (required) |
| `--output` | `text` | Report format: `text` or `json` |

## Report and exit codes

Text output prints one status line, then one line per diagnostic —
`severity ID <object path>: message` — where the object path is
`group/version, Kind=Kind/namespace/name[/field]`:

```
module lint generated/module.yaml: pass
```

```
module lint generated/module.yaml: findings
error CONTROLLER_DEPLOYMENT_MISMATCH apps/v1, Kind=Deployment/mock-controller: controller must be apps/v1 Deployment "no-such-deployment"
```

JSON output has `definition`, `status`, `hasErrors`, `hasWarnings`,
`diagnostics` (each with `id`, `severity`, object identity, `message`),
and on an execution failure a classified `failure {category, message}`:

```json
{"definition":"generated/module.yaml","status":"findings","hasErrors":true,"hasWarnings":false,"diagnostics":[{"id":"CONTROLLER_DEPLOYMENT_MISMATCH","severity":"error","group":"apps","version":"v1","kind":"Deployment","name":"mock-controller","message":"controller must be apps/v1 Deployment \"no-such-deployment\""}]}
```

Statuses and exit codes (binary; with `go run`, a nonzero child exit
becomes wrapper exit 1 with `exit status N` on stderr — preserve the
failure in pipelines, never mask it, and never assert `$? == 2`):

| Status | Binary exit | Meaning |
|--------|-------------|---------|
| `pass` | 0 | Every check passed |
| `findings` | 1 when any error-severity finding | Diagnostics exist; error-severity findings fail without an execution error |
| `failed` | 1 or 2 | Execution failure: `render`/`cancelled` exit 1; `invalid-options`/`definition` exit 2 |

Execution failures also print one summary line to stderr; observed
findings stay in the report. Note that findings (definition defects)
and execution failures (the command could not run) are distinct: a
`findings` report means the lint ran and the definition has defects.

## Checks

| ID | Severity | Reports when |
|----|----------|--------------|
| `DEPLOYMENT_MISSING` / `DEPLOYMENT_MULTIPLE` | error | The rendered bundle does not contain exactly one Deployment |
| `CONTROLLER_DEPLOYMENT_MISMATCH` | error | The bundle's single Deployment does not match the definition's controller selection (name defaults to `metadata.name`) |
| `CONTAINER_MISSING` | error | A container the definition selects — the main container, or the configured init container — is not present in the Deployment's pod spec |
| `MODULE_CRD_MISSING` / `MODULE_CRD_DUPLICATE` | error | The bundle does not contain exactly one CustomResourceDefinition declaring the module's own group and kind |
| `RESOURCE_KIND_DISALLOWED` | error | A bundled object's kind is not on the module manifest allowlist |

The resource allowlist mirrors the operator's: Deployment, Service,
ServiceAccount, Role, RoleBinding, ClusterRole, ClusterRoleBinding,
CustomResourceDefinition, MutatingWebhookConfiguration,
ValidatingWebhookConfiguration, ValidatingAdmissionPolicy,
ValidatingAdmissionPolicyBinding, cert-manager Issuer and Certificate,
and NetworkPolicy. An allowed kind is allowed for every module.

During rendering — before the checks above — a reserved-resource guard
rejects rendered objects that are instances of the module's own custom
resource type, `PlatformModuleDefinition` objects, or the definition
CRD itself; such a collision is a render execution failure, not a
diagnostic.

"Offline" means cluster-free, not network-free: the native Helm and
Kustomize libraries may load external bases or schemas referenced by
the local source, print warnings, and make network requests while
rendering.

## Commands

```sh
# after generating (see generate.md)
go run github.com/lburgazzoli/odh-platform-tools/cmd@"$ODH_TOOLS_REF" module lint \
  --config "$OUT_DIR/module.yaml"

# machine-readable report for CI
go run github.com/lburgazzoli/odh-platform-tools/cmd@"$ODH_TOOLS_REF" module lint \
  --config "$OUT_DIR/module.yaml" --output json
```

Lint resolves the definition's manifest paths relative to the
definition file's own directory, so it works from any working
directory. A failed lint must fail the CI job; never pipe the report
through a filter that swallows the exit code.

## Common failures and recovery

| Symptom | Cause | Recovery |
|---------|-------|----------|
| `DEPLOYMENT_MISSING`/`DEPLOYMENT_MULTIPLE` | Source renders zero or several controllers | Fix the source; only one controller Deployment is supported |
| `CONTROLLER_DEPLOYMENT_MISMATCH` | Definition selects a Deployment name that differs from the rendered one | Edit the definition or regenerate |
| `CONTAINER_MISSING` | Selected main/init container is absent from the pod spec | Edit the definition's container selection or regenerate |
| `MODULE_CRD_MISSING`/`MODULE_CRD_DUPLICATE` | The module's own CRD is absent or duplicated in the bundle | Fix the source's CRD set |
| `RESOURCE_KIND_DISALLOWED` | A bundled object's kind is outside the allowlist | Remove or replace the object; do not widen the allowlist by hand |
| `definition: invalid module definition: read definition file ...` | Missing or unreadable definition file | Check the `--config` path (exit 2) |
| `render` failure | The backend could not render, or a reserved resource was rendered | Rerun with `--debug` for the wrapped cause; fix the chart/tree, then [regenerate](generate.md) to a new output path |

When lint reports findings against a generated definition, repair the
underlying source (chart or Kustomize tree) and
[generate again](generate.md) to a **new** output path — there is no
merge or sync, and stale output must not be reused. Findings caused by
hand edits to the definition can be fixed by editing the file again.

## Next steps

- [Deploy the module](deploy.md) — requires cluster access, the
  platform version, and current related-image values.
- For the offline CI workflow that pairs generation with lint, see the
  [module TCK workflow template](../../templates/module-tck.yaml).
