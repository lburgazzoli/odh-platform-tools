package kustomize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
)

// Module and kustomization inputs exercised through the real framework
// action.
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
    format: kustomize
    kustomize:
      path: ./manifests/overlays/development
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

	overlayKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
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

	baseKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- service.yaml
`

	deploymentResourceYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: module-controller
spec:
  replicas: 1
`

	serviceResourceYAML = `apiVersion: v1
kind: Service
metadata:
  name: module-service
`

	patchYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: module-controller
spec:
  replicas: 2
`

	paramsEnv = `LOG_LEVEL=info
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

// writeKustomizeFixture writes the standard Kustomize module tree:
// an overlay with direct sibling base, patch, and generator inputs.
func writeKustomizeFixture(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", overlayKustomizationYAML)
	writeModuleFile(t, root, "manifests/overlays/development/deployment.yaml", deploymentResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/patch.yaml", patchYAML)
	writeModuleFile(t, root, "manifests/overlays/development/params.env", paramsEnv)
	writeModuleFile(t, root, "manifests/base/kustomization.yaml", baseKustomizationYAML)
	writeModuleFile(t, root, "manifests/base/service.yaml", serviceResourceYAML)
}

// findResource returns the rendered object of the given kind whose
// name has the given prefix.
func findResource(
	t *testing.T,
	resources []unstructured.Unstructured,
	kind, name string,
) unstructured.Unstructured {
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
	kind, prefix string,
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

func TestRenderKustomize(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	resources, err := Render(t.Context(), loadDefinition(t, root))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resources).NotTo(BeEmpty())

	// Direct resource files, sibling bases, patches, and generators all
	// load with engine restrictions off, as in a plain kustomize build.
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

func TestRenderKustomizeYmlExtension(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	// The same overlay, authored as kustomization.yml, renders through
	// the entry file name wiring instead of the engine default lookup.
	if err := os.Rename(
		filepath.Join(root, "manifests/overlays/development/kustomization.yaml"),
		filepath.Join(root, "manifests/overlays/development/kustomization.yml"),
	); err != nil {
		t.Fatal(err)
	}

	resources, err := Render(t.Context(), loadDefinition(t, root))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(findResource(t, resources, "Service", "module-service")).NotTo(BeZero())
}

func TestRenderKustomizeIgnoresControllerDescriptor(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	withDescriptor := t.TempDir()
	writeKustomizeFixture(t, withDescriptor)
	writeModuleFile(t, withDescriptor, "definition.yaml", definitionWithEnvYAML)

	withoutDescriptor := t.TempDir()
	writeKustomizeFixture(t, withoutDescriptor)

	first, err := Render(t.Context(), loadDefinition(t, withDescriptor))
	g.Expect(err).NotTo(HaveOccurred())

	second, err := Render(t.Context(), loadDefinition(t, withoutDescriptor))
	g.Expect(err).NotTo(HaveOccurred())

	// Descriptor images and env entries never reach rendering.
	g.Expect(first).To(Equal(second))
}

func TestRenderKustomizeIsStatelessAcrossCalls(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	first := t.TempDir()
	writeKustomizeFixture(t, first)

	second := t.TempDir()
	writeKustomizeFixture(t, second)
	writeModuleFile(t, second, "definition.yaml", strings.Replace(definitionYAML, "name: synthetic", "name: other", 1))

	firstOutput, err := Render(t.Context(), loadDefinition(t, first))
	g.Expect(err).NotTo(HaveOccurred())

	_, err = Render(t.Context(), loadDefinition(t, second))
	g.Expect(err).NotTo(HaveOccurred())

	repeated, err := Render(t.Context(), loadDefinition(t, first))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(repeated).To(Equal(firstOutput))
}

func TestRenderKustomizeRenderError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)
	// A well-formed reference the engine cannot build.
	writeModuleFile(t, root, "manifests/overlays/development/deployment.yaml", "not: [valid yaml\n")

	resources, err := Render(t.Context(), loadDefinition(t, root))
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
	g.Expect(err.Error()).To(ContainSubstring("kustomize render for module synthetic"))
}

func TestRenderKustomizePropagatesCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	resources, err := Render(ctx, loadDefinition(t, root))
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
}
