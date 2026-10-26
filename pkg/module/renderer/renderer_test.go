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

package renderer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// helmDefinitionYAML is the valid Helm module definition.
const helmDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
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

// helmDefinitionWithEnvYAML is the Helm module definition with
// controller descriptor env entries.
const helmDefinitionWithEnvYAML = `apiVersion: config.opendatahub.io/v1alpha1
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
        image: quay.io/example/controller:v2
        env:
        - name: LOG_LEVEL
          value: debug
`

// helmDefinitionWithInstanceNameYAML sets an explicit instance name
// different from the module name.
const helmDefinitionWithInstanceNameYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    name: synthetic-trainer
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

// helmCoLocatedDefinitionYAML selects the Helm backend with no helm
// block: the entry defaults to the definition file's own directory.
const helmCoLocatedDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
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
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

// helmDefinitionWithValuesYAML authors literal chart values with an
// omitted path: the entry defaults to the definition file's own
// directory and the values merge over the chart defaults.
const helmDefinitionWithValuesYAML = `apiVersion: config.opendatahub.io/v1alpha1
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
      values:
        replicas: 2
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

// helmChartYAML declares a chart with a vendored dependency.
const helmChartYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- name: vendored
  version: 1.2.3
  repository: https://charts.example.com
`

// helmValuesYAML is the chart default values.
const helmValuesYAML = `replicas: 1
`

// helmValuesSchemaJSON constrains the chart values.
const helmValuesSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "replicas": {"type": "integer", "minimum": 1}
  }
}
`

// helmTemplateYAML is the chart's namespace-less deployment template.
const helmTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-controller
spec:
  replicas: {{ .Values.replicas }}
`

// helmNamespacedTemplateYAML is the chart's deployment template with
// the namespace templated from the release.
const helmNamespacedTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-controller
  namespace: {{ .Release.Namespace }}
spec:
  replicas: {{ .Values.replicas }}
`

// helmVendoredTemplateYAML is the vendored dependency's namespace-less
// service template.
const helmVendoredTemplateYAML = `apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-service
`

// helmVendoredChartYAML declares the vendored dependency chart.
const helmVendoredChartYAML = `apiVersion: v2
name: vendored
version: 1.2.3
`

// sharedAlphaConfigMapYAML and sharedBetaConfigMapYAML are same-named
// ConfigMaps in different namespaces.
const sharedAlphaConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
  namespace: alpha
`

const sharedBetaConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
  namespace: beta
`

// helmValuesKindsTemplateYAML renders the authored value kinds into
// ConfigMap data.
const helmValuesKindsTemplateYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: values-cm
data:
  scalar: {{ .Values.scalar | toJson | quote }}
  nested: {{ .Values.nested | toJson | quote }}
  list: {{ .Values.list | toJson | quote }}
  flag: {{ .Values.flag | toJson | quote }}
  placeholder: {{ .Values.placeholder | toJson | quote }}
`

// helmValuesKindsDefinitionYAML authors literal chart values of every
// JSON kind, including a null entry.
const helmValuesKindsDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
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
      values:
        scalar: 2
        nested:
          tag: v1.0.0
        list:
        - a
        - b
        flag: true
        nullValue: null
        placeholder: "{{RELATED_IMAGE_EXAMPLE_CONTROLLER}}"
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

// kustomizeDefinitionYAML is the valid Kustomize module definition.
const kustomizeDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
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
    format: kustomize
    kustomize:
      path: ./manifests/overlays/development
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

// kustomizeDefinitionWithEnvYAML is the Kustomize module definition
// with controller descriptor env entries.
const kustomizeDefinitionWithEnvYAML = `apiVersion: config.opendatahub.io/v1alpha1
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
    format: kustomize
    kustomize:
      path: ./manifests/overlays/development
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v2
        env:
        - name: LOG_LEVEL
          value: debug
`

// kustomizeDefinitionWithInstanceNameYAML sets an explicit instance
// name different from the module name.
const kustomizeDefinitionWithInstanceNameYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    name: synthetic-trainer
    type:
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
  manifests:
    format: kustomize
    kustomize:
      path: ./manifests/overlays/development
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

// overlayKustomizationYAML is the overlay entry with a direct
// resource, a sibling base, a patch, and a generator.
const overlayKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
- rolebinding.yaml
- crd.yaml
- ../../base
configMapGenerator:
- name: params
  envs:
  - params.env
patches:
- path: patch.yaml
  target:
    kind: Deployment
    name: module-controller
`

// baseKustomizationYAML is the sibling base entry.
const baseKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- service.yaml
`

// deploymentResourceYAML is the namespaced Deployment resource.
const deploymentResourceYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: module-controller
spec:
  replicas: 1
`

// serviceResourceYAML is the namespaced Service resource.
const serviceResourceYAML = `apiVersion: v1
kind: Service
metadata:
  name: module-service
`

// roleBindingResourceYAML is a RoleBinding whose subject carries no
// authored namespace.
const roleBindingResourceYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: module-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: module-role
subjects:
- kind: ServiceAccount
  name: module-controller
`

// clusterScopedResourceYAML is a cluster-scoped CRD that must not
// receive a namespace.
const clusterScopedResourceYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: syntheticmodules.components.platform.opendatahub.io
`

// patchYAML patches the Deployment replica count.
const patchYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: module-controller
spec:
  replicas: 2
`

// paramsEnv feeds the ConfigMap generator.
const paramsEnv = `LOG_LEVEL=info
`

// kustomizeCoLocatedDefinitionYAML selects the Kustomize backend with
// no kustomize block: the entry defaults to the definition file's own
// directory.
const kustomizeCoLocatedDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
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
    format: kustomize
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

// kustomizeCoLocatedKustomizationYAML is a self-contained entry with
// one direct resource.
const kustomizeCoLocatedKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
`

// clusterRoleBindingResourceYAML is a cluster-scoped ClusterRoleBinding
// whose ServiceAccount subject carries no authored namespace.
const clusterRoleBindingResourceYAML = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: module-clusterrolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: module-role
subjects:
- kind: ServiceAccount
  name: module-controller
`

// validatingWebhookResourceYAML is a ValidatingWebhookConfiguration
// whose webhook targets a service in another namespace.
const validatingWebhookResourceYAML = `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: module-validating
webhooks:
- name: validate.module.io
  clientConfig:
    service:
      namespace: webhook-system
      name: module-webhook
`

// mutatingWebhookResourceYAML is a MutatingWebhookConfiguration whose
// webhook targets a service in another namespace.
const mutatingWebhookResourceYAML = `apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: module-mutating
webhooks:
- name: mutate.module.io
  clientConfig:
    service:
      namespace: webhook-system
      name: module-webhook
`

// urlWebhookResourceYAML is a ValidatingWebhookConfiguration whose
// webhook targets a URL: the service reference path is absent, and
// the transformer must not create one.
const urlWebhookResourceYAML = `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: module-url
webhooks:
- name: remote.module.io
  clientConfig:
    url: https://example.com/validate
`

// crdWithConversionResourceYAML is a CRD whose conversion webhook
// targets a service in another namespace.
const crdWithConversionResourceYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: convertedmodules.components.platform.opendatahub.io
spec:
  conversion:
    strategy: Webhook
    webhook:
      clientConfig:
        service:
          namespace: webhook-system
          name: module-webhook
`

// referencesKustomizationYAML is an entry with the cross-reference
// resources the namespace plugin rewrites.
const referencesKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
- clusterrolebinding.yaml
- validatingwebhook.yaml
- mutatingwebhook.yaml
- urlwebhook.yaml
- crd.yaml
`

// writeModuleFile writes content at a module-root-relative path.
func writeModuleFile(t *testing.T, root string, rel string, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadDefinition loads the module definition through the real loader.
func loadDefinition(t *testing.T, root string) v1alpha1.PlatformModuleDefinition {
	t.Helper()

	return loadDefinitionFile(t, root, "definition.yaml")
}

// loadDefinitionFile loads the module definition at a
// module-root-relative path through the real loader.
func loadDefinitionFile(t *testing.T, root string, rel string) v1alpha1.PlatformModuleDefinition {
	t.Helper()

	g := NewWithT(t)

	definition, err := loader.LoadFromPath(t.Context(), filepath.Join(root, filepath.FromSlash(rel)))
	g.Expect(err).NotTo(HaveOccurred())

	return definition
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

// writeHelmChartFixture writes the standard Helm module tree.
func writeHelmChartFixture(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", helmChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", helmValuesYAML)
	writeModuleFile(t, root, "manifests/chart/values.schema.json", helmValuesSchemaJSON)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", helmTemplateYAML)

	archive := buildArchive(t, map[string]string{
		"vendored/Chart.yaml":         helmVendoredChartYAML,
		"vendored/templates/svc.yaml": helmVendoredTemplateYAML,
	})
	writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))
}

// writeHelmCoLocatedChartFixture writes the chart with the definition
// file inside it, next to Chart.yaml.
func writeHelmCoLocatedChartFixture(t *testing.T, root string, definitionContent string) {
	t.Helper()

	writeModuleFile(t, root, "Chart.yaml", helmChartYAML)
	writeModuleFile(t, root, "values.yaml", helmValuesYAML)
	writeModuleFile(t, root, "values.schema.json", helmValuesSchemaJSON)
	writeModuleFile(t, root, "templates/deployment.yaml", helmTemplateYAML)
	writeModuleFile(t, root, "module.yaml", definitionContent)
}

// writeKustomizeFixture writes the standard Kustomize module tree:
// an overlay with direct sibling base, patch, and generator inputs.
func writeKustomizeFixture(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", kustomizeDefinitionYAML)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", overlayKustomizationYAML)
	writeModuleFile(t, root, "manifests/overlays/development/deployment.yaml", deploymentResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/rolebinding.yaml", roleBindingResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/crd.yaml", clusterScopedResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/patch.yaml", patchYAML)
	writeModuleFile(t, root, "manifests/overlays/development/params.env", paramsEnv)
	writeModuleFile(t, root, "manifests/base/kustomization.yaml", baseKustomizationYAML)
	writeModuleFile(t, root, "manifests/base/service.yaml", serviceResourceYAML)
}

// writeReferencesFixture writes a Kustomize module tree exercising
// the namespace plugin's cross-reference rewriting.
func writeReferencesFixture(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", kustomizeDefinitionYAML)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", referencesKustomizationYAML)
	writeModuleFile(t, root, "manifests/overlays/development/deployment.yaml", deploymentResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/clusterrolebinding.yaml", clusterRoleBindingResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/validatingwebhook.yaml", validatingWebhookResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/mutatingwebhook.yaml", mutatingWebhookResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/urlwebhook.yaml", urlWebhookResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/crd.yaml", crdWithConversionResourceYAML)
}

// findResource returns the rendered object of the given kind and name.
func findResource(t *testing.T, resources []unstructured.Unstructured, kind string, name string) unstructured.Unstructured {
	t.Helper()

	for _, resource := range resources {
		if resource.GetKind() == kind && resource.GetName() == name {
			return resource
		}
	}

	t.Fatalf("no %s %s in rendered output: %v", kind, name, resources)

	return unstructured.Unstructured{}
}

// findResourceByNamePrefix returns the first rendered object of the
// given kind whose name starts with prefix.
func findResourceByNamePrefix(
	t *testing.T,
	resources []unstructured.Unstructured,
	kind string,
	prefix string,
) unstructured.Unstructured {
	t.Helper()

	for _, resource := range resources {
		if resource.GetKind() == kind && strings.HasPrefix(resource.GetName(), prefix) {
			return resource
		}
	}

	t.Fatalf("no %s with prefix %s in rendered output: %v", kind, prefix, resources)

	return unstructured.Unstructured{}
}

// assertNoRendererMetadata fails when any rendered object carries a
// renderer-added annotation from the manifest kit.
func assertNoRendererMetadata(t *testing.T, resources []unstructured.Unstructured) {
	t.Helper()

	for i := range resources {
		for key := range resources[i].GetAnnotations() {
			if strings.HasPrefix(key, "manifests.k8s-manifests-kit/") {
				t.Fatalf("renderer-added annotation %q on %s %s", key, resources[i].GetKind(), resources[i].GetName())
			}
		}
	}
}

// resourceKinds returns the rendered objects' kinds in order.
func resourceKinds(resources []unstructured.Unstructured) []string {
	kinds := make([]string, 0, len(resources))

	for i := range resources {
		kinds = append(kinds, resources[i].GetKind())
	}

	return kinds
}

// TestRenderHelmChart proves the Helm backend renders through the
// public service: the release name is the module name, chart values
// map from the chart defaults, and vendored dependency templates
// render from the chart's charts/ archive.
func TestRenderHelmChart(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmChartFixture(t, root)

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resources).NotTo(BeEmpty())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(1)))

	g.Expect(findResource(t, resources, "Service", "synthetic-service")).NotTo(BeZero())
}

// TestRenderKustomize proves the Kustomize backend renders through
// the public service: direct resource files, sibling bases, patches,
// and generators all load with engine restrictions off, as in a plain
// kustomize build.
func TestRenderKustomize(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resources).NotTo(BeEmpty())

	deployment := findResource(t, resources, "Deployment", "module-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(2)))

	g.Expect(findResource(t, resources, "Service", "module-service")).NotTo(BeZero())

	configMap := findResourceByNamePrefix(t, resources, "ConfigMap", "params-")
	data, found, err := unstructured.NestedStringMap(configMap.Object, "data")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(data).To(HaveKeyWithValue("LOG_LEVEL", "info"))
}

// TestRenderRejectsUnsupportedFormat proves backend selection is the
// service's own: an unsupported manifest format is a definition
// failure with the ErrInvalidDefinition classification, and no render
// happens.
func TestRenderRejectsUnsupportedFormat(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmChartFixture(t, root)

	definition := loadDefinition(t, root)
	definition.Spec.Manifests.Format = v1alpha1.Format("bogus")

	resources, err := Render(t.Context(), definition, filepath.Join(root, "definition.yaml"))

	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("bogus"))
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	g.Expect(resources).To(BeNil())
}

// TestRenderHelmCoLocatedChart proves a Helm definition that omits the
// helm block renders the chart co-located with the definition file:
// the entry defaults to the definition file's own directory, and the
// chart loader carries the definition file as inert chart content.
func TestRenderHelmCoLocatedChart(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmCoLocatedChartFixture(t, root, helmCoLocatedDefinitionYAML)

	resources, err := Render(t.Context(), loadDefinitionFile(t, root, "module.yaml"), filepath.Join(root, "module.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(1)))
}

// TestRenderKustomizeCoLocated proves a Kustomize definition that
// omits the kustomize block renders the entry co-located with the
// definition file: kustomization.yaml, resources, and the definition
// file share one directory.
func TestRenderKustomizeCoLocated(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "kustomization.yaml", kustomizeCoLocatedKustomizationYAML)
	writeModuleFile(t, root, "deployment.yaml", deploymentResourceYAML)
	writeModuleFile(t, root, "module.yaml", kustomizeCoLocatedDefinitionYAML)

	resources, err := Render(t.Context(), loadDefinitionFile(t, root, "module.yaml"), filepath.Join(root, "module.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "module-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(1)))
}

// TestRenderHelmDefinitionValues proves authored literal values merge
// over the chart defaults with operator-identical precedence — chart
// default 1, definition value 2 — with the omitted path defaulting to
// the definition file's directory.
func TestRenderHelmDefinitionValues(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmCoLocatedChartFixture(t, root, helmDefinitionWithValuesYAML)

	resources, err := Render(t.Context(), loadDefinitionFile(t, root, "module.yaml"), filepath.Join(root, "module.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(2)))
}

// TestRenderHelmDefinitionValueKinds proves authored values of every
// JSON kind survive loading and lazy decoding into the render —
// including a null entry, whose apiextensions JSON representation has
// empty Raw and must decode as JSON null rather than fail with EOF.
func TestRenderHelmDefinitionValueKinds(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "Chart.yaml", helmChartYAML)
	writeModuleFile(t, root, "templates/configmap.yaml", helmValuesKindsTemplateYAML)
	writeModuleFile(t, root, "module.yaml", helmValuesKindsDefinitionYAML)

	resources, err := Render(t.Context(), loadDefinitionFile(t, root, "module.yaml"), filepath.Join(root, "module.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	configMap := findResource(t, resources, "ConfigMap", "values-cm")
	data, found, err := unstructured.NestedStringMap(configMap.Object, "data")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(data).To(SatisfyAll(
		HaveKeyWithValue("scalar", "2"),
		HaveKeyWithValue("nested", `{"tag":"v1.0.0"}`),
		HaveKeyWithValue("list", `["a","b"]`),
		HaveKeyWithValue("flag", "true"),
		// Placeholder-looking text stays literal.
		HaveKeyWithValue("placeholder", `"{{RELATED_IMAGE_EXAMPLE_CONTROLLER}}"`),
	))
}

// TestRenderIgnoresControllerDescriptor proves descriptor images and
// env entries never reach rendering for either backend: no process
// environment is read and the output is identical.
func TestRenderIgnoresControllerDescriptor(t *testing.T) {
	t.Parallel()

	t.Run("helm", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		withDescriptor := t.TempDir()
		writeHelmChartFixture(t, withDescriptor)
		writeModuleFile(t, withDescriptor, "definition.yaml", helmDefinitionWithEnvYAML)

		withoutDescriptor := t.TempDir()
		writeHelmChartFixture(t, withoutDescriptor)

		first, err := Render(t.Context(), loadDefinition(t, withDescriptor), filepath.Join(withDescriptor, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		second, err := Render(t.Context(), loadDefinition(t, withoutDescriptor), filepath.Join(withoutDescriptor, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(first).To(Equal(second))
	})

	t.Run("kustomize", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		withDescriptor := t.TempDir()
		writeKustomizeFixture(t, withDescriptor)
		writeModuleFile(t, withDescriptor, "definition.yaml", kustomizeDefinitionWithEnvYAML)

		withoutDescriptor := t.TempDir()
		writeKustomizeFixture(t, withoutDescriptor)

		first, err := Render(t.Context(), loadDefinition(t, withDescriptor), filepath.Join(withDescriptor, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		second, err := Render(t.Context(), loadDefinition(t, withoutDescriptor), filepath.Join(withoutDescriptor, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(first).To(Equal(second))
	})
}

// TestRenderIgnoresInstanceName proves the configured instance name
// is reserved for future custom resource creation and lookup: the
// rendered resources are identical whether it is omitted or explicit.
func TestRenderIgnoresInstanceName(t *testing.T) {
	t.Parallel()

	t.Run("helm", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		withoutName := t.TempDir()
		writeHelmChartFixture(t, withoutName)

		withName := t.TempDir()
		writeHelmChartFixture(t, withName)
		writeModuleFile(t, withName, "definition.yaml", helmDefinitionWithInstanceNameYAML)

		omitted, err := Render(t.Context(), loadDefinition(t, withoutName), filepath.Join(withoutName, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		explicit, err := Render(t.Context(), loadDefinition(t, withName), filepath.Join(withName, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(explicit).To(Equal(omitted))
	})

	t.Run("kustomize", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		withoutName := t.TempDir()
		writeKustomizeFixture(t, withoutName)

		withName := t.TempDir()
		writeKustomizeFixture(t, withName)
		writeModuleFile(t, withName, "definition.yaml", kustomizeDefinitionWithInstanceNameYAML)

		omitted, err := Render(t.Context(), loadDefinition(t, withoutName), filepath.Join(withoutName, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		explicit, err := Render(t.Context(), loadDefinition(t, withName), filepath.Join(withName, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(explicit).To(Equal(omitted))
	})
}

// TestRenderIsStatelessAcrossCalls proves repeated renders of the
// same module are identical for both backends: no state leaks between
// calls, interleaved or not.
func TestRenderIsStatelessAcrossCalls(t *testing.T) {
	t.Parallel()

	t.Run("helm", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		first := t.TempDir()
		writeHelmChartFixture(t, first)

		second := t.TempDir()
		writeHelmChartFixture(t, second)
		writeModuleFile(t, second, "definition.yaml", strings.Replace(helmDefinitionYAML, "name: synthetic", "name: other", 1))

		firstOutput, err := Render(t.Context(), loadDefinition(t, first), filepath.Join(first, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		_, err = Render(t.Context(), loadDefinition(t, second), filepath.Join(second, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		repeated, err := Render(t.Context(), loadDefinition(t, first), filepath.Join(first, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(repeated).To(Equal(firstOutput))
	})

	t.Run("kustomize", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		first := t.TempDir()
		writeKustomizeFixture(t, first)

		second := t.TempDir()
		writeKustomizeFixture(t, second)
		writeModuleFile(t, second, "definition.yaml", strings.Replace(kustomizeDefinitionYAML, "name: synthetic", "name: other", 1))

		firstOutput, err := Render(t.Context(), loadDefinition(t, first), filepath.Join(first, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		_, err = Render(t.Context(), loadDefinition(t, second), filepath.Join(second, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		repeated, err := Render(t.Context(), loadDefinition(t, first), filepath.Join(first, "definition.yaml"))
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(repeated).To(Equal(firstOutput))
	})
}

// TestRenderPropagatesCancellation proves an already-cancelled context
// fails the render before any backend runs, for both formats.
func TestRenderPropagatesCancellation(t *testing.T) {
	t.Parallel()

	t.Run("helm", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeHelmChartFixture(t, root)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		resources, err := Render(ctx, loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
		g.Expect(err).To(HaveOccurred())
		g.Expect(resources).To(BeNil())
		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})

	t.Run("kustomize", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeKustomizeFixture(t, root)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		resources, err := Render(ctx, loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
		g.Expect(err).To(HaveOccurred())
		g.Expect(resources).To(BeNil())
		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})
}

// TestRenderHelmReleaseNamespace proves the namespace option becomes
// the Helm release namespace through the public service: a template
// using .Release.Namespace renders into it, while Helm never fills
// metadata.namespace itself — a template that does not use it stays
// namespace-less, exactly as the operator's normal deploy action
// would see it.
func TestRenderHelmReleaseNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmChartFixture(t, root)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", helmNamespacedTemplateYAML)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	resources, err := Render(t.Context(), definition, definitionFile, WithNamespace("opendatahub"))
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "opendatahub"`),
	))

	service := findResource(t, resources, "Service", "synthetic-service")
	g.Expect(service.Object).To(SatisfyAll(
		jq.Match(`.metadata | has("namespace") | not`),
	))
}

// TestRenderKustomizeAppliesNamespace proves the namespace option
// reaches the Kustomize backend's real namespace transformer through
// the public service: namespaced resources, including generated ones,
// and RoleBinding subjects receive the namespace while cluster-scoped
// objects keep their scope.
func TestRenderKustomizeAppliesNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"), WithNamespace("opendatahub"))
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "module-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "opendatahub"`),
	))

	service := findResource(t, resources, "Service", "module-service")
	g.Expect(service.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "opendatahub"`),
	))

	configMap := findResourceByNamePrefix(t, resources, "ConfigMap", "params-")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "opendatahub"`),
	))

	roleBinding := findResource(t, resources, "RoleBinding", "module-rolebinding")
	g.Expect(roleBinding.Object).To(SatisfyAll(
		jq.Match(`.subjects[0].namespace == "opendatahub"`),
	))

	crd := findResource(t, resources, "CustomResourceDefinition", "syntheticmodules.components.platform.opendatahub.io")
	g.Expect(crd.Object).To(SatisfyAll(
		jq.Match(`.metadata | has("namespace") | not`),
	))
}

// TestRenderKustomizeNamespacePluginReferences proves the builtin
// namespace transformer is configured exactly as the operator's
// Kustomize render action configures it, through the public service:
// ClusterRoleBinding ServiceAccount subjects and both webhook kinds'
// service references receive the namespace, the absent optional
// service path of a URL-based webhook is not created, and
// cluster-scoped objects keep their scope.
func TestRenderKustomizeNamespacePluginReferences(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeReferencesFixture(t, root)

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"), WithNamespace("opendatahub"))
	g.Expect(err).NotTo(HaveOccurred())

	clusterRoleBinding := findResource(t, resources, "ClusterRoleBinding", "module-clusterrolebinding")
	g.Expect(clusterRoleBinding.Object).To(SatisfyAll(
		jq.Match(`.subjects[0].namespace == "opendatahub"`),
		jq.Match(`.metadata | has("namespace") | not`),
	))

	validating := findResource(t, resources, "ValidatingWebhookConfiguration", "module-validating")
	g.Expect(validating.Object).To(SatisfyAll(
		jq.Match(`.webhooks[0].clientConfig.service.namespace == "opendatahub"`),
	))

	mutating := findResource(t, resources, "MutatingWebhookConfiguration", "module-mutating")
	g.Expect(mutating.Object).To(SatisfyAll(
		jq.Match(`.webhooks[0].clientConfig.service.namespace == "opendatahub"`),
	))

	// The URL-based webhook has no service reference: none is created.
	urlWebhook := findResource(t, resources, "ValidatingWebhookConfiguration", "module-url")
	g.Expect(urlWebhook.Object).To(SatisfyAll(
		jq.Match(`.webhooks[0].clientConfig.url == "https://example.com/validate"`),
		jq.Match(`.webhooks[0].clientConfig | has("service") | not`),
	))

	crd := findResource(t, resources, "CustomResourceDefinition", "convertedmodules.components.platform.opendatahub.io")
	g.Expect(crd.Object).To(SatisfyAll(
		jq.Match(`.spec.conversion.webhook.clientConfig.service.namespace == "opendatahub"`),
		jq.Match(`.metadata | has("namespace") | not`),
	))
}

// TestRenderZeroOptionsReproduceNoOptionRender proves an empty
// RenderOptions preset is the identity over the option machinery:
// the offline lint output is reproduced exactly, namespace-less, for
// both backends.
func TestRenderZeroOptionsReproduceNoOptionRender(t *testing.T) {
	t.Parallel()

	t.Run("helm", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeHelmChartFixture(t, root)

		definition := loadDefinition(t, root)
		definitionFile := filepath.Join(root, "definition.yaml")

		offline, err := Render(t.Context(), definition, definitionFile)
		g.Expect(err).NotTo(HaveOccurred())

		zeroOptions, err := Render(t.Context(), definition, definitionFile, RenderOptions{})
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(zeroOptions).To(Equal(offline))
	})

	t.Run("kustomize", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeKustomizeFixture(t, root)

		definition := loadDefinition(t, root)
		definitionFile := filepath.Join(root, "definition.yaml")

		offline, err := Render(t.Context(), definition, definitionFile)
		g.Expect(err).NotTo(HaveOccurred())

		zeroOptions, err := Render(t.Context(), definition, definitionFile, RenderOptions{})
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(zeroOptions).To(Equal(offline))

		for _, resource := range zeroOptions {
			g.Expect(resource.Object).To(SatisfyAll(
				jq.Match(`.metadata | has("namespace") | not`),
			))
		}
	})
}

// TestRenderErrorCarriesFormatAndModuleContext proves render failures
// name the format and the module in their error text while keeping
// the cause reachable.
func TestRenderErrorCarriesFormatAndModuleContext(t *testing.T) {
	t.Parallel()

	t.Run("helm", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeHelmChartFixture(t, root)
		// The framework action is strict: values must satisfy the
		// chart's values.schema.json, validated offline.
		writeModuleFile(t, root, "manifests/chart/values.yaml", "replicas: 0\n")

		resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
		g.Expect(err).To(HaveOccurred())
		g.Expect(resources).To(BeNil())
		g.Expect(err.Error()).To(ContainSubstring("helm render for module synthetic"))
	})

	t.Run("kustomize", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeKustomizeFixture(t, root)
		// A well-formed reference the engine cannot build.
		writeModuleFile(t, root, "manifests/overlays/development/deployment.yaml", "not: [valid yaml\n")

		resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
		g.Expect(err).To(HaveOccurred())
		g.Expect(resources).To(BeNil())
		g.Expect(err.Error()).To(ContainSubstring("kustomize render for module synthetic"))
	})
}

// TestRenderCoLocatedExamples render the repository's own co-located
// examples through the public service, proving the documented layouts
// keep working: the example renders its full controller package in
// apply order — CRD, ServiceAccount, Role, RoleBinding, then the
// Deployment — with no renderer-added metadata.
func TestRenderCoLocatedExamples(t *testing.T) {
	t.Parallel()

	t.Run("helm", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definitionFile := filepath.Join("..", "..", "..", "examples", "helm", "chart", "module.yaml")

		definition, err := loader.LoadFromPath(t.Context(), definitionFile)
		g.Expect(err).NotTo(HaveOccurred())

		resources, err := Render(t.Context(), definition, definitionFile)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(resourceKinds(resources)).To(Equal([]string{
			"CustomResourceDefinition",
			"ServiceAccount",
			"Role",
			"RoleBinding",
			"Deployment",
		}))

		assertNoRendererMetadata(t, resources)
	})

	t.Run("kustomize", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definitionFile := filepath.Join("..", "..", "..", "examples", "kustomize", "module.yaml")

		definition, err := loader.LoadFromPath(t.Context(), definitionFile)
		g.Expect(err).NotTo(HaveOccurred())

		resources, err := Render(t.Context(), definition, definitionFile)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(resourceKinds(resources)).To(Equal([]string{
			"CustomResourceDefinition",
			"ServiceAccount",
			"Role",
			"RoleBinding",
			"Deployment",
		}))

		assertNoRendererMetadata(t, resources)
	})
}

// TestRenderAppliesOrderBeforeCallerPostRenderers proves the shared
// engine registers the library apply-order post-renderer before
// caller-supplied post-renderers, exactly once, for both backends:
// the sentinel sees the whole batch once, in apply-order sequence.
func TestRenderAppliesOrderBeforeCallerPostRenderers(t *testing.T) {
	t.Parallel()

	t.Run("helm", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
		writeModuleFile(t, root, "manifests/chart/Chart.yaml", helmChartYAML)
		writeModuleFile(t, root, "manifests/chart/values.yaml", helmValuesYAML)
		writeModuleFile(t, root, "manifests/chart/templates/cm-beta.yaml", sharedBetaConfigMapYAML)
		writeModuleFile(t, root, "manifests/chart/templates/cm-alpha.yaml", sharedAlphaConfigMapYAML)
		writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", helmTemplateYAML)

		calls := 0
		var order []string

		sentinel := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			calls++

			for i := range objects {
				order = append(order, objects[i].GetNamespace()+"/"+objects[i].GetName())
			}

			return objects, nil
		}

		resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"),
			WithPostRenderer(sentinel))
		g.Expect(err).NotTo(HaveOccurred())

		// The sentinel ran exactly once, after the apply-order
		// post-renderer: the ConfigMaps arrive namespace-sorted ahead
		// of the Deployment, the order the old framework returned.
		g.Expect(calls).To(Equal(1))
		g.Expect(order).To(Equal([]string{"alpha/shared", "beta/shared", "/synthetic-controller"}))
		g.Expect(resources).To(HaveLen(3))
	})

	t.Run("kustomize", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeKustomizeFixture(t, root)

		calls := 0
		var kinds []string

		sentinel := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			calls++

			for i := range objects {
				kinds = append(kinds, objects[i].GetKind())
			}

			return objects, nil
		}

		resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"),
			WithPostRenderer(sentinel))
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(calls).To(Equal(1))

		// Apply-order sequence: CRD first, ConfigMap ahead of the
		// Deployment it follows in the entry, whatever the
		// kustomization order was.
		g.Expect(kinds[0]).To(Equal("CustomResourceDefinition"))
		g.Expect(kinds).To(ContainElements("Deployment", "Service", "ConfigMap"))
		g.Expect(resources).NotTo(BeEmpty())
	})
}

// TestRenderCancellationBetweenProcessors proves an expired context
// stops the engine chain between processors for both backends: a
// caller post-renderer that cancels the context prevents every later
// processor from running, and the render never returns successful
// output. The synchronous native build itself is not interrupted —
// only the chain around it is cooperative.
func TestRenderCancellationBetweenProcessors(t *testing.T) {
	t.Parallel()

	renderCase := func(t *testing.T, writeFixture func(t *testing.T, root string)) {
		t.Helper()

		g := NewWithT(t)

		root := t.TempDir()
		writeFixture(t, root)

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		secondRan := false

		cancelNow := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			cancel()

			return objects, nil
		}

		never := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			secondRan = true

			return objects, nil
		}

		resources, err := Render(ctx, loadDefinition(t, root), filepath.Join(root, "definition.yaml"),
			WithPostRenderer(cancelNow),
			WithPostRenderer(never))
		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		g.Expect(secondRan).To(BeFalse())
		g.Expect(resources).To(BeNil())
	}

	t.Run("helm", func(t *testing.T) {
		t.Parallel()

		renderCase(t, writeHelmChartFixture)
	})

	t.Run("kustomize", func(t *testing.T) {
		t.Parallel()

		renderCase(t, writeKustomizeFixture)
	})
}

// contractResource builds one minimal unstructured object.
func contractResource(kind string, namespace string, name string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}}
}

// writeContractChartFixture writes a Helm module rendering one
// ordinary ConfigMap, the enrichment target of the contract tests.
func writeContractChartFixture(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", helmChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", helmValuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/configmap.yaml", guardOrdinaryConfigMapTemplateYAML)
}

// TestRenderGuardRejectsCallerEnrichedReservedIdentities proves the
// guard runs after caller enrichment: a post-renderer that appends a
// reserved identity — or rewrites an ordinary rendered object into
// one — fails the whole render with the sentinel and a nil batch.
// Reserved objects are never filtered out of successful output.
func TestRenderGuardRejectsCallerEnrichedReservedIdentities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
	}{
		{"module instance at another version"},
		{"platform module definition"},
		{"reserved definition CRD"},
	}

	documents := map[string]string{
		"module instance at another version": guardModuleInstanceTemplateYAML,
		"platform module definition":         guardDefinitionTemplateYAML,
		"reserved definition CRD":            guardReservedCRDTemplateYAML,
	}

	modes := []struct {
		name string
		// enrich returns the batch a caller post-renderer produces
		// from the rendered objects and the case's reserved document.
		enrich func(t *testing.T, document unstructured.Unstructured, objects []unstructured.Unstructured) []unstructured.Unstructured
	}{
		{
			name: "appended by the caller",
			enrich: func(t *testing.T, document unstructured.Unstructured, objects []unstructured.Unstructured) []unstructured.Unstructured {
				t.Helper()

				return append(objects, document)
			},
		},
		{
			name: "rewritten from an ordinary rendered object",
			enrich: func(t *testing.T, document unstructured.Unstructured, _ []unstructured.Unstructured) []unstructured.Unstructured {
				t.Helper()

				// The whole batch is replaced: the ordinary rendered
				// objects are gone and only the reserved document —
				// whose own name and identity are kept — remains.
				return []unstructured.Unstructured{document}
			},
		},
	}

	for _, mode := range modes {
		for _, testCase := range cases {
			t.Run(mode.name+" / "+testCase.name, func(t *testing.T) {
				t.Parallel()

				g := NewWithT(t)

				root := t.TempDir()
				writeContractChartFixture(t, root)

				definition := loadDefinition(t, root)
				definitionFile := filepath.Join(root, "definition.yaml")

				document := guardResource(t, documents[testCase.name])

				enricher := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
					return mode.enrich(t, document, objects), nil
				}

				resources, err := Render(t.Context(), definition, definitionFile, WithPostRenderer(enricher))

				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.Is(err, ErrReservedResource)).To(BeTrue())
				g.Expect(resources).To(BeNil())
			})
		}
	}
}

// TestRenderGuardAllowsCallerAppendedOrdinaryResources proves caller
// enrichment with ordinary resources still succeeds and returns the
// enriched batch: the guard rejects only reserved identities.
func TestRenderGuardAllowsCallerAppendedOrdinaryResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	enricher := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		return append(objects, contractResource("ConfigMap", "modules", "caller-added")), nil
	}

	resources, err := Render(t.Context(), definition, definitionFile, WithPostRenderer(enricher))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(findResource(t, resources, "ConfigMap", "module-config")).NotTo(BeZero())
	g.Expect(findResource(t, resources, "ConfigMap", "caller-added")).NotTo(BeZero())
}

// TestRenderExpiredDeadlinePreventsEveryCallback proves an
// already-expired deadline fails the render before any backend or
// caller processor runs, keeping the DeadlineExceeded identity and a
// nil batch.
func TestRenderExpiredDeadlinePreventsEveryCallback(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)

	ran := false
	observe := func(context.Context, []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		ran = true

		return nil, nil
	}

	resources, err := Render(ctx, definition, definitionFile, WithPostRenderer(observe))

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(resources).To(BeNil())
	g.Expect(ran).To(BeFalse())
}

// TestRenderCancellationAtFinalProcessorNeverPublishesOutput proves
// the final context check: a last caller post-renderer that cancels
// the context lets the processor chain complete, but the render never
// becomes successful output — the failure keeps the cancellation
// identity and returns a nil batch after every processor ran once.
func TestRenderCancellationAtFinalProcessorNeverPublishesOutput(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	calls := 0

	count := func(context.Context, []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		calls++

		return nil, nil
	}

	cancelNow := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		cancel()

		return objects, nil
	}

	resources, err := Render(ctx, definition, definitionFile,
		WithPostRenderer(count),
		WithPostRenderer(count),
		WithPostRenderer(cancelNow),
	)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	g.Expect(resources).To(BeNil())
	g.Expect(calls).To(Equal(2))
}

// TestRenderProcessorErrorWithPartialOutput proves a caller processor
// that both enriches the batch and fails aborts the render with its
// own error identity: the partial output is never returned, and no
// later callback runs.
func TestRenderProcessorErrorWithPartialOutput(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	processorFailure := errors.New("processor failure")

	laterRan := false

	failing := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		return append(objects, contractResource("ConfigMap", "modules", "partial")), processorFailure
	}

	never := func(context.Context, []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		laterRan = true

		return nil, nil
	}

	resources, err := Render(t.Context(), definition, definitionFile,
		WithPostRenderer(failing),
		WithPostRenderer(never),
	)

	g.Expect(err).To(MatchError(processorFailure))
	g.Expect(resources).To(BeNil())
	g.Expect(laterRan).To(BeFalse())
}

// TestExecuteDoesNotResortAfterCallerEnrichment proves no extra final
// sort exists behind the engine: a caller processor appends an
// ordinary object that ApplyOrder would sort ahead of the existing
// output, and the returned batch keeps it appended — the framework
// deploy action, not the render, owns the final ordering.
func TestExecuteDoesNotResortAfterCallerEnrichment(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	// optsTestRenderer's batch sorts, under ApplyOrder, to
	// alpha/shared, beta/shared, then the namespace-less Deployment.
	appendSortAhead := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		// alpha/aaa sorts ahead of every existing object under
		// ApplyOrder: a final sort would move it to the front.
		return append(objects, optsTestObject("ConfigMap", "alpha", "aaa")), nil
	}

	resources, err := execute(t.Context(), guardDefinition(), optsTestRenderer{}, RenderOptions{
		PostRenderers: []enginetypes.PostRenderer{appendSortAhead},
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resources).To(HaveLen(4))

	// The appended object stays appended: the sorted prefix is
	// untouched and the enrichment remains at the end.
	g.Expect(resources[0].GetNamespace() + "/" + resources[0].GetName()).To(Equal("alpha/shared"))
	g.Expect(resources[1].GetNamespace() + "/" + resources[1].GetName()).To(Equal("beta/shared"))
	g.Expect(resources[2].GetNamespace() + "/" + resources[2].GetName()).To(Equal("/synthetic-controller"))
	g.Expect(resources[3].GetNamespace() + "/" + resources[3].GetName()).To(Equal("alpha/aaa"))
}

// TestRenderIgnoresNilOptionsThroughRealBackend proves nil interface
// and typed nil functional options are ignored through the public
// service over a real backend: the render output is identical to the
// no-option call.
func TestRenderIgnoresNilOptionsThroughRealBackend(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	plain, err := Render(t.Context(), definition, definitionFile)
	g.Expect(err).NotTo(HaveOccurred())

	withNils, err := Render(t.Context(), definition, definitionFile,
		nil,
		option.FunctionalOption[RenderOptions](nil),
	)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(withNils).To(Equal(plain))
}

// TestRenderNamespaceOptionLastWins proves the namespace option
// replaces, not accumulates: through the real Helm backend the last
// namespace option becomes the release namespace a template renders
// into.
func TestRenderNamespaceOptionLastWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", helmNamespacedTemplateYAML)

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"),
		WithNamespace("first"),
		WithNamespace("second"),
	)
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	namespace, found, err := unstructured.NestedString(deployment.Object, "metadata", "namespace")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(namespace).To(Equal("second"))
}

// TestRenderPostRendererOptionsAppendInOrder proves caller
// post-renderers apply in option order through the public service:
// the first registered processor observes the batch before the
// second, exactly once each, after the engine's apply-order
// post-renderer.
func TestRenderPostRendererOptionsAppendInOrder(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	var calls []string

	named := func(name string) enginetypes.PostRenderer {
		return func(context.Context, []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			calls = append(calls, name)

			return nil, nil
		}
	}

	_, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"),
		WithPostRenderer(named("first")),
		WithPostRenderer(named("second")),
	)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(calls).To(Equal([]string{"first", "second"}))
}

// TestRenderPresetDoesNotLeakCallerAppends proves a preset passed
// through the public service clones its processor slice: the caller
// filling its own spare capacity after the render can never observe
// or overwrite the option's processor.
func TestRenderPresetDoesNotLeakCallerAppends(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	var renderCalls []string
	var callerCalls []string

	named := func(calls *[]string, name string) enginetypes.PostRenderer {
		return func(context.Context, []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			*calls = append(*calls, name)

			return nil, nil
		}
	}

	// One processor with spare capacity behind it, applied as a
	// whole-preset option.
	callerSlice := make([]enginetypes.PostRenderer, 1, 2)
	callerSlice[0] = named(&renderCalls, "preset")

	_, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"),
		RenderOptions{PostRenderers: callerSlice},
		WithPostRenderer(named(&renderCalls, "option")),
	)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(renderCalls).To(Equal([]string{"preset", "option"}))

	// The preset cloned its processor slice: the render's own
	// "option" append never wrote into the caller's spare capacity,
	// so the slot behind the caller's length is still empty. An
	// aliased preset would have left the "option" processor there.
	g.Expect(callerSlice[:cap(callerSlice)][1]).To(BeNil())

	// The caller keeps filling its own spare capacity after the
	// render: its own processor runs, and the render's recorded
	// calls are untouched.
	callerSlice = append(callerSlice, named(&callerCalls, "caller-second"))

	_, _ = callerSlice[1](t.Context(), nil)
	g.Expect(callerCalls).To(Equal([]string{"caller-second"}))
	g.Expect(renderCalls).To(Equal([]string{"preset", "option"}))
}
