package helm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
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

// TestNewHelmChartRendersFromConfiguredPath proves the native
// constructor renders the configured chart directory through Process:
// the release name is the module name, chart values map from the chart
// defaults, and vendored dependency templates render from the chart's
// charts/ archive.
func TestNewHelmChartRendersFromConfiguredPath(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)

	native, err := New(loadDefinition(t, root), filepath.Join(root, "definition.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(1)))

	g.Expect(findResource(t, resources, "Service", "synthetic-service")).NotTo(BeZero())
}

// TestNewHelmCoLocatedChart proves a definition that omits the helm
// block constructs a renderer over the chart co-located with the
// definition file: the entry defaults to the definition file's own
// directory, and the chart loader carries the definition file as
// inert chart content.
func TestNewHelmCoLocatedChart(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeCoLocatedChartFixture(t, root, coLocatedDefinitionYAML)

	native, err := New(loadDefinitionFile(t, root, "module.yaml"), filepath.Join(root, "module.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(1)))
}

// TestNewHelmDefinitionValues proves authored literal values merge
// over the chart defaults with operator-identical precedence — chart
// default 1, definition value 2 — through the native renderer's own
// lazy values function.
func TestNewHelmDefinitionValues(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeCoLocatedChartFixture(t, root, definitionWithValuesYAML)

	native, err := New(loadDefinitionFile(t, root, "module.yaml"), filepath.Join(root, "module.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(2)))
}

// TestNewHelmRejectsInvalidValuesAgainstSchema proves the strict
// native configuration: values must satisfy the chart's
// values.schema.json, validated offline at Process time.
func TestNewHelmRejectsInvalidValuesAgainstSchema(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)
	writeModuleFile(t, root, "manifests/chart/values.yaml", "replicas: 0\n")

	native, err := New(loadDefinition(t, root), filepath.Join(root, "definition.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
}

// TestNewHelmStrictMissingKey proves the constructor keeps the old
// framework action's strict template mode: a template referencing an
// undefined value fails the render.
func TestNewHelmStrictMissingKey(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/configmap.yaml", strictMissingKeyTemplateYAML)

	native, err := New(loadDefinition(t, root), filepath.Join(root, "definition.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
}

// TestNewHelmAddsNoRendererMetadata proves the explicit
// ContentHash=false and SourceAnnotations=false construction flags
// preserve the old framework action's effective output: neither chart
// templates nor bundled chart CRDs gain a content hash or source
// annotation.
func TestNewHelmAddsNoRendererMetadata(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", templateYAML)
	writeModuleFile(t, root, "manifests/chart/crds/syntheticmodules.yaml", crdFileYAML)

	native, err := New(loadDefinition(t, root), filepath.Join(root, "definition.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())

	// The bundled CRD is part of the rendered output.
	g.Expect(findResource(t, resources, "CustomResourceDefinition", "syntheticmodules.components.platform.opendatahub.io")).NotTo(BeZero())
	g.Expect(findResource(t, resources, "Deployment", "synthetic-controller")).NotTo(BeZero())

	assertNoRendererMetadata(t, resources)
}

// TestDecodeHelmValues proves authored literal chart values decode to
// the map the Helm renderer consumes: every JSON kind survives,
// including a null entry whose apiextensions JSON representation has
// empty Raw, and placeholder-looking text stays literal.
func TestDecodeHelmValues(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	values := map[string]apiextensionsv1.JSON{
		"scalar":      {Raw: []byte("2")},
		"string":      {Raw: []byte(`"literal"`)},
		"nested":      {Raw: []byte(`{"tag":"v1.0.0"}`)},
		"list":        {Raw: []byte(`["a","b"]`)},
		"boolean":     {Raw: []byte("true")},
		"nullValue":   {}, // apiextensions JSON represents null with empty Raw.
		"placeholder": {Raw: []byte(`"{{RELATED_IMAGE_EXAMPLE_CONTROLLER}}"`)},
	}

	decoded, err := decodeHelmValues(values)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(decoded).To(SatisfyAll(
		HaveKeyWithValue("scalar", float64(2)),
		HaveKeyWithValue("string", "literal"),
		HaveKeyWithValue("nested", map[string]any{"tag": "v1.0.0"}),
		HaveKeyWithValue("list", []any{"a", "b"}),
		HaveKeyWithValue("boolean", true),
		// Empty Raw is JSON null, not malformed content: it decodes
		// as a nil value instead of failing with EOF.
		HaveKeyWithValue("nullValue", BeNil()),
		// Placeholder-looking text stays literal.
		HaveKeyWithValue("placeholder", "{{RELATED_IMAGE_EXAMPLE_CONTROLLER}}"),
	))

	// The source values are not mutated.
	g.Expect(values["nullValue"].Raw).To(BeEmpty())
	g.Expect(values["scalar"].Raw).To(Equal([]byte("2")))
}

// TestDecodeHelmValuesRejectsMalformedEntry proves a malformed
// authored entry is reported rather than dropped.
func TestDecodeHelmValuesRejectsMalformedEntry(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	values := map[string]apiextensionsv1.JSON{
		"malformed": {Raw: []byte(`{"tag":`)},
	}

	decoded, err := decodeHelmValues(values)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(`helm value "malformed"`))
	g.Expect(decoded).To(BeNil())
}
