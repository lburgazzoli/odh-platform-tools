# Module onboarding

Shared onboarding for external module authors: how the `odh-platform-tools`
CLI is invoked, what to check before running anything, the ordered path from
a local Helm chart or Kustomize tree to a validated running module, and how
to adapt the CI workflow template. The per-command recipes assume this page:

- [Generate a definition from a local source](module/generate.md)
- [Lint a definition offline](module/lint.md)
- [Deploy a controller package](module/deploy.md)
- [Validate a running module](module/validate.md)
- [Run the mock module controller](module/mock.md)

## How the pieces fit

A module is one `PlatformModuleDefinition`
(`config.opendatahub.io/v1alpha1`, cluster-scoped) plus a local manifest
source — a Helm chart or a Kustomize tree. The definition describes the
controller package: which backend renders it, which Deployment is the
controller, which related-image names it consumes, and the identity of the
module instance.

The `module` command group does not orchestrate the workflow end to end.
Each command is one separate step:

1. `module generate` infers a definition from the local source, offline.
2. `module lint` renders the definition and checks the package, offline.
3. `module deploy` installs the controller package on a cluster. It never
   creates the module instance.
4. The module instance — the module custom resource — is applied separately
   with `kubectl apply` after deploy.
5. `module validate` checks the running module against the platform
   contract, read-only, waiting automatically under one timeout.

`module mock` is off this path: it runs a status-only test controller for a
compatible module resource. It is a stand-in for developing and
demonstrating the contract, never the real module.

## Invocation

### Pinned go run

The tools run with `go run` from their module path — no installed binary
and no clone of the tools repository required. The reference must be chosen
explicitly:

```sh
ODH_TOOLS_REF=<release-tag-or-commit-sha>
go run "github.com/lburgazzoli/odh-platform-tools/cmd@${ODH_TOOLS_REF}" module --help
```

`ODH_TOOLS_REF` should be a released version tag or commit SHA that Go
accepts. While the tools have no releases and are iterating, `main` is the
working default — but a moving ref makes CI results and failure reports
irreproducible, so pin a tag or commit once releases exist. Never invent a
release that was not confirmed to exist. If the user has not chosen a ref,
ask.

A shell function keeps later examples short without uncontrolled word
splitting:

```sh
odh() {
  go run "github.com/lburgazzoli/odh-platform-tools/cmd@${ODH_TOOLS_REF:?ODH_TOOLS_REF must be set to a release, commit, or main while iterating}" "$@"
}
```

Every `odh …` command below means exactly this function. Do not wrap the
invocation in `eval` or an unquoted command string.

### Setup and network requirements

- A Go toolchain is required on every machine that runs the commands,
  including CI runners. The first `go run` of a pin downloads and compiles
  the module — network access and disk are needed, and that compile time is
  outside the CLI's own `--timeout` budgets.
- The tools repository currently requires Go 1.27.3. That is the
  requirement of this checkout, not of every pin: verify the selected pin
  instead of inferring the requirement from the consumer module's `go.mod`,
  which pins the consumer's own dependency graph, not the tools'.
- Do not infer a minimum tools version from a failure. If the pin lacks a
  command or flag a recipe needs, ask the user to choose a compatible pin;
  do not substitute `main`.

### Pin preflight

Run both checks before relying on any recipe. The first works from any
directory, module or not, and reports the module's Go requirement for the
pin (`GoVersion`):

```sh
go list -m -json "github.com/lburgazzoli/odh-platform-tools@${ODH_TOOLS_REF}"
```

The second compiles the pin and confirms the commands and flags the recipes
use exist in that version:

```sh
odh module --help
odh module generate --help
odh module lint --help
odh module deploy --help
odh module validate --help
```

### Exit codes and the go run wrapper

The binary's own contract: exit 0 success, 1 findings and runtime,
cancellation, or timeout failures, 2 usage, invalid options, and definition
or image-file errors.

Under `go run`, a nonzero child exit normally becomes the wrapper's exit 1
with `exit status N` on stderr — a usage error surfaces as exit 1 plus
`exit status 2`, not as `$? == 2`. Preserve the failure; never assert a
specific nonzero code in examples or scripts. In CI, failing the step on
any nonzero exit is the correct behavior.

### Debug output

`--debug` is a root flag inherited by every module command. On failure it
prints the wrapped error chain — every cause, including raw causes the
classified summaries withhold — to stderr, one indented line per cause. It
changes no output or exit code otherwise, so it is safe to add to any
failing invocation.

## Preflight: gather facts, then ask

Before running anything, inspect what the user actually has. Resolve facts
by inspection; ask only for choices.

### Facts to inspect

- The source: a Helm chart directory or a Kustomize tree, and whether it
  sits at the repository root or nested.
- Rendered objects: exactly one CRD with a single served storage version,
  and exactly one Deployment whose main container is the unique `manager`
  or the only regular container. Init containers need an explicit
  `--init-container` name.
- Chart values that select the controller image and the target namespace —
  the real controller image must be configured in the source before
  generation, because generation copies it unchanged.
- Any existing related-images file and instance manifest (often under a
  `samples` or `config` directory of the module operator).
- Prerequisite APIs the controller watches unconditionally — for example
  prometheus operator CRDs. A controller whose watched CRDs are absent runs
  but reconciles nothing.

### Choices to ask

Never assume permission to build or push images, or to use a cluster. Ask
for:

- The `ODH_TOOLS_REF` pin, if not already chosen.
- Module name (`--name`) when the lowercase CRD kind is not the platform
  module name, and instance name (`--instance-name`) when the CRD mandates
  a singleton name.
- Init container name when the Deployment has init containers.
- Image strategy — recommend reusing existing images. If the user chooses
  building images, inspect their actual module build contract and ask
  before adding any build or publication steps to a workflow.
- Platform version, target namespace, and the cluster target for live
  operations.

### Images: controller versus related

Two distinct image roles, easy to confuse:

- The controller image is the module operator's own image. It is set in the
  source — chart values or Kustomize manifests — before generation.
  Generation copies it into the definition unchanged; deployment never
  rewrites it into a token.
- Related images are operand images the controller deploys. The definition
  records only their names in `spec.relatedImages`; their values are
  resolved at deploy time, injected as `RELATED_IMAGE_*` environment
  variables on the controller Deployment.

Both generation and deploy accept `--related-images-file` with the same
YAML shape:

```yaml
relatedImages:
- name: RELATED_IMAGE_EXAMPLE_CONTROLLER
  value: quay.io/example/example-controller:v1.0.0
```

Generation copies only the entry names — sorted and deduplicated — into the
definition. Deploy resolves the names to values from that file and the
process environment. A matching environment variable takes precedence over
the file, and an explicitly empty variable masks its file value. Pass the
same related-images file at generation and again at deploy so the deployed
values match the names recorded in the definition.

## Onboarding path

### 1. Generate, into a fresh output path

Generation is create-once: the output file is created once with mode 0600
and never overwritten — a second run against an existing path fails with
exit 1 and leaves the original file untouched. Every run, including every
CI run, needs a fresh destination: a unique temporary directory with an
existing parent and a nonexistent output file, created outside the chart.

```sh
OUT="$(mktemp -d)"
odh module generate <source-dir> \
  --values image.fullRef=<controller-image> \
  --values namespace=<namespace> \
  --related-images-file <related-images.yaml> \
  --output "$OUT/module.yaml"
```

- Helm sources: `--values-file` loads first, then each `--values` flag in
  order; later entries override earlier ones, and the winners are persisted
  in `spec.manifests.helm.values`. Values options are rejected for
  Kustomize sources.
- Helm destinations inside the chart must be excluded by the chart's
  existing `.helmignore`; the command never edits ignore files. Shell
  redirection bypasses that guard — do not use it. Kustomize sources allow
  colocated output.
- Relative manifest paths in the generated document resolve against the
  definition file's own directory: generated beside the source, they point
  back at it.
- Never retain a committed definition as the CI input — always generate
  fresh, including for every PR and manual workflow run.

The generated file is an editable starting point; there is no merge, force,
or sync mode. See [generate](module/generate.md).

### 2. Lint offline

```sh
odh module lint --config "$OUT/module.yaml"
```

No cluster access. Status `pass` exits 0; error-severity findings exit 1.
See [lint](module/lint.md).

### 3. Live operations, only with explicit approval

Nothing below runs without the user's explicit approval of the cluster
target and the mutation. Do not create a cluster merely because this skill
is active.

```sh
odh module deploy \
  --config "$OUT/module.yaml" \
  --platform-version <semver> \
  --related-images-file <related-images.yaml> \
  --namespace <namespace> \
  --wait

kubectl apply --filename <instance-manifest.yaml>

odh module validate \
  --config "$OUT/module.yaml" \
  --namespace <namespace> \
  --timeout 120s
```

`deploy` publishes the definition owner and applies the controller package;
`--wait` waits for the controller Deployment rollout. The module instance
is applied separately — deploy never creates it. `validate` is read-only
and waits automatically under its overall `--timeout`. See
[deploy](module/deploy.md) and [validate](module/validate.md).

## Cluster targeting and approval

Deploy, mock, and validate inherit the standard kubeconfig, context,
namespace, and auth flags from the root command with kubectl semantics:
explicit flag, else the context's namespace, else `default`. Generation
accepts and ignores them.

Before any mutating command:

- Confirm the target explicitly — which kubeconfig file, which context,
  which namespace. Never rely on whatever happens to be the ambient
  default.
- `deploy` writes cluster resources. `mock` writes status. `validate` is
  read-only. Say which of these a step performs, and get approval for the
  writing ones.
- Clean up only resources created and owned by the specific walkthrough or
  run — nothing outside it.

## CI workflow

The one template to adapt is
[module-tck.yaml](../templates/module-tck.yaml). Copy it into the
consumer repository's `.github/workflows/` directory and edit; there is no
second template. Its assumptions: an Ubuntu-hosted Bash workflow, a
Podman-backed disposable Kind cluster for manual runs, and user-provided
module prerequisites.

### Layout

The workflow is a single job. The source is checked out into
`github.workspace/source/` — also when the chart is at the repository root —
and generation writes into a fixed `.generated/` sibling beside it, never
inside `source/`. The generated definition's manifest path is relative to
the generated file's directory, so it keeps resolving on the same runner
for every later step; nothing is passed between jobs or machines.

The live steps (Kind, prerequisites, namespace, deploy, instance, validate,
cleanup) are gated on `if: github.event_name == 'workflow_dispatch'`, which
a `pull_request` event can never satisfy — so PR runs execute generation
and lint only, without a cluster and without secrets. Every step that
invokes the tools or Kind defines its own shell function: functions,
installed tools, and `$GITHUB_ENV` entries never cross step boundaries.

### PR runs

PR runs — fork PRs included — execute generation and lint only: no cluster,
no registry-write secrets, no image publication. Checkout uses
`persist-credentials: false`. Never use `pull_request_target`.

### Manual runs

Manual runs repeat the same offline gate with fresh inputs, then use a
disposable Kind cluster: install prerequisite APIs and the target
namespace, deploy with `--wait`, apply the instance manifest separately,
validate read-only, and delete the run-owned cluster. The runner is
ephemeral — a failed run discards the whole machine, cluster included.

Before a credential-bearing manual run, require reviewed and trusted
source, images, and prerequisite manifests. Kind is not a security sandbox,
and passing lint is not a trust decision. Existing images must be pullable
by the Kind nodes; private registries need an explicitly chosen credential
approach — approved registry credentials stay live-job-only, pull-only,
narrowly scoped, and short-lived, and are never packaged into source or
generated artifacts. PRs may not depend on secrets.

## Common failures and recovery

Distinguish these before reacting; each links to the detailed recipe:

| Symptom | What it is | Where to go |
|---------|------------|-------------|
| `generate` fails with discovery diagnostics — missing or multiple CRDs, dual storage versions, no `manager` container, bootstrap failures mentioning `--name` | Discovery ambiguity: the source does not meet the strict single-CRD, single-Deployment contract, or needs an explicit `--name` | [generate](module/generate.md) |
| Deploy succeeds and the controller runs, but reconciles nothing | Missing prerequisite APIs: the controller watches CRDs that are not installed | [deploy](module/deploy.md) |
| Controller pod `ImagePullBackOff` or `ErrImagePull` | Image-pull problem: the image is not pullable by the nodes, or a private registry lacks credentials | [deploy](module/deploy.md) |
| `deploy --wait` passed, `validate` still reports `MODULE_READY` or `MODULE_CONTRACT` findings | Rollout versus module readiness: the Deployment is ready but the module custom resource's status lags; validate keeps waiting under its `--timeout` | [validate](module/validate.md) |
| `validate` aborts promptly with a runtime failure — forbidden, connection error | A permission or access error, not a validation finding; ordinary findings keep waiting until the budget expires | [validate](module/validate.md) |
| `lint` exits 1 with a findings report | Ordinary validation findings on the package; fix the definition or source and re-lint | [lint](module/lint.md) |

## Safety summary

- Pin the tools; preflight the pin's Go requirement and command help.
- Generate fresh every run; create-once output.
- Explicit cluster target and approval for every mutation; validate is
  read-only.
- Trust is a review decision, made before live runs — never a lint result.
