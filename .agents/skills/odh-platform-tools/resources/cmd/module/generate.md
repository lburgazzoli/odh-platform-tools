# Generate a module definition

Render a local Helm chart or Kustomize tree offline and infer a minimal
`PlatformModuleDefinition` from the rendered objects. Generation is the
first step of the module workflow: generate, [lint](lint.md), deploy,
validate.

Shared setup — the pinned `go run` invocation, `ODH_TOOLS_REF`, Go and
network requirements, and `go run` exit-code wrapper semantics — is
defined in [the module command overview](../module.md). Read it first;
every command below assumes it.

## What generation does

1. Renders the source at `SOURCE_DIR` offline (no cluster access; the
   inherited kubeconfig/context flags are accepted and ignored).
2. Infers the module identity and controller selection from the
   rendered objects (strict discovery, below).
3. Validates the inferred definition with the same checks as
   `module lint`, plus a reserved-resource guard, **before writing
   anything**.
4. Writes the definition to stdout (default) or to a file created once
   with mode `0600`.

"Offline" means cluster-free: the native Helm and Kustomize libraries
may still load external bases or schemas referenced by local sources,
print their own warnings, and make network requests. No network or
diagnostic sandbox is promised.

## Inputs

| Input | Required | Notes |
|-------|----------|-------|
| `SOURCE_DIR` | yes | Local Helm chart directory or Kustomize tree |
| `--output` | no | `stdout` (default) or a file path; see [Output](#output) |
| `--name` | no | Module name; defaults to the lowercase CRD kind |
| `--instance-name` | no | Module instance name; defaults to the module name |
| `--init-container` | yes when init containers exist | Must name one rendered init container exactly |
| `--values-file` | no | Helm only; one nonempty YAML mapping, loaded first |
| `--values` | no, repeatable | Helm only; ordered set expressions, applied after the file |
| `--related-images-file` | no | YAML file of related image sources; only names are copied |

Omit every option that does not apply — empty values are rejected for
`--name`, `--instance-name`, `--init-container`, and `--output`, and
`--values-file` must be nonempty. Do not pass Helm values options at
all for a Kustomize source; they are rejected before any render.

## Discovery requirements

Discovery is strict. Ambiguous sources fail instead of guessing, so a
chart is expected to be a single-controller module source:

- The source must render **exactly one** `CustomResourceDefinition`
  (as `apiextensions.k8s.io/v1`), and that CRD must declare **exactly
  one served storage version** — it supplies the module group, version,
  and kind. Zero or multiple CRDs, or a CRD with zero or multiple
  storage versions (or an unserved storage version), fail.
- The source must render **exactly one** `Deployment`. Charts that
  render several controllers are not supported; split them or render
  only the controller package.
- The controller main container is the unique container named
  `manager` when several regular containers exist, or the only regular
  container. Two regular containers without a `manager` fail.
- Whenever the Deployment has init containers, `--init-container`
  must name one of them exactly; naming a container that does not
  exist, or passing one when no init containers exist, fails.
- Selected containers must have a nonempty image.

### Names and when to override them

- The module name defaults to the **lowercase CRD kind** — plain
  lowercasing, no pluralization or kebab-casing. Pass `--name` when:
  - the lowercased kind is longer than 53 characters or otherwise not
    usable as a Helm release name (Helm sources render under the
    module name as release name),
  - bootstrap discovery fails because the chart's rendering depends on
    `.Release.Name` (the bootstrap pass uses a neutral release name;
    its failures mention `--name`).
- The instance name recorded in `spec.instance.name` defaults to the
  module name. Pass `--instance-name` when the CRD mandates a specific
  singleton name — for example a controller that only reconciles one
  fixed custom resource name. It must be a valid DNS subdomain. When
  it equals the module name it is omitted from the generated file and
  re-derived on load.

## Values (Helm sources only)

`--values-file` loads first, then each `--values` flag in order; later
entries override earlier ones. Expressions use Helm `set` syntax and
reach the parser verbatim — preserve shell quoting so commas and
escapes survive:

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd@"$ODH_TOOLS_REF" module generate ./chart \
  --values-file values-prod.yaml \
  --values replicas=3 \
  --values 'gateway.host=example.com'
```

The winning values persist as plaintext, typed literals in
`spec.manifests.helm.values` of the generated file: booleans, numbers,
nested mappings, lists, and explicit nulls keep their shapes. Only the
overrides you pass are embedded — never chart defaults. Do not put
secrets in values: they are stored in plaintext in the generated file
and may appear in native renderer warnings.

A values file that is not a mapping, or a flag whose type conflicts
with a file entry's type, fails with exit 2. The file's contents never
appear in the diagnostic. Values options on a Kustomize source fail
with exit 2 before any render — omit them entirely.

## Related images

`--related-images-file` reads the same YAML format as
`module deploy`:

```yaml
relatedImages:
- name: RELATED_IMAGE_EXAMPLE_CONTROLLER
  value: quay.io/example/example-controller:v1.0.0
```

Generation copies only the entry **names** — sorted and
deduplicated — into `spec.relatedImages`. Values and the file path are
never stored in the generated definition. At deployment the names are
resolved again, from the process environment or from a
`--related-images-file` passed to `module deploy`; pass the current
values file again at that point.

Main and init container images are copied into the definition
**unchanged** — they are never rewritten to `{{VARIABLE}} related-image
tokens. Configure the real controller image in the chart source or
Helm values **before** generating. To use a related-image token
instead, edit the generated file afterwards.

When the current inputs change — a different set of related-image
names, new values, a changed chart — generate again to a **new**
output path. There is no merge, force, or sync mode, and stale output
must never be reused.

## Output

- `--output stdout` (the default) writes the document to stdout and
  nothing else; file successes write no stdout. Relative manifest
  paths in the document are resolved against the definition file's
  directory, so save stdout in the working directory of the
  invocation — or adjust `path` afterwards — before linting or
  deploying.
- A file path must have an **existing parent directory** — publication
  never creates directories. The file is created once with mode `0600`
  (subject to umask) and **never overwritten**: an existing file, or a
  path whose symlink target exists, fails with exit 1 and leaves the
  original bytes untouched.

### Chart safety (Helm sources)

For Helm sources, a destination inside the chart must be excluded by
the chart's existing `.helmignore`; this protects charts whose
templates consume chart files (for example through `.Files`), which
would otherwise render their own definition. The command never edits
ignore files. Kustomize sources have no ignore policy and allow
colocated output.

Shell redirection (`> chart/gen.yaml`) bypasses this guard and is
discouraged: it can silently place a definition inside the chart that
the next render consumes.

## Commands

Helm source — output next to, never inside, the chart:

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd@"$ODH_TOOLS_REF" module generate ./chart \
  --output ./generated/module.yaml
```

Kustomize source — colocated output is allowed:

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd@"$ODH_TOOLS_REF" module generate ./kustomize \
  --output ./kustomize/generated.yaml
```

The tools repository bundles example sources (`examples/helm/chart`,
`examples/kustomize`, and `examples/related-images.yaml`); those paths
are examples specific to a tools repository checkout — an external
consumer uses its own chart or tree.

### In CI: generate fresh on every run

Every workflow run — including every PR and every manual
invocation — must generate a fresh definition:

```sh
OUT_DIR="$(mktemp -d)"   # unique, existing parent, outside the chart
go run github.com/lburgazzoli/odh-platform-tools/cmd@"$ODH_TOOLS_REF" module generate ./chart \
  --output "$OUT_DIR/module.yaml"
go run github.com/lburgazzoli/odh-platform-tools/cmd@"$ODH_TOOLS_REF" module lint \
  --config "$OUT_DIR/module.yaml"
```

Never cache, reuse, or commit a generated definition from a previous
run, and never mask a failed generation or lint step in a pipeline:
each step must fail the job on a nonzero exit.

## Success and failure

- **Success**: exit 0, no stderr. The file exists (mode `0600`) or the
  document is on stdout.
- **Failure**: one classified diagnostic on stderr; nothing is
  published.

Failure categories (binary exit codes; see
[the module command overview](../module.md) for the `go run` wrapper,
which turns a nonzero child exit into wrapper exit 1 with
`exit status N` on stderr — preserve the failure, never assert
`$? == 2` in automation):

| Category | Binary exit | Meaning |
|----------|-------------|---------|
| `invalid-options` | 2 | Bad flags: values options on Kustomize, destination inside a chart, unusable names |
| `definition` | 2 | Source violates discovery rules, or a malformed values file / `.helmignore` |
| `image-file` | 2 | Unreadable or invalid related-images file |
| `render` | 1 | The bootstrap or final render failed |
| `reserved` | 1 | The rendered set contains a reserved resource |
| `validation` | 1 | The generated definition failed the lint checks |
| `output` | 1 | Publication failed (existing file, unwritable directory) |
| `cancelled` | 1 | Cancellation or deadline |

Native renderer failures are reported by stage and source path only —
their raw text is withheld because it may quote chart or values
content; rerun with `--debug` for the wrapped cause chain.

## Common failures and recovery

| Symptom | Cause | Recovery |
|---------|-------|----------|
| `module source must render exactly one CustomResourceDefinition/Deployment, found N` | The chart renders several CRDs or controllers | Render only the controller package; multi-controller charts are not supported |
| `must declare exactly one storage version` | Dual-storage or storage-less CRD | Fix the CRD in the source |
| `... without a unique container named "manager"` | Several regular containers, none uniquely `manager` | Fix the chart, or name the controller container `manager` |
| `an explicit init container selection is required` | Deployment has init containers | Pass `--init-container <name>` |
| Bootstrap failure mentioning `--name` | Discovery depends on the release name | Pass an explicit `--name` |
| `module name ... is not usable as a Helm release name` | Lowercased CRD kind exceeds 53 chars or is not a release name | Pass an explicit `--name` |
| `output ... may be consumed by the chart` | Destination inside the chart, not excluded | Move the output outside the chart or exclude it in `.helmignore` |
| `install generated definition ...: file exists` | Output file already exists | Generate to a new unique path; never delete or overwrite by hand in CI |
| `Helm values options require a Helm chart source` | Values options on Kustomize | Remove `--values`/`--values-file` |
| `generated definition failed validation: ... RESOURCE_KIND_DISALLOWED ...` | Rendered set contains a kind outside the module allowlist | Remove or replace the disallowed object in the source |

To isolate a source problem, render the chart or tree with its native
tool (`helm template` / `kubectl kustomize`) and check the rendered
objects against the discovery rules above.

## Next steps

- [Lint the generated definition](lint.md) — offline, no cluster
  credentials required.
- Edit the generated file freely (extra env, an `{{VARIABLE}}` image
  token, a different instance name); regeneration never merges into
  an existing file.
- For a CI workflow that runs these steps, see the
  [module TCK workflow template](../../templates/module-tck.yaml).
