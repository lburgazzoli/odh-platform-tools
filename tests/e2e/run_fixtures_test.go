//go:build e2e

/*
Copyright 2026 Luca Burgazzoli

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

// Live scenario fixtures. Every cluster-scoped identity — the
// definition name, the payload CRD and its group, and the
// payload-supplied Namespace — carries the scenario's unique suffix,
// so no two runs can collide and any pre-existing object is a foreign
// object the suite fails closed on. The CONTROLLERIMAGE placeholder
// selects each scenario's controller image strategy: a literal
// pipeline image, a whole-token related-image reference, or an
// intentionally unpullable tag for nonconvergence.
//
// The pinned generic workload image is the registry.k8s.io pause
// image: a successful no-op init container and a long-running main
// container, with no custom controller build or publication.

const (
	// pauseImage is the recorded pause image pin supporting both a
	// successful init and a long-running main container.
	pauseImage = "registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a"

	// unpullableImage is an intentionally nonexistent tag: workload
	// pods stay pending forever, proving default runs never wait for
	// convergence.
	unpullableImage = "registry.k8s.io/pause:no-such-tag-exists"

	// sourceImageRef is a whole-token related-image reference the
	// file or environment source resolves.
	sourceImageRef = "quay.io/example/alpha:v1"

	// helmDefinitionTemplate is the Helm scenario definition.
	helmDefinitionTemplate = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic-UNIQUESUFFIX
spec:
  instance:
    type:
      group: e2eUNIQUESUFFIX.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
  relatedImages:
  - RELATED_IMAGE_ALPHA
  manifests:
    format: helm
    helm:
      path: ./manifests/chart
      values:
        message: VALUEMESSAGE
        replicas: REPLICAS
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: CONTROLLERIMAGE
        env:
        - name: LOG_LEVEL
          value: debug
      initContainer:
        name: installer
`

	// kustomizeDefinitionTemplate is the Kustomize scenario
	// definition: no values, placement by the engine's namespace
	// plugin.
	kustomizeDefinitionTemplate = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic-UNIQUESUFFIX
spec:
  instance:
    type:
      group: e2eUNIQUESUFFIX.platform.opendatahub.io
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
        image: CONTROLLERIMAGE
        env:
        - name: LOG_LEVEL
          value: debug
      initContainer:
        name: installer
`

	chartYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
`

	chartValuesYAML = `message: chart-default
replicas: 1
`

	// deploymentTemplate carries the controller package's workload:
	// an init container sharing the controller image by name, a main
	// container whose LOG_LEVEL starts as a valueFrom reference the
	// injector replaces, and a replica count the authored values
	// tune. Placement is the release namespace the template
	// requests.
	deploymentTemplate = `apiVersion: apps/v1
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
        image: registry.k8s.io/pause:3.10
      containers:
      - name: manager
        image: registry.k8s.io/pause:3.10
        env:
        - name: LOG_LEVEL
          valueFrom:
            configMapKeyRef:
              name: values-cm
              key: message
`

	// unrelatedDeploymentTemplate is a second, non-controller
	// Deployment: the env injector skips it and a requested wait
	// never observes it.
	unrelatedDeploymentTemplate = `apiVersion: apps/v1
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
        image: registry.k8s.io/pause:3.10
`

	serviceAccountTemplate = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: synthetic-sa
  namespace: {{ .Release.Namespace }}
`

	roleTemplate = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: synthetic-role
  namespace: {{ .Release.Namespace }}
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list", "watch"]
`

	// roleBindingTemplate references the Role and ServiceAccount in
	// the release namespace: real references, really placed.
	roleBindingTemplate = `apiVersion: rbac.authorization.k8s.io/v1
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
  namespace: {{ .Release.Namespace }}
`

	// payloadCRDTemplate is the module type's own CRD: the unique
	// per-scenario group keeps every run's payload type isolated.
	payloadCRDTemplate = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: syntheticmodules.e2eUNIQUESUFFIX.platform.opendatahub.io
spec:
  group: e2eUNIQUESUFFIX.platform.opendatahub.io
  names:
    plural: syntheticmodules
    singular: syntheticmodule
    kind: SyntheticModule
    listKind: SyntheticModuleList
  scope: Cluster
  versions:
  - name: v1alpha1
    served: true
    storage: true
    subresources:
      status: {}
    schema:
      openAPIV3Schema:
        type: object
        properties:
          status:
            type: object
            properties:
              phase:
                type: string
`

	// valuesConfigMapTemplate renders the authored chart value,
	// proving authored values survive composition onto the server.
	valuesConfigMapTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: values-cm
  namespace: {{ .Release.Namespace }}
data:
  message: {{ .Values.message | quote }}
`

	// sharedTargetConfigMapTemplate and sharedAuxConfigMapTemplate
	// are co-located entries: the same resource name in two real
	// namespaces, one placed by the release namespace and one
	// explicit.
	sharedTargetConfigMapTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
  namespace: {{ .Release.Namespace }}
data:
  placement: target
`

	sharedAuxConfigMapTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
  namespace: aux-UNIQUESUFFIX
data:
  placement: aux
`

	gateConfigMapTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: gate-cm
  namespace: {{ .Release.Namespace }}
  labels:
    app.opendatahub.io/upgrade-gate: "true"
data:
  gate: paused
`

	// foreignConfigMapTemplate is the payload side of the foreign
	// adoption scenario.
	foreignConfigMapTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: adopted-cm
  namespace: {{ .Release.Namespace }}
data:
  payload: value
`

	// reservedResourceTemplate renders the configured module type's
	// own kind: the shared payload guard rejects it at render,
	// before any write.
	reservedResourceTemplate = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: foreign-UNIQUESUFFIX
spec:
  instance:
    type:
      group: e2eUNIQUESUFFIX.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
`

	// invalidConfigMapTemplate carries a data key the API server
	// rejects: a bounded, real admission failure after the owner is
	// confirmed.
	invalidConfigMapTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: invalid-cm
  namespace: {{ .Release.Namespace }}
data:
  "invalid key!": value
`

	// auxNamespaceTemplate is the payload-supplied, uniquely named
	// Namespace: initially absent, created by the real framework
	// apply, and left without owner references.
	auxNamespaceTemplate = `apiVersion: v1
kind: Namespace
metadata:
  name: aux-UNIQUESUFFIX
`

	// The Kustomize scenario tree: plain resources placed by the
	// engine's namespace plugin, with the same workload shape as the
	// Helm chart.
	kustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
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
- shared.yaml
`

	baseKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
`

	kustomizeDeploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  replicas: 2
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
        image: registry.k8s.io/pause:3.10
      containers:
      - name: manager
        image: registry.k8s.io/pause:3.10
        env:
        - name: LOG_LEVEL
          valueFrom:
            configMapKeyRef:
              name: values-cm
              key: message
`

	kustomizeServiceAccountYAML = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: synthetic-sa
`

	kustomizeRoleYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: synthetic-role
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list", "watch"]
`

	// kustomizeRoleBindingYAML leaves the subject's namespace unset:
	// the engine's real namespace transformer must rewrite it.
	kustomizeRoleBindingYAML = `apiVersion: rbac.authorization.k8s.io/v1
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

	kustomizeValuesConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: values-cm
data:
  message: authored-value
`

	kustomizeGateConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: gate-cm
  labels:
    app.opendatahub.io/upgrade-gate: "true"
data:
  gate: paused
`

	kustomizeSharedConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
data:
  placement: target
`
)
