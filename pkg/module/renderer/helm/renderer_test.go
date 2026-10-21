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

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// Module and chart inputs exercised through the real framework action.
const (
	definitionYAML = `apiVersion: config.opendatahub.io/v1
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
`

	definitionWithEnvYAML = `apiVersion: config.opendatahub.io/v1
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
        image: quay.io/example/controller:v2
        env:
        - name: LOG_LEVEL
          value: debug
`

	chartYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- name: vendored
  version: 1.2.3
  repository: https://charts.example.com
`

	chartWithMissingDependencyYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- name: absent
  version: 1.0.0
`

	valuesYAML = `replicas: 1
`

	valuesSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "replicas": {"type": "integer", "minimum": 1}
  }
}
`

	templateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-controller
spec:
  replicas: {{ .Values.replicas }}
`

	vendoredTemplateYAML = `apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-service
`

	vendoredChartYAML = `apiVersion: v2
name: vendored
version: 1.2.3
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

// loadDefinition loads the module definition through the real loader.
func loadDefinition(t *testing.T, root string) config.Document {
	t.Helper()

	g := NewWithT(t)

	document, err := loader.New().Load(t.Context(), filepath.Join(root, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	return document
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

// findResource returns the rendered object of the given kind and name.
func findResource(t *testing.T, resources []unstructured.Unstructured, kind, name string) unstructured.Unstructured {
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

	resources, err := Render(t.Context(), loadDefinition(t, root))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resources).NotTo(BeEmpty())

	// The release name is the module name and chart values map from
	// the captured defaults.
	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(1)))

	// Vendored dependency templates render from the snapshot archive.
	g.Expect(findResource(t, resources, "Service", "synthetic-service")).NotTo(BeZero())
}

func TestRenderHelmRejectsInvalidValuesAgainstSchema(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)
	// The framework action is strict: values must satisfy the chart's
	// values.schema.json, validated offline.
	writeModuleFile(t, root, "manifests/chart/values.yaml", "replicas: 0\n")

	resources, err := Render(t.Context(), loadDefinition(t, root))
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

	first, err := Render(t.Context(), loadDefinition(t, withDescriptor))
	g.Expect(err).NotTo(HaveOccurred())

	second, err := Render(t.Context(), loadDefinition(t, withoutDescriptor))
	g.Expect(err).NotTo(HaveOccurred())

	// Descriptor images and env entries never reach rendering: no
	// process environment is read and the output is identical.
	g.Expect(first).To(Equal(second))
}

func TestRenderHelmIsStatelessAcrossCalls(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	first := t.TempDir()
	writeChartFixture(t, first)

	second := t.TempDir()
	writeChartFixture(t, second)
	writeModuleFile(t, second, "definition.yaml", strings.Replace(definitionYAML, "name: synthetic", "name: other", 1))

	firstOutput, err := Render(t.Context(), loadDefinition(t, first))
	g.Expect(err).NotTo(HaveOccurred())

	_, err = Render(t.Context(), loadDefinition(t, second))
	g.Expect(err).NotTo(HaveOccurred())

	repeated, err := Render(t.Context(), loadDefinition(t, first))
	g.Expect(err).NotTo(HaveOccurred())

	// Repeated renders of the same module are identical: no state
	// leaks between calls.
	g.Expect(repeated).To(Equal(firstOutput))
}

func TestRenderHelmRejectsUnsafeSources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithMissingDependencyYAML)

	resources, err := Render(t.Context(), loadDefinition(t, root))
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
	g.Expect(errors.Is(err, renderer.ErrUnsafeSource)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("helm backend for module synthetic"))
}

func TestRenderHelmPropagatesCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	resources, err := Render(ctx, loadDefinition(t, root))
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
}
