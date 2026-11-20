# Run a mock module controller (live)

`module mock` runs a status-only mock controller in the foreground
until the command is cancelled. It watches one discoverable,
cluster-scoped singleton custom resource and updates only that
resource's status: condition controls from an annotation plus the
platform release from a ConfigMap. Use it to exercise the platform
status contract — including [validate](validate.md) — without
implementing a real module controller.

It is a mock, not an installer or an operator:

- It does **not** install the CRD. A compatible, cluster-scoped CRD
  (single served storage version, singleton constraint, platform
  status schema) must already be installed on the target.
- It does **not** create the singleton instance; create it yourself.
- It does **not** manage the instance's spec, and it does not
  implement any real module's operand work.
- Only the status of the watched resource is written.

Shared setup, the pinned `go run` invocation, and connection-safety
rules are defined in [the module onboarding](../module.md). Running
the controller writes status to a cluster: get explicit approval and
a named target first.

## Prerequisites and inputs

- Explicit user approval to run a controller against the target
  cluster, and an explicitly selected target
  (`--kubeconfig`/`--context`).
- An installed, compatible, **cluster-scoped** CRD for the module
  resource, including a status subresource compatible with the
  platform status contract. If the module's controller package was
  deployed with [deploy](deploy.md), its CRD is already installed.
- A separately created singleton instance of that resource. The
  bundled `examples/helm/instance.yaml` and
  `examples/kustomize/instance.yaml` are examples specific to the
  tools repository checkout — use your own instance manifest.
- The module resource's API version and kind
  (`--api-version <group/version>`, `--kind <Kind>`). The flags must
  select a custom resource: built-in kinds and
  `CustomResourceDefinition` itself are rejected (exit 2).
- A named, valid platform ConfigMap (`--platform-configmap`)
  providing `data.platformVersion`. Deploy creates it as
  `odh-<definition-name>-config`; if you set up the resource
  manually, create the ConfigMap yourself with a valid semantic
  version.
- Namespace resolution for the ConfigMap: in-cluster the Pod
  namespace (the bundled example Deployments pass it as
  `POD_NAMESPACE` using the Downward API; the in-cluster
  ServiceAccount namespace file is the fallback); outside the
  cluster, the inherited kubeconfig/context settings with `default`
  as the final fallback. `--namespace` overrides.
- RBAC for the controller identity. The minimum, mirroring the
  bundled example packages: `get`/`list`/`watch` on the module
  resource (cluster-scoped) and on the platform ConfigMap in its
  namespace, `get`/`patch`/`update` on the module resource's
  `/status`, and `create`/`patch` on `events.k8s.io` events (recorded
  in the default namespace for cluster-scoped modules).

## Invocation

Run it in the foreground; it serves until cancelled with SIGINT or
SIGTERM (cancellation exits 1):

```sh
go run "github.com/lburgazzoli/odh-platform-tools/cmd@${ODH_TOOLS_REF:?pin ODH_TOOLS_REF}" \
  module mock \
  --api-version <group/version> \
  --kind <Kind> \
  --platform-configmap <odh-<name>-config> \
  --kubeconfig <kubeconfig> --context <context> [--namespace <namespace>]
```

Invalid flags — a malformed group/version, a kind that is not a
custom resource, an invalid ConfigMap name, or an invalid namespace —
fail with exit 2 before any cluster access. Through `go run`, a
nonzero CLI exit normally surfaces as wrapper exit 1 with
`exit status N` on stderr.

### Deployment-mode constraints

When you run the mock as a Deployment (the example packages do), keep
it to the shape the command supports:

- **Single replica.** The controller has no leader election; two
  replicas would both write status.
- **`Recreate` strategy**, not `RollingUpdate` — a rolling restart
  would briefly run two replicas against one singleton.
- Process health and readiness probes on **port 8081**
  (`/healthz`, `/readyz`). These report process health only; they are
  independent of the simulated module conditions, so probe readiness
  is never module readiness.

## Simulating readiness

Set the `mock.platform.opendatahub.io/conditions` annotation on the
module CR to one YAML or JSON sequence of conditions (quote YAML
status values):

```yaml
metadata:
  annotations:
    mock.platform.opendatahub.io/conditions: |
      - type: MockReady
        status: "False"
        reason: SimulatedFailure
        message: Recover by clearing this annotation.
```

- Each entry requires a condition `type` and a `status` of `True`,
  `False`, or `Unknown`; `reason`, `message`, and `severity` are
  optional. `Ready` and `ProvisioningSucceeded` are reserved —
  `ProvisioningSucceeded` reports the actual reconciliation result.
- The framework derives `Ready` from the current conditions:
  `Degraded` is healthy at `False`, other types at `True`; `Info`
  conditions are excluded from readiness. So the annotation above
  makes the module unready, and a `module validate` invocation holds
  its readiness finding until its timeout instead of passing.
- **Recover** by removing the annotation
  (`kubectl annotate <resource> mock.platform.opendatahub.io/conditions-`)
  or setting an empty/absent sequence, which restores the healthy
  `MockReady=True` and `Degraded=False` defaults.
- The controller owns all observation metadata: every condition's
  `observedGeneration` is the processed CR's `metadata.generation`,
  transition times are stamped at whole-second UTC precision, and a
  condition keeps its `lastTransitionTime` while its status is
  unchanged. `status.observedGeneration` tracks the CR generation on
  every pass and `status.phase` is `Ready` or `Not Ready`.
- Repeated condition types keep the last whole entry. Every other
  annotation field is ignored; a present but blank annotation is
  invalid.

### Platform release behavior

The controller watches the named ConfigMap and publishes its
normalized semantic version as a `status.releases` entry named
`platform`, even when a valid condition override makes the module
unready. Observe a version update with:

```sh
kubectl --kubeconfig <kubeconfig> --context <context> \
  --namespace <namespace> patch configmap <odh-<name>-config> \
  --type=merge --patch '{"data":{"platformVersion":"2.20.0"}}'

kubectl --kubeconfig <kubeconfig> --context <context> wait \
  --for=jsonpath='{.status.releases[?(@.name=="platform")].version}'=2.20.0 \
  <resource> --timeout=30s
```

### Malformed input

A malformed annotation — or a missing ConfigMap, or missing, blank,
or invalid `data.platformVersion` — fails the framework's
provisioning rather than producing special mock conditions: the
controller reports `ProvisioningSucceeded=False` with the classified
message, derives `Ready=False` and phase `Not Ready`, records a
warning `ProvisioningError` event, and retries automatically. On any
input failure it installs only the healthy defaults (which do not
imply overall readiness), drops previously injected custom
conditions, and keeps the last valid persisted release if one
exists. An invalid annotation is reported before the ConfigMap is
read. Correcting the input resumes normal status reporting.

## Observable results and recovery

- The command itself produces no success report — it runs until
  cancelled. Watch the resource's status with `kubectl get <resource>
  -o yaml` or validate with [validate](validate.md).
- A runtime failure (REST configuration, watch or status write
  errors) prints `module mock: <classified error>` to stderr and
  exits 1; add `--debug` for the full error chain.
- Cancellation (SIGINT/SIGTERM) exits 1 by design.
- **Run-owned cleanup limits:** stopping the controller does not
  remove the CRD, the instance, the ConfigMap, or any status already
  written, and the command performs no cleanup of its own. Remove
  only resources the specific walkthrough created, and only with the
  target explicitly selected.

## Next step

With a mock module reporting a healthy status, run
[validate](validate.md) against it to see the full ten-check report
pass, then use the annotation and ConfigMap controls above to watch
individual findings appear and recover.
