//go:build integration

// Package integration exercises the complete offline lint chain:
// actual definition files, the strict reusable loader, the actual
// framework Helm and Kustomize render actions, and the pure
// validator, composed through the public module.NewLinter action.
// Everything runs without Kubernetes, credentials, network access,
// containers, or external renderer binaries.
package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// The synthetic module exercised by the offline chain: a controller
// Deployment with the default main container name, bundled
// ServiceAccount and RBAC, and a cluster-scoped Module CRD with
// static status and singleton evidence.
const (
	helmDefinitionYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
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
        env:
        - name: LOG_LEVEL
          value: debug
`

	kustomizeDefinitionYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  type:
    group: components.platform.opendatahub.io
    version: v1alpha1
    kind: SyntheticModule
  manifests:
    format: kustomize
    kustomize:
      path: ./manifests
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`
)

// Helm chart inputs: the controller Deployment, ServiceAccount,
// RBAC pair, and Module CRD as regular templates, plus a vendored
// dependency chart.
const (
	chartYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- name: vendored
  version: 1.2.3
  repository: https://charts.example.com
`

	chartValuesYAML = `replicas: 1
`

	helmDeploymentTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
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
        image: quay.io/chart/synthetic-controller:v9
`

	helmServiceAccountTemplateYAML = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: synthetic-sa
`

	helmRoleTemplateYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: synthetic-role
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list", "watch"]
`

	helmRoleBindingTemplateYAML = `apiVersion: rbac.authorization.k8s.io/v1
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

	vendoredChartYAML = `apiVersion: v2
name: vendored
version: 1.2.3
`

	vendoredServiceTemplateYAML = `apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-service
`
)

// Kustomize inputs: an entry kustomization with a sibling base inside
// the module root plus direct resource files.
const (
	entryKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- ./base
- crd.yaml
- serviceaccount.yaml
- role.yaml
- rolebinding.yaml
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
)

// moduleCRDYAML is the Module CRD shared by both backends: cluster
// scoped, one served storage version, status subresource, structural
// schema with the demonstrable status contract, and fixed-name CEL
// singleton evidence.
const moduleCRDYAML = `apiVersion: apiextensions.k8s.io/v1
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
        x-kubernetes-validations:
        - rule: self.metadata.name == 'default-synthetic'
        properties:
          status:
            type: object
            properties:
              observedGeneration:
                type: integer
              conditions:
                type: array
                items:
                  type: object
                  properties:
                    type:
                      type: string
                    status:
                      type: string
                    reason:
                      type: string
                    message:
                      type: string
                    lastTransitionTime:
                      type: string
                      format: date-time
                    observedGeneration:
                      type: integer
              releases:
                type: array
                items:
                  type: object
                  properties:
                    name:
                      type: string
                    version:
                      type: string
`

// Negative inputs: definition defects, unsafe transitive sources,
// render failures, and finding-bearing objects.
const (
	// plainChartYAML declares no dependencies for finding fixtures
	// that do not vendor a chart archive.
	plainChartYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
`

	invalidDefinitionYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: broken
spec:
  type:
    version: v1alpha1
    kind: SyntheticModule
  manifests:
    format: kustomize
    kustomize:
      path: ./manifests
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

	escapingKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- ../../external/resource.yaml
`

	externalResourceYAML = `apiVersion: v1
kind: Secret
metadata:
  name: external-secret
`

	brokenTemplateYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: broken
data:
  key: {{ .Values.missing
`

	namespaceTemplateYAML = `apiVersion: v1
kind: Namespace
metadata:
  name: synthetic-namespace
`

	listTemplateYAML = `apiVersion: v1
kind: List
metadata:
  name: synthetic-list
items:
- apiVersion: v1
  kind: ConfigMap
  metadata:
    name: listed-configmap
`

	unresolvedRoleBindingTemplateYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: dangling-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: missing-role
subjects:
- kind: ServiceAccount
  name: missing-sa
`
)

// writeModuleFile writes content at a module-root-relative path.
func writeModuleFile(t *testing.T, root, rel, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildArchive builds a gzipped tar chart archive.
func buildArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)

	for _, name := range slices.Sorted(maps.Keys(entries)) {
		content := entries[name]

		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buffer.Bytes()
}

// writeHelmModule materializes the valid Helm module tree.
func writeHelmModule(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", chartValuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", helmDeploymentTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/serviceaccount.yaml", helmServiceAccountTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/role.yaml", helmRoleTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/rolebinding.yaml", helmRoleBindingTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/crd.yaml", moduleCRDYAML)

	archive := buildArchive(t, map[string]string{
		"vendored/Chart.yaml":         vendoredChartYAML,
		"vendored/templates/svc.yaml": vendoredServiceTemplateYAML,
	})
	writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))
}

// writeKustomizeModule materializes the valid Kustomize module tree
// with a sibling base inside the module root.
func writeKustomizeModule(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", kustomizeDefinitionYAML)
	writeModuleFile(t, root, "manifests/kustomization.yaml", entryKustomizationYAML)
	writeModuleFile(t, root, "manifests/base/kustomization.yaml", baseKustomizationYAML)
	writeModuleFile(t, root, "manifests/base/deployment.yaml", kustomizeDeploymentYAML)
	writeModuleFile(t, root, "manifests/crd.yaml", moduleCRDYAML)
	writeModuleFile(t, root, "manifests/serviceaccount.yaml", kustomizeServiceAccountYAML)
	writeModuleFile(t, root, "manifests/role.yaml", kustomizeRoleYAML)
	writeModuleFile(t, root, "manifests/rolebinding.yaml", kustomizeRoleBindingYAML)
}

// lint runs the public composed action over one module root.
func lint(t *testing.T, root string) (linter.Result, error) {
	t.Helper()

	return lintContext(t.Context(), t, root)
}

// lintContext runs the public composed action with an explicit
// context over one module root.
func lintContext(
	ctx context.Context,
	t *testing.T,
	root string,
) (linter.Result, error) {
	t.Helper()

	return module.NewLinter().Run(ctx, linter.Options{
		DefinitionFile: filepath.Join(root, "definition.yaml"),
	})
}

// diagnosticIDs collects the check IDs of a lint result.
func diagnosticIDs(result linter.Result) []string {
	ids := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		ids = append(ids, diagnostic.ID)
	}

	return ids
}
