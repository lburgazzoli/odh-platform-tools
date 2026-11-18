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

const (

	// unpullableImage is an intentionally nonexistent tag: workload
	// pods stay pending forever, proving default runs never wait for
	// convergence.
	unpullableImage = "registry.k8s.io/pause:no-such-tag-exists"

	// sourceImageRef is a whole-token related-image reference the
	// file or environment source resolves: only a complete {{NAME}}
	// token resolves, and resolution is never recursive. The value is
	// YAML-quoted because a bare {{ opens a flow mapping.
	sourceImageRef = "'{{RELATED_IMAGE_ALPHA}}'"

	// helmDefinitionTemplate is the Helm scenario definition.
	helmDefinitionTemplate = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: mock-UNIQUESUFFIX
spec:
  instance:
    name: default-mock-UNIQUESUFFIX
    type:
      group: e2eUNIQUESUFFIX.platform.opendatahub.io
      version: v1alpha1
      kind: MODULEKIND
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
      name: mock-controller
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
  name: mock-UNIQUESUFFIX
spec:
  instance:
    name: default-mock-UNIQUESUFFIX
    type:
      group: e2eUNIQUESUFFIX.platform.opendatahub.io
      version: v1alpha1
      kind: MODULEKIND
  relatedImages:
  - RELATED_IMAGE_ALPHA
  manifests:
    format: kustomize
    kustomize:
      path: ./manifests
  controller:
    deployment:
      name: mock-controller
      container:
        image: CONTROLLERIMAGE
        env:
        - name: LOG_LEVEL
          value: debug
      initContainer:
        name: installer
`

	chartYAML = `apiVersion: v2
name: mock
version: 1.0.0
`

	chartValuesYAML = `message: chart-default
replicas: 1
`

	// deploymentTemplate carries the controller package's workload:
	// an init container sharing the controller image by name, a main
	// container whose LOG_LEVEL starts as a valueFrom reference the
	// injector replaces, and a replica count the authored values
	// tune. The init invokes CLI help and the main runs module mock
	// in the rendered namespace.
	deploymentTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: mock-controller
  namespace: {{ .Release.Namespace }}
spec:
  replicas: {{ .Values.replicas }}
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: mock
  template:
    metadata:
      labels:
        app: mock
    spec:
      serviceAccountName: mock-sa
      initContainers:
      - name: installer
        image: CONTROLLERIMAGE
        imagePullPolicy: IfNotPresent
        args: ["module", "mock", "--help"]
      containers:
      - name: manager
        image: CONTROLLERIMAGE
        imagePullPolicy: IfNotPresent
        args:
        - module
        - mock
        - --api-version=e2eUNIQUESUFFIX.platform.opendatahub.io/v1alpha1
        - --kind=MODULEKIND
        - --platform-configmap=odh-mock-UNIQUESUFFIX-config
        ports:
        - name: probes
          containerPort: 8081
        livenessProbe:
          httpGet:
            path: /healthz
            port: probes
        readinessProbe:
          httpGet:
            path: /readyz
            port: probes
        env:
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
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
  name: mock-sa
  namespace: {{ .Release.Namespace }}
`

	roleTemplate = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: mock-role
  namespace: {{ .Release.Namespace }}
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  resourceNames: [odh-mock-UNIQUESUFFIX-config]
  verbs: ["get", "list", "watch"]
`

	// roleBindingTemplate references the Role and ServiceAccount in
	// the release namespace: real references, really placed.
	roleBindingTemplate = `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: mock-binding
  namespace: {{ .Release.Namespace }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: mock-role
subjects:
- kind: ServiceAccount
  name: mock-sa
  namespace: {{ .Release.Namespace }}
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
    name: default-mock-UNIQUESUFFIX
    type:
      group: e2eUNIQUESUFFIX.platform.opendatahub.io
      version: v1alpha1
      kind: MODULEKIND
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
- clusterrole.yaml
- clusterrolebinding.yaml
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
  name: mock-controller
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: mock
  template:
    metadata:
      labels:
        app: mock
    spec:
      serviceAccountName: mock-sa
      initContainers:
      - name: installer
        image: CONTROLLERIMAGE
        imagePullPolicy: IfNotPresent
        args: ["module", "mock", "--help"]
      containers:
      - name: manager
        image: CONTROLLERIMAGE
        imagePullPolicy: IfNotPresent
        args:
        - module
        - mock
        - --api-version=e2eUNIQUESUFFIX.platform.opendatahub.io/v1alpha1
        - --kind=MODULEKIND
        - --platform-configmap=odh-mock-UNIQUESUFFIX-config
        ports:
        - name: probes
          containerPort: 8081
        livenessProbe:
          httpGet:
            path: /healthz
            port: probes
        readinessProbe:
          httpGet:
            path: /readyz
            port: probes
        env:
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: LOG_LEVEL
          valueFrom:
            configMapKeyRef:
              name: values-cm
              key: message
`

	kustomizeServiceAccountYAML = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: mock-sa
`

	kustomizeRoleYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: mock-role
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  resourceNames: [odh-mock-UNIQUESUFFIX-config]
  verbs: ["get", "list", "watch"]
`

	// kustomizeRoleBindingYAML leaves the subject's namespace unset:
	// the engine's real namespace transformer must rewrite it.
	kustomizeRoleBindingYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: mock-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: mock-role
subjects:
- kind: ServiceAccount
  name: mock-sa
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

// Event writes for a cluster-scoped module are recorded in default.
const liveClusterRoleYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: mock-UNIQUESUFFIX-mock-controller
rules:
- apiGroups: [e2eUNIQUESUFFIX.platform.opendatahub.io]
  resources: [MODULEPLURAL]
  verbs: [get, list, watch]
- apiGroups: [e2eUNIQUESUFFIX.platform.opendatahub.io]
  resources: [MODULEPLURAL/status]
  verbs: [get, patch, update]
- apiGroups: [events.k8s.io]
  resources: [events]
  verbs: [create, patch]
`

const helmLiveClusterRoleBindingYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: mock-UNIQUESUFFIX-mock-controller
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: mock-UNIQUESUFFIX-mock-controller
subjects:
- kind: ServiceAccount
  name: mock-sa
  namespace: {{ .Release.Namespace }}
`

const kustomizeLiveClusterRoleBindingYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: mock-UNIQUESUFFIX-mock-controller
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: mock-UNIQUESUFFIX-mock-controller
subjects:
- kind: ServiceAccount
  name: mock-sa
`
