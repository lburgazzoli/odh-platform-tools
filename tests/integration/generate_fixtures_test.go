//go:build integration

package integration

import (
	"testing"
)

// Helm template fixtures specific to generation: release-name-bound
// controller identity, init-container selection, and .Files.Glob
// consumption of colocated chart files.
const (
	// generateReleaseDeploymentTemplateYAML binds the controller
	// Deployment name and image to the release name, proving the
	// generated definition selects the final render.
	generateReleaseDeploymentTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-controller
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
      containers:
      - name: manager
        image: quay.io/example/{{ .Release.Name }}:v1
`

	// generateTaggedDeploymentTemplateYAML derives the controller
	// image tag from values, proving merged overrides participate in
	// discovery and not only the emitted values block.
	generateTaggedDeploymentTemplateYAML = `apiVersion: apps/v1
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
      containers:
      - name: manager
        image: quay.io/chart/synthetic-controller:{{ .Values.tag | default "v0" }}
`

	// generateInitDeploymentTemplateYAML carries two init containers:
	// an explicit selection is mandatory and must name one exactly.
	generateInitDeploymentTemplateYAML = `apiVersion: apps/v1
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
      - name: first-setup
        image: quay.io/example/setup:v1
      - name: second-setup
        image: quay.io/example/setup:v2
      containers:
      - name: manager
        image: quay.io/chart/synthetic-controller:v9
`

	// generateAmbiguousDeploymentTemplateYAML has no manager and two
	// regular containers: strict discovery must reject it.
	generateAmbiguousDeploymentTemplateYAML = `apiVersion: apps/v1
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
      containers:
      - name: worker
        image: quay.io/example/worker:v1
      - name: sidecar
        image: quay.io/example/sidecar:v1
`

	// generateExtraDeploymentTemplateYAML is a second Deployment: the
	// bundle then renders two controllers.
	generateExtraDeploymentTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: extra-controller
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
        image: quay.io/example/extra:v1
`

	// generateGlobDeploymentTemplateYAML consumes every file under
	// extra/ through .Files.Glob, so a generated file landing there
	// unignored would change the rendered content.
	generateGlobDeploymentTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  annotations:
    globbed/files: {{ (.Files.Glob "extra/*.yaml").AsConfig | quote }}
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
      containers:
      - name: manager
        image: quay.io/chart/synthetic-controller:v9
`

	// generateExtraConfigYAML is the globbed chart payload.
	generateExtraConfigYAML = `key: value
`

	// generateExtraCRDYAML is a second Module CRD: the bundle then
	// renders two module types.
	generateExtraCRDYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: extramodules.components.platform.opendatahub.io
spec:
  group: components.platform.opendatahub.io
  names:
    plural: extramodules
    singular: extramodule
    kind: ExtraModule
    listKind: ExtraModuleList
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
`

	// generateDualStorageCRDYAML declares two storage versions: the
	// module GVK cannot be inferred from it.
	generateDualStorageCRDYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: syntheticmodules.components.platform.opendatahub.io
spec:
  group: components.platform.opendatahub.io
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
  - name: v1beta1
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
`

	// generateReservedInstanceTemplateYAML bundles an instance of the
	// module's own custom resource.
	generateReservedInstanceTemplateYAML = `apiVersion: components.platform.opendatahub.io/v1alpha1
kind: SyntheticModule
metadata:
  name: bundled-instance
`

	// generateLeakDeploymentTemplateYAML pipes a values string
	// through tpl, so a malformed value surfaces inside the native
	// renderer error text; the wrapper diagnostic must withhold it.
	generateLeakDeploymentTemplateYAML = `apiVersion: apps/v1
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
      containers:
      - name: manager
        image: quay.io/chart/synthetic-controller:v9
        env:
        - name: CONFIG
          value: {{ tpl .Values.config . | quote }}
`
)

// Generation option fixtures: merged values, related image names,
// and malformed inputs whose sensitive contents must never surface.
const (
	// generateSecretValuesYAML carries a secret-bearing string whose
	// malformed template content must never reach diagnostics.
	generateSecretValuesYAML = `config: |
  token: sensitive-render-token
  broken: {{ .Values.nope
`
	// generateOverrideValuesYAML exercises the preserved value
	// shapes: booleans, numbers, nested mappings, and explicit nulls.
	generateOverrideValuesYAML = `enabled: true
count: 3
nested:
  name: demo
  items:
  - one
  - two
empty: null
`

	// generateRelatedImagesYAML is unordered with a duplicate name and
	// value payloads that must never reach the generated document.
	generateRelatedImagesYAML = `relatedImages:
- name: zeta-image
  value: quay.io/example/secret-zeta:v1
- name: alpha-image
  value: quay.io/example/secret-alpha:v1
- name: alpha-image
  value: quay.io/example/secret-alpha:v2
`

	// generateMalformedValuesYAML is not a mapping document; its
	// contents must not appear in any diagnostic.
	generateMalformedValuesYAML = `- token: sensitive-token-123
`

	// generateMalformedImagesYAML is not a related-images document;
	// its contents must not appear in any diagnostic.
	generateMalformedImagesYAML = `relatedImages: sensitive-image-payload
`

	// generateIgnoreFileYAML excludes one exact chart-root file.
	generateIgnoreFileYAML = `gen.yaml
`

	// generateIgnoreAncestorYAML excludes one chart subdirectory.
	generateIgnoreAncestorYAML = `out/
`
)

// writeGenerateChart materializes a self-contained Helm chart source
// root: Chart.yaml, values.yaml, RBAC templates, and the given
// controller Deployment and CRD documents, plus any extra templates.
// An empty deployment or CRD omits that template.
func writeGenerateChart(
	t *testing.T,
	dir string,
	deployment string,
	crd string,
	extra map[string]string,
) {
	t.Helper()

	writeModuleFile(t, dir, "Chart.yaml", plainChartYAML)
	writeModuleFile(t, dir, "values.yaml", chartValuesYAML)

	if deployment != "" {
		writeModuleFile(t, dir, "templates/deployment.yaml", deployment)
	}

	if crd != "" {
		writeModuleFile(t, dir, "templates/crd.yaml", crd)
	}

	writeModuleFile(t, dir, "templates/serviceaccount.yaml", helmServiceAccountTemplateYAML)
	writeModuleFile(t, dir, "templates/role.yaml", helmRoleTemplateYAML)
	writeModuleFile(t, dir, "templates/rolebinding.yaml", helmRoleBindingTemplateYAML)

	for name, content := range extra {
		writeModuleFile(t, dir, "templates/"+name, content)
	}
}

// writeGenerateKustomizeSource materializes a self-contained
// Kustomize source root with a sibling base.
func writeGenerateKustomizeSource(t *testing.T, dir string) {
	t.Helper()

	writeModuleFile(t, dir, "kustomization.yaml", entryKustomizationYAML)
	writeModuleFile(t, dir, "base/kustomization.yaml", baseKustomizationYAML)
	writeModuleFile(t, dir, "base/deployment.yaml", kustomizeDeploymentYAML)
	writeModuleFile(t, dir, "crd.yaml", moduleCRDYAML)
	writeModuleFile(t, dir, "serviceaccount.yaml", kustomizeServiceAccountYAML)
	writeModuleFile(t, dir, "role.yaml", kustomizeRoleYAML)
	writeModuleFile(t, dir, "rolebinding.yaml", kustomizeRoleBindingYAML)
}
