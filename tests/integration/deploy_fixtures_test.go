//go:build integration

// Run chain fixtures: definitions and manifest trees exercising the
// complete offline run composition — image sources, controller
// overrides, authored values, namespace placement, same-named maps,
// and gate-labeled maps — shared by the Helm and Kustomize chains.
package integration

import "testing"

// runKubeconfigYAML is a standalone kubeconfig whose context carries
// the effective installation namespace, pointing at an unreachable
// server: namespace resolution is local and the composed chain never
// contacts this address, because the cluster connection is the one
// simulated boundary.
const runKubeconfigYAML = `apiVersion: v1
kind: Config
clusters:
- name: run-cluster
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: run-context
  context:
    cluster: run-cluster
    user: run-user
    namespace: run-namespace
current-context: run-context
users:
- name: run-user
  user: {}
`

// runHelmDefinitionYAML is the Helm chain's definition: one declared
// related image, a literal controller image shared by the main and
// init containers, one explicit extra env var, and authored chart
// values reaching a template.
const runHelmDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    type:
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
  relatedImages:
  - RELATED_IMAGE_ALPHA
  manifests:
    format: helm
    helm:
      path: ./manifests/chart
      values:
        message: authored-value
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
        env:
        - name: LOG_LEVEL
          value: debug
      initContainer:
        name: installer
`

// runHelmSourceImageDefinitionYAML differs only in the controller
// image: a whole-token related-image reference resolved from the
// selected file or the environment.
const runHelmSourceImageDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    type:
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
  relatedImages:
  - RELATED_IMAGE_ALPHA
  manifests:
    format: helm
    helm:
      path: ./manifests/chart
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: '{{RELATED_IMAGE_ALPHA}}'
`

// runHelmFallbackDefinitionYAML omits the controller Deployment name:
// the env injector must fall back to the module's metadata.name,
// matching the operator's deploymentNameFor release-name fallback.
const runHelmFallbackDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    type:
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
  manifests:
    format: helm
    helm:
      path: ./manifests/chart
  controller:
    deployment:
      container:
        image: quay.io/example/controller:v1
`

// runHelmMalformedEnvDefinitionYAML targets a chart whose controller
// container carries a malformed env field: the typed conversion of
// the targeted Deployment fails the render instead of repairing it.
const runHelmMalformedEnvDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    type:
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
  manifests:
    format: helm
    helm:
      path: ./manifests/chart
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

// runKustomizeDefinitionYAML is the Kustomize chain's definition: the
// same controller contract over the shared Kustomize engine, whose
// namespace plugin places unset namespaces into the applications
// namespace.
const runKustomizeDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    type:
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
  relatedImages:
  - RELATED_IMAGE_ALPHA
  manifests:
    format: kustomize
    kustomize:
      path: ./manifests
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
        env:
        - name: LOG_LEVEL
          value: debug
      initContainer:
        name: installer
`

// runImagesEnv is the dotenv related-images file selecting the
// declared source key.
const runImagesEnv = `RELATED_IMAGE_ALPHA=quay.io/example/alpha:v3
`

// runChartYAML declares the vendored dependency chart.
const runChartYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- name: vendored
  version: 1.2.3
  repository: https://charts.example.com
`

// runChartValuesYAML carries chart defaults the authored definition
// values merge over.
const runChartValuesYAML = `replicas: 1
message: chart-default
`

// runDeploymentTemplateYAML is the controller Deployment: an init
// container sharing the literal override, the default main container
// name, and a chart-default image the injector replaces. Placement
// comes from the release namespace the template requests — the
// renderer never fills metadata.namespace itself.
const runDeploymentTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  namespace: {{ .Release.Namespace }}
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels:
      app: synthetic
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      serviceAccountName: synthetic-sa
      initContainers:
      - name: installer
        image: quay.io/chart/synthetic-installer:v2
      containers:
      - name: manager
        image: quay.io/chart/synthetic-controller:v9
`

// runMalformedEnvDeploymentTemplateYAML renders the controller
// Deployment with a malformed env field on the main container, which
// fails the render stage before any deploy.
const runMalformedEnvDeploymentTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  namespace: {{ .Release.Namespace }}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: synthetic
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      containers:
      - name: manager
        image: quay.io/chart/synthetic-controller:v9
        env: not-a-list
`

// runFallbackDeploymentTemplateYAML names the Deployment after the
// module's metadata.name, the fallback target.
const runFallbackDeploymentTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic
  namespace: {{ .Release.Namespace }}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: synthetic
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      containers:
      - name: manager
        image: quay.io/chart/synthetic-controller:v9
`

// runUnrelatedDeploymentTemplateYAML is a second, non-controller
// Deployment: the env injector skips it and a requested wait never
// observes it.
const runUnrelatedDeploymentTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: unrelated-controller
  namespace: {{ .Release.Namespace }}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: unrelated
  template:
    metadata:
      labels:
        app: unrelated
    spec:
      containers:
      - name: manager
        image: quay.io/chart/unrelated:v1
`

// runValuesConfigMapTemplateYAML renders the authored chart value
// into ConfigMap data, proving authored values survive composition.
const runValuesConfigMapTemplateYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: values-cm
  namespace: {{ .Release.Namespace }}
data:
  message: {{ .Values.message | quote }}
`

// runAlphaConfigMapTemplateYAML and runBetaConfigMapTemplateYAML are
// same-named ConfigMaps in distinct namespaces: the engine's
// apply-order post-renderer must keep the pre-migration namespace
// ordering all the way to the deploy boundary.
const (
	runAlphaConfigMapTemplateYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
  namespace: alpha
`

	runBetaConfigMapTemplateYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
  namespace: beta
`
)

// runGateConfigMapTemplateYAML carries the operator's upgrade-gate
// label (pkg/controller/gates/gates.go, commit 5a1d17f57b32): no
// gates subsystem exists here, so the map stays an ordinary rendered
// resource.
const runGateConfigMapTemplateYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: gate-cm
  namespace: {{ .Release.Namespace }}
  labels:
    app.opendatahub.io/upgrade-gate: "true"
data:
  gate: paused
`

// The Helm controller package's identity and RBAC: templated for the
// release namespace, because the renderer never fills
// metadata.namespace itself.
const (
	runServiceAccountTemplateYAML = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: synthetic-sa
  namespace: {{ .Release.Namespace }}
`

	runRoleTemplateYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: synthetic-role
  namespace: {{ .Release.Namespace }}
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list", "watch"]
`

	runRoleBindingTemplateYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: synthetic-binding
  namespace: {{ .Release.Namespace }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: synthetic-role
subjects:
- kind: ServiceAccount
  name: synthetic-sa
`
)

// The Kustomize chain's identity and RBAC: plain resources, placed by
// the engine's namespace plugin.
const (
	runKustomizeServiceAccountYAML = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: synthetic-sa
`

	runKustomizeRoleYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: synthetic-role
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list", "watch"]
`

	runKustomizeRoleBindingYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: synthetic-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: synthetic-role
subjects:
- kind: ServiceAccount
  name: synthetic-sa
`
)

// runKustomizationYAML is the Kustomize entry: a sibling base plus
// direct resources.
const runKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- ./base
- crd.yaml
- namespace.yaml
- serviceaccount.yaml
- role.yaml
- rolebinding.yaml
- values.yaml
- gate.yaml
`

// runBaseKustomizationYAML pulls the controller Deployment from the
// sibling base.
const runBaseKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
`

// runKustomizeDeploymentYAML mirrors the Helm controller Deployment
// without explicit placement: the namespace plugin sets it.
const runKustomizeDeploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  replicas: 1
  selector:
    matchLabels:
      app: synthetic
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      serviceAccountName: synthetic-sa
      initContainers:
      - name: installer
        image: quay.io/chart/synthetic-installer:v2
      containers:
      - name: manager
        image: quay.io/chart/synthetic-controller:v9
`

// runKustomizeValuesConfigMapYAML is the Kustomize chain's plain
// ConfigMap resource, placed by the namespace plugin.
const runKustomizeValuesConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: values-cm
data:
  message: authored-value
`

// runKustomizeGateConfigMapYAML is the Kustomize chain's gate-labeled
// ConfigMap resource.
const runKustomizeGateConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: gate-cm
  labels:
    app.opendatahub.io/upgrade-gate: "true"
data:
  gate: paused
`

// runKustomizeNamespaceYAML is an auxiliary Namespace resource: the
// framework's dynamic ownership excludes core/v1 Namespaces, so it
// must apply without controller references, exactly as upstream.
const runKustomizeNamespaceYAML = `apiVersion: v1
kind: Namespace
metadata:
  name: aux-namespace
`

// writeDeployHelmModule materializes the Helm deploy module tree: the full
// controller package plus the value, same-named, gate, and unrelated
// resources.
func writeDeployHelmModule(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", runHelmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", runChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", runChartValuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", runDeploymentTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/serviceaccount.yaml", runServiceAccountTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/role.yaml", runRoleTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/rolebinding.yaml", runRoleBindingTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/crd.yaml", moduleCRDYAML)
	writeModuleFile(t, root, "manifests/chart/templates/values-cm.yaml", runValuesConfigMapTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/cm-alpha.yaml", runAlphaConfigMapTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/cm-beta.yaml", runBetaConfigMapTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/gate-cm.yaml", runGateConfigMapTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/unrelated.yaml", runUnrelatedDeploymentTemplateYAML)

	archive := buildArchive(t, map[string]string{
		"vendored/Chart.yaml":         vendoredChartYAML,
		"vendored/templates/svc.yaml": vendoredServiceTemplateYAML,
	})
	writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))
}

// writeDeployHelmVariant materializes the Helm module tree with one
// definition and deployment template substituted, for the parity
// variants.
func writeDeployHelmVariant(
	t *testing.T,
	root string,
	definition string,
	deploymentTemplate string,
) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", definition)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", runChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", runChartValuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", deploymentTemplate)
	writeModuleFile(t, root, "manifests/chart/templates/serviceaccount.yaml", runServiceAccountTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/role.yaml", runRoleTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/rolebinding.yaml", runRoleBindingTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/crd.yaml", moduleCRDYAML)

	archive := buildArchive(t, map[string]string{
		"vendored/Chart.yaml":         vendoredChartYAML,
		"vendored/templates/svc.yaml": vendoredServiceTemplateYAML,
	})
	writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))
}

// writeDeployKustomizeModule materializes the Kustomize deploy module
// tree.
func writeDeployKustomizeModule(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", runKustomizeDefinitionYAML)
	writeModuleFile(t, root, "manifests/kustomization.yaml", runKustomizationYAML)
	writeModuleFile(t, root, "manifests/base/kustomization.yaml", runBaseKustomizationYAML)
	writeModuleFile(t, root, "manifests/base/deployment.yaml", runKustomizeDeploymentYAML)
	writeModuleFile(t, root, "manifests/crd.yaml", moduleCRDYAML)
	writeModuleFile(t, root, "manifests/serviceaccount.yaml", runKustomizeServiceAccountYAML)
	writeModuleFile(t, root, "manifests/role.yaml", runKustomizeRoleYAML)
	writeModuleFile(t, root, "manifests/rolebinding.yaml", runKustomizeRoleBindingYAML)
	writeModuleFile(t, root, "manifests/values.yaml", runKustomizeValuesConfigMapYAML)
	writeModuleFile(t, root, "manifests/gate.yaml", runKustomizeGateConfigMapYAML)
	writeModuleFile(t, root, "manifests/namespace.yaml", runKustomizeNamespaceYAML)
}
