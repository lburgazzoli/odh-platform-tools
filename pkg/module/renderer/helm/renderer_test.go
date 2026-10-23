package helm

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

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// definitionYAML is the valid Helm module definition.
const definitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
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

// definitionWithEnvYAML is the Helm module definition with controller
// descriptor env entries.
const definitionWithEnvYAML = `apiVersion: config.opendatahub.io/v1alpha1
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

// definitionWithInstanceNameYAML sets an explicit instance name
// different from the module name.
const definitionWithInstanceNameYAML = `apiVersion: config.opendatahub.io/v1alpha1
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

// coLocatedDefinitionYAML selects the Helm backend with no helm
// block: the entry defaults to the definition file's own directory.
const coLocatedDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
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

// definitionWithValuesYAML authors literal chart values with an
// omitted path: the entry defaults to the definition file's own
// directory and the values merge over the chart defaults.
const definitionWithValuesYAML = `apiVersion: config.opendatahub.io/v1alpha1
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

// chartYAML declares a chart with a vendored dependency.
const chartYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- name: vendored
  version: 1.2.3
  repository: https://charts.example.com
`

// valuesYAML is the chart default values.
const valuesYAML = `replicas: 1
`

// valuesSchemaJSON constrains the chart values.
const valuesSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "replicas": {"type": "integer", "minimum": 1}
  }
}
`

// templateYAML is the chart's namespace-less deployment template.
const templateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-controller
spec:
  replicas: {{ .Values.replicas }}
`

// namespacedTemplateYAML is the chart's deployment template with the
// namespace templated from the release.
const namespacedTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-controller
  namespace: {{ .Release.Namespace }}
spec:
  replicas: {{ .Values.replicas }}
`

// vendoredTemplateYAML is the vendored dependency's namespace-less
// service template.
const vendoredTemplateYAML = `apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-service
`

// vendoredChartYAML declares the vendored dependency chart.
const vendoredChartYAML = `apiVersion: v2
name: vendored
version: 1.2.3
`

// crdFileYAML is a bundled chart CRD from the chart's crds/
// directory.
const crdFileYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: syntheticmodules.components.platform.opendatahub.io
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

// valuesKindsTemplateYAML renders the authored value kinds into
// ConfigMap data.
const valuesKindsTemplateYAML = `apiVersion: v1
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

// valuesKindsDefinitionYAML authors literal chart values of every
// JSON kind, including a null entry.
const valuesKindsDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
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

// strictMissingKeyTemplateYAML references a value the chart does not
// define.
const strictMissingKeyTemplateYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: strict-cm
data:
  value: {{ .Values.missing }}
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

// writeChartFixture writes the standard Helm module tree.
func writeChartFixture(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/values.schema.json", valuesSchemaJSON)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", templateYAML)

	archive := buildArchive(t, map[string]string{
		"vendored/Chart.yaml":         vendoredChartYAML,
		"vendored/templates/svc.yaml": vendoredTemplateYAML,
	})
	writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))
}

// writeCoLocatedChartFixture writes the chart with the definition
// file inside it, next to Chart.yaml.
func writeCoLocatedChartFixture(t *testing.T, root string, definitionContent string) {
	t.Helper()

	writeModuleFile(t, root, "Chart.yaml", chartYAML)
	writeModuleFile(t, root, "values.yaml", valuesYAML)
	writeModuleFile(t, root, "values.schema.json", valuesSchemaJSON)
	writeModuleFile(t, root, "templates/deployment.yaml", templateYAML)
	writeModuleFile(t, root, "module.yaml", definitionContent)
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

func TestRenderHelmChart(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resources).NotTo(BeEmpty())

	// The release name is the module name and chart values map from
	// the chart defaults.
	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(1)))

	// Vendored dependency templates render from the chart's charts/ archive.
	g.Expect(findResource(t, resources, "Service", "synthetic-service")).NotTo(BeZero())
}

// TestRenderHelmCoLocatedChart proves a definition that omits the
// helm block renders the chart co-located with the definition file:
// the entry defaults to the definition file's own directory through
// the real engine, and the chart loader carries the definition file
// as inert chart content.
func TestRenderHelmCoLocatedChart(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeCoLocatedChartFixture(t, root, coLocatedDefinitionYAML)

	resources, err := Render(t.Context(), loadDefinitionFile(t, root, "module.yaml"), filepath.Join(root, "module.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	// The chart defaults apply: the definition authors no values.
	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(1)))
}

// TestRenderHelmDefinitionValues proves authored literal values merge
// over the chart defaults with operator-identical precedence — chart
// default 1, definition value 2 — through the real engine, with the
// omitted path defaulting to the definition file's directory.
func TestRenderHelmDefinitionValues(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeCoLocatedChartFixture(t, root, definitionWithValuesYAML)

	resources, err := Render(t.Context(), loadDefinitionFile(t, root, "module.yaml"), filepath.Join(root, "module.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(2)))
}

func TestRenderHelmRejectsInvalidValuesAgainstSchema(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)
	// The framework action is strict: values must satisfy the chart's
	// values.schema.json, validated offline.
	writeModuleFile(t, root, "manifests/chart/values.yaml", "replicas: 0\n")

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
	g.Expect(err.Error()).To(ContainSubstring("helm render for module synthetic"))
}

func TestRenderHelmIgnoresControllerDescriptor(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	withDescriptor := t.TempDir()
	writeChartFixture(t, withDescriptor)
	writeModuleFile(t, withDescriptor, "definition.yaml", definitionWithEnvYAML)

	withoutDescriptor := t.TempDir()
	writeChartFixture(t, withoutDescriptor)

	first, err := Render(t.Context(), loadDefinition(t, withDescriptor), filepath.Join(withDescriptor, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	second, err := Render(t.Context(), loadDefinition(t, withoutDescriptor), filepath.Join(withoutDescriptor, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	// Descriptor images and env entries never reach rendering: no
	// process environment is read and the output is identical.
	g.Expect(first).To(Equal(second))
}

func TestRenderHelmIgnoresInstanceName(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	withoutName := t.TempDir()
	writeChartFixture(t, withoutName)

	withName := t.TempDir()
	writeChartFixture(t, withName)
	writeModuleFile(t, withName, "definition.yaml", definitionWithInstanceNameYAML)

	omitted, err := Render(t.Context(), loadDefinition(t, withoutName), filepath.Join(withoutName, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	explicit, err := Render(t.Context(), loadDefinition(t, withName), filepath.Join(withName, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	// The configured instance name is reserved for future custom
	// resource creation and lookup: the rendered resources are
	// identical whether it is omitted or explicit.
	g.Expect(explicit).To(Equal(omitted))
}

func TestRenderHelmIsStatelessAcrossCalls(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	first := t.TempDir()
	writeChartFixture(t, first)

	second := t.TempDir()
	writeChartFixture(t, second)
	writeModuleFile(t, second, "definition.yaml", strings.Replace(definitionYAML, "name: synthetic", "name: other", 1))

	firstOutput, err := Render(t.Context(), loadDefinition(t, first), filepath.Join(first, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	_, err = Render(t.Context(), loadDefinition(t, second), filepath.Join(second, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	repeated, err := Render(t.Context(), loadDefinition(t, first), filepath.Join(first, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	// Repeated renders of the same module are identical: no state
	// leaks between calls.
	g.Expect(repeated).To(Equal(firstOutput))
}

func TestRenderHelmPropagatesCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	resources, err := Render(ctx, loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
}

func TestRenderHelmWithOptionsReleaseNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", namespacedTemplateYAML)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	resources, err := RenderWithOptions(t.Context(), definition, definitionFile, renderer.Options{
		Namespace: "opendatahub",
	})
	g.Expect(err).NotTo(HaveOccurred())

	// The options' namespace is the release namespace: a template
	// using .Release.Namespace renders into it.
	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "opendatahub"`),
	))

	// Helm does not fill metadata.namespace itself: a template that
	// does not use .Release.Namespace stays namespace-less, exactly
	// as the operator's normal deploy action would see it.
	service := findResource(t, resources, "Service", "synthetic-service")
	g.Expect(service.Object).To(SatisfyAll(
		jq.Match(`.metadata | has("namespace") | not`),
	))
}

func TestRenderHelmZeroOptionsReproduceOfflineRender(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	offline, err := Render(t.Context(), definition, definitionFile)
	g.Expect(err).NotTo(HaveOccurred())

	zeroOptions, err := RenderWithOptions(t.Context(), definition, definitionFile, renderer.Options{})
	g.Expect(err).NotTo(HaveOccurred())

	// Render is the zero-option adapter over the same implementation:
	// the offline lint output is reproduced exactly.
	g.Expect(zeroOptions).To(Equal(offline))
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

// TestRenderHelmAddsNoRendererMetadata proves the explicit
// ContentHash=false and SourceAnnotations=false options preserve the
// old framework action's effective output: neither chart templates
// nor bundled chart CRDs gain a content hash or source annotation.
func TestRenderHelmAddsNoRendererMetadata(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", templateYAML)
	writeModuleFile(t, root, "manifests/chart/crds/syntheticmodules.yaml", crdFileYAML)

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	// The bundled CRD is part of the rendered output.
	g.Expect(findResource(t, resources, "CustomResourceDefinition", "syntheticmodules.components.platform.opendatahub.io")).NotTo(BeZero())
	g.Expect(findResource(t, resources, "Deployment", "synthetic-controller")).NotTo(BeZero())

	assertNoRendererMetadata(t, resources)
}

// TestRenderHelmDefinitionValueKinds proves authored values of every
// JSON kind survive loading and lazy decoding into the render —
// including a null entry, whose apiextensions JSON representation has
// empty Raw and must decode as JSON null rather than fail with EOF.
func TestRenderHelmDefinitionValueKinds(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "Chart.yaml", chartYAML)
	writeModuleFile(t, root, "templates/configmap.yaml", valuesKindsTemplateYAML)
	writeModuleFile(t, root, "module.yaml", valuesKindsDefinitionYAML)

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

// TestRenderHelmStrictMissingKey proves the renderer keeps the old
// framework action's strict template mode: a template referencing an
// undefined value fails the render.
func TestRenderHelmStrictMissingKey(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/configmap.yaml", strictMissingKeyTemplateYAML)

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
	g.Expect(err.Error()).To(ContainSubstring("helm render for module synthetic"))
}

// TestRenderHelmAppliesOrderBeforeCallerPostRenderers proves the
// shared engine registers the library apply-order post-renderer
// before caller-supplied post-renderers, exactly once: separate Helm
// templates containing same-named ConfigMaps across namespaces arrive
// at the sentinel callback in the same order the old framework
// produced — apply-order sorted — and the sentinel sees the whole
// batch once.
func TestRenderHelmAppliesOrderBeforeCallerPostRenderers(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/cm-beta.yaml", sharedBetaConfigMapYAML)
	writeModuleFile(t, root, "manifests/chart/templates/cm-alpha.yaml", sharedAlphaConfigMapYAML)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", templateYAML)

	calls := 0
	var order []string

	sentinel := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		calls++

		for i := range objects {
			order = append(order, objects[i].GetNamespace()+"/"+objects[i].GetName())
		}

		return objects, nil
	}

	resources, err := RenderWithOptions(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"), renderer.Options{
		PostRenderers: []enginetypes.PostRenderer{sentinel},
	})
	g.Expect(err).NotTo(HaveOccurred())

	// The sentinel ran exactly once, after the apply-order
	// post-renderer: the ConfigMaps arrive namespace-sorted ahead of
	// the Deployment, the order the old framework returned.
	g.Expect(calls).To(Equal(1))
	g.Expect(order).To(Equal([]string{"alpha/shared", "beta/shared", "/synthetic-controller"}))
	g.Expect(resources).To(HaveLen(3))
}

// TestRenderHelmCancellationBetweenProcessors proves an expired
// context stops the engine chain between processors: a caller
// post-renderer that cancels the context prevents every later
// processor from running, and the render never returns successful
// output. The synchronous chart build itself is not interrupted —
// only the chain around it is cooperative.
func TestRenderHelmCancellationBetweenProcessors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)

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

	resources, err := RenderWithOptions(ctx, loadDefinition(t, root), filepath.Join(root, "definition.yaml"), renderer.Options{
		PostRenderers: []enginetypes.PostRenderer{cancelNow, never},
	})
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	g.Expect(secondRan).To(BeFalse())
	g.Expect(resources).To(BeNil())
}

// TestRenderHelmCoLocatedExample renders the repository's own
// co-located Helm example through the direct renderer and the shared
// engine, proving the documented layout keeps working after the
// engine migration.
func TestRenderHelmCoLocatedExample(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definitionFile := filepath.Join("..", "..", "..", "..", "examples", "helm", "chart", "module.yaml")

	definition, err := loader.LoadFromPath(t.Context(), definitionFile)
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := Render(t.Context(), definition, definitionFile)
	g.Expect(err).NotTo(HaveOccurred())

	// The example renders its full controller package in apply order:
	// CRD, ServiceAccount, Role, RoleBinding, then the Deployment.
	g.Expect(resourceKinds(resources)).To(Equal([]string{
		"CustomResourceDefinition",
		"ServiceAccount",
		"Role",
		"RoleBinding",
		"Deployment",
	}))

	assertNoRendererMetadata(t, resources)
}

// resourceKinds returns the rendered objects' kinds in order.
func resourceKinds(resources []unstructured.Unstructured) []string {
	kinds := make([]string, 0, len(resources))

	for i := range resources {
		kinds = append(kinds, resources[i].GetKind())
	}

	return kinds
}
