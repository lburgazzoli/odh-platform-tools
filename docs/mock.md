# Run a mock module

`odh-platform-tools module mock` runs a foreground controller in the existing
tools binary and image. It watches an installed cluster-scoped singleton
custom resource whose CRD follows the platform status contract. It updates
only that resource's status. Install the CRD and create the instance
separately. Run one replica with the `Recreate` Deployment strategy. The
controller has no leader election.

## Run both examples in Kind

From the repository root, use Podman and Kind to create a dedicated
cluster and load the native tools image. Run the commands in one shell
so the demo keeps the same image, cluster name, and kubeconfig.

```sh
cluster="odh-mock-$(date -u +%Y%m%dt%H%M%S)-$$"
context="kind-$cluster"
helm_namespace=odh-helm-demo
kustomize_namespace=odh-kustomize-demo
image_registry=quay.io/lburgazzoli
image_name=odh-platform-tools
tag="$cluster"
image="$image_registry/$image_name:$tag"
demo_dir=$(mktemp -d)
kubeconfig="$demo_dir/kubeconfig"

setup_mock_demo() {
  test -d "$demo_dir" &&
  make container-build IMAGE_REGISTRY="$image_registry" IMAGE_NAME="$image_name" IMAGE_TAG="$tag" &&
  podman save --format docker-archive --output "$demo_dir/tools.tar" "$image" &&
  KIND_EXPERIMENTAL_PROVIDER=podman kind create cluster --name "$cluster" --kubeconfig "$kubeconfig" &&
  touch "$demo_dir/cluster-created" &&
  KIND_EXPERIMENTAL_PROVIDER=podman kind load image-archive "$demo_dir/tools.tar" --name "$cluster" &&
  rm "$demo_dir/tools.tar" &&
  kubectl --kubeconfig "$kubeconfig" --context "$context" create namespace "$helm_namespace" &&
  kubectl --kubeconfig "$kubeconfig" --context "$context" create namespace "$kustomize_namespace"
}

setup_mock_demo
```

Install the Helm and Kustomize controller packages in separate namespaces.
Each package has a Deployment named `mock-controller`. The process environment
sets the image to the exact tag loaded into Kind. If you use the default
`dev` tag, you can instead pass `--related-images-file examples/related-images.yaml`
with `RELATED_IMAGE_MOCK_CONTROLLER` unset. See the [image source rules](cli.md#related-images-file).

```sh
RELATED_IMAGE_MOCK_CONTROLLER="$image" go run ./cmd module deploy \
  --config examples/helm/chart/module.yaml \
  --platform-version 2.19.0 --kubeconfig "$kubeconfig" --context "$context" \
  --namespace "$helm_namespace" --wait

RELATED_IMAGE_MOCK_CONTROLLER="$image" go run ./cmd module deploy \
  --config examples/kustomize/module.yaml \
  --platform-version 2.19.0 --kubeconfig "$kubeconfig" --context "$context" \
  --namespace "$kustomize_namespace" --wait
```

Deploy's `--wait` checks the Deployment rollout. Apply the instances
separately after the controllers start.

```sh
kubectl --kubeconfig "$kubeconfig" --context "$context" apply -f examples/helm/instance.yaml
kubectl --kubeconfig "$kubeconfig" --context "$context" apply -f examples/kustomize/instance.yaml
```

Validate both running modules read-only. Each invocation waits under its
overall `--timeout` until every check passes in one observation cycle and
exits 0 only then; the report names each check and, on findings, the
field that failed. The commands reuse the walkthrough's explicit
connection variables and require the instances to have been applied
separately, as above. [Validate flags and checks](cli.md#odh-platform-tools-module-validate)

```sh
go run ./cmd module validate \
  --config examples/helm/chart/module.yaml \
  --kubeconfig "$kubeconfig" --context "$context" \
  --namespace "$helm_namespace"

go run ./cmd module validate \
  --config examples/kustomize/module.yaml \
  --kubeconfig "$kubeconfig" --context "$context" \
  --namespace "$kustomize_namespace"
```

The controls below are visible to `module validate`: while the
`MockReady=False` annotation is set, a validation invocation holds its
readiness finding until the timeout instead of passing.

Run the same controls against both instances. Each controller watches the
platform ConfigMap in its own namespace.

```sh
observe_mock() {
  instance=$1
  namespace=$2
  configmap=$3
  annotation=mock.platform.opendatahub.io/conditions

  kubectl --kubeconfig "$kubeconfig" --context "$context" wait --for=condition=Ready "$instance" --timeout=30s &&
  kubectl --kubeconfig "$kubeconfig" --context "$context" annotate --overwrite "$instance" \
    "$annotation=[{\"type\":\"MockReady\",\"status\":\"False\"}]" &&
  kubectl --kubeconfig "$kubeconfig" --context "$context" wait --for=condition=Ready=False "$instance" --timeout=30s &&
  kubectl --kubeconfig "$kubeconfig" --context "$context" annotate "$instance" "$annotation-" &&
  kubectl --kubeconfig "$kubeconfig" --context "$context" wait --for=condition=Ready "$instance" --timeout=30s &&
  kubectl --kubeconfig "$kubeconfig" --context "$context" --namespace "$namespace" patch configmap "$configmap" \
    --type=merge --patch '{"data":{"platformVersion":"2.20.0"}}' &&
  kubectl --kubeconfig "$kubeconfig" --context "$context" wait \
    --for=jsonpath='{.status.releases[?(@.name=="platform")].version}'=2.20.0 \
    "$instance" --timeout=30s &&
  kubectl --kubeconfig "$kubeconfig" --context "$context" get "$instance" -o yaml
}

observe_mock helmmodule/default-helm-module "$helm_namespace" odh-helm-module-config
observe_mock kustomizemodule/default-kustomize-module "$kustomize_namespace" odh-kustomize-module-config
```

Both examples use the `components.platform.opendatahub.io/v1alpha1` group
and version. Their ServiceAccounts can read and watch the selected
module resources and the platform ConfigMap, write module status, and
record reconciliation errors as Kubernetes events. The module CRs are
cluster-scoped.

After trying both examples, remove the demo cluster and the temporary
kubeconfig.

```sh
cleanup_mock_demo() {
  test -d "$demo_dir" || return 0
  if test -f "$demo_dir/cluster-created"; then
    KIND_EXPERIMENTAL_PROVIDER=podman kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig" || return
  fi
  rm -f "$kubeconfig" "$demo_dir/tools.tar" "$demo_dir/cluster-created" &&
  rmdir "$demo_dir"
}

cleanup_mock_demo
```

## Conditions and platform version

Set `mock.platform.opendatahub.io/conditions` on the module CR to one YAML
or JSON sequence. Quote YAML status values. This sequence simulates a
readiness failure alongside an informational observation.

```yaml
metadata:
  annotations:
    mock.platform.opendatahub.io/conditions: |
      - type: MockReady
        status: "False"
        reason: SimulatedFailure
        message: Recover by clearing this annotation.
      - type: Observation
        status: "Unknown"
        severity: Info
```

- Each entry requires a Kubernetes condition `type` and a `status` of
  `True`, `False`, or `Unknown`. Optional fields are `reason`, `message`,
  and `severity`. The annotation must be exactly one YAML or JSON
  document. `Ready` and `ProvisioningSucceeded` are reserved;
  `ProvisioningSucceeded` reports the actual reconciliation pipeline
  result.
- Every other field is ignored regardless of its value. This is a
  migration from earlier versions: `observedGeneration`,
  `lastTransitionTime`, and `lastHeartbeatTime` are no longer honored as
  controls, and unknown fields of any shape are dropped rather than
  rejected. Generation and transition times remain in the emitted status
  as controller-owned outputs (see below); `lastHeartbeatTime` is a
  deprecated field the platform clears.
- Repeated condition types keep the last whole entry; earlier entries of
  the same type are not merged. Duplicate mapping keys, merge keys, and
  aliases follow ordinary YAML and JSON decoder semantics.
- Entries override `MockReady=True` and `Degraded=False` or add custom
  conditions. An empty sequence or an absent annotation restores the
  defaults. A present blank annotation is invalid. Removing a custom entry removes that condition from status.
- The framework derives `Ready` from the current conditions. `Degraded`
  is healthy at `False`, and other types are healthy at `True`.
  `Info` conditions are excluded from readiness.
- An omitted or empty reason becomes the type plus status, such as
  `MockReadyFalse`. An omitted or empty message uses the resolved reason.
- The controller owns all observation metadata. Every condition's
  `observedGeneration` is the processed CR's `metadata.generation`, and
  transition times are stamped at whole-second UTC precision. A
  condition keeps its `lastTransitionTime` while its final status stays
  the same, so reason, message, and dependency changes do not move it.
- Top-level `status.observedGeneration` uses the processed CR's
  generation on every pass. `status.phase` is `Ready` or `Not Ready`
  according to the derived `Ready` condition.

The named ConfigMap provides `data.platformVersion`. The deployer calls
it `odh-<definition-name>-config`. The controller watches the ConfigMap
and publishes the normalized semantic version in a `status.releases`
entry named `platform`, even when a valid condition override makes the
module unready.

Input errors fail the framework's provisioning instead of producing
special mock conditions. A malformed annotation — or a missing
ConfigMap, or missing, blank, or invalid `data.platformVersion` — makes
the controller return a classified error: the framework reports
`ProvisioningSucceeded=False` with that message, derives `Ready=False`
and phase `Not Ready`, records a warning `ProvisioningError` event, and
retries automatically. On any input failure the controller installs only
the healthy `MockReady=True` and `Degraded=False` defaults — these do not
imply overall readiness while `ProvisioningSucceeded` is `False` — drops
previously injected custom conditions, and keeps the last valid persisted
release, if one exists. A valid annotation is never partly applied when
the platform version cannot be resolved, and an invalid annotation is
reported before the ConfigMap is read. Correcting the input resumes
normal status reporting; a simulated unready status from a valid
annotation keeps `ProvisioningSucceeded=True` and is not an error. Actual
API failures follow the framework's error handling.

The manager serves process probes on port 8081 at `/healthz` and
`/readyz` independently of the simulated conditions. The same command runs the
controller against a manually configured resource. [Command flags](cli.md#odh-platform-tools-module-mock)
