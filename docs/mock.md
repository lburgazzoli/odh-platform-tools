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
  make image IMAGE_REGISTRY="$image_registry" IMAGE_NAME="$image_name" IMAGE_TAG="$tag" &&
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

- Each entry requires a unique Kubernetes condition `type` and a `status`
  of `True`, `False`, or `Unknown`. Optional fields are `reason`, `message`,
  `severity`, `observedGeneration`, and `lastTransitionTime`. Use a
  nonnegative integer for the generation and a nonzero RFC3339 timestamp.
  Unknown fields, null values, duplicate keys or types, and malformed
  documents invalidate the whole annotation.
- Entries override `MockReady=True` and `Degraded=False` or add custom
  conditions. An empty sequence or an absent annotation restores the
  defaults. A present blank annotation is invalid. Removing a custom entry removes that condition from status.
- The framework derives `Ready` from the current conditions. `Degraded`
  is healthy at `False`, and other types are healthy at `True`.
  `Info` conditions are excluded from readiness. `Ready` and
  `ProvisioningSucceeded` are reserved. `ProvisioningSucceeded` reports
  the actual reconciliation pipeline result.
- An empty or omitted reason becomes the type plus status, such as
  `MockReadyFalse`. An empty or omitted message uses the resolved reason.
- An omitted condition generation uses the CR's current
  `metadata.generation`. A supplied zero is honored and may serialize as
  absent. Automatic transition times stay fixed while a condition's
  final status stays the same. Supplied times are honored at the
  Kubernetes API's whole-second precision.
- Top-level `status.observedGeneration` uses the processed CR's
  generation on every pass. `status.phase` is `Ready` or `Not Ready`
  according to the derived `Ready` condition.

The named ConfigMap provides `data.platformVersion`. The deployer calls
it `odh-<definition-name>-config`. The controller watches the ConfigMap
and publishes the normalized semantic version in a `status.releases`
entry named `platform`, even when a valid condition override makes the
module unready.

An invalid annotation reports `MockReady=False` and `Degraded=True` with
reason `InvalidMockAnnotation`. With a valid annotation, a missing
ConfigMap or missing, blank, or invalid version reports the same
conditions with reason `InvalidPlatformConfiguration`. These input
failures drop stale custom conditions and keep the last valid persisted
release, if one exists. Correct the input to resume normal status
reporting. Actual API failures follow the framework's error handling.

The manager serves process probes on port 8081 at `/healthz` and
`/readyz` independently of the simulated conditions. The same command runs the
controller against a manually configured resource. [Command flags](cli.md#odh-platform-tools-module-mock)
