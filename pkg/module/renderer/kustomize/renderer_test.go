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

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// definitionYAML is the valid Kustomize module definition.
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
    format: kustomize
    kustomize:
      path: ./manifests/overlays/development
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

// definitionWithEnvYAML is the Kustomize module definition with
// controller descriptor env entries.
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

// coLocatedDefinitionYAML selects the Kustomize backend with no
// kustomize block: the entry defaults to the definition file's own
// directory.
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
    format: kustomize
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

// coLocatedKustomizationYAML is a self-contained entry with one
// direct resource.
const coLocatedKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
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

// writeKustomizeFixture writes the standard Kustomize module tree:
// an overlay with direct sibling base, patch, and generator inputs.
func writeKustomizeFixture(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", overlayKustomizationYAML)
	writeModuleFile(t, root, "manifests/overlays/development/deployment.yaml", deploymentResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/rolebinding.yaml", roleBindingResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/crd.yaml", clusterScopedResourceYAML)
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
	kind string,
	name string,
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

func TestRenderKustomize(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
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

// TestRenderKustomizeCoLocated proves a definition that omits the
// kustomize block renders the entry co-located with the definition
// file through the real engine: kustomization.yaml, resources, and
// the definition file share one directory.
func TestRenderKustomizeCoLocated(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "kustomization.yaml", coLocatedKustomizationYAML)
	writeModuleFile(t, root, "deployment.yaml", deploymentResourceYAML)
	writeModuleFile(t, root, "module.yaml", coLocatedDefinitionYAML)

	resources, err := Render(t.Context(), loadDefinitionFile(t, root, "module.yaml"), filepath.Join(root, "module.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "module-controller")
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(1)))
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

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
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

	first, err := Render(t.Context(), loadDefinition(t, withDescriptor), filepath.Join(withDescriptor, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	second, err := Render(t.Context(), loadDefinition(t, withoutDescriptor), filepath.Join(withoutDescriptor, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	// Descriptor images and env entries never reach rendering.
	g.Expect(first).To(Equal(second))
}

func TestRenderKustomizeIgnoresInstanceName(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	withoutName := t.TempDir()
	writeKustomizeFixture(t, withoutName)

	withName := t.TempDir()
	writeKustomizeFixture(t, withName)
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

func TestRenderKustomizeIsStatelessAcrossCalls(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	first := t.TempDir()
	writeKustomizeFixture(t, first)

	second := t.TempDir()
	writeKustomizeFixture(t, second)
	writeModuleFile(t, second, "definition.yaml", strings.Replace(definitionYAML, "name: synthetic", "name: other", 1))

	firstOutput, err := Render(t.Context(), loadDefinition(t, first), filepath.Join(first, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	_, err = Render(t.Context(), loadDefinition(t, second), filepath.Join(second, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	repeated, err := Render(t.Context(), loadDefinition(t, first), filepath.Join(first, "definition.yaml"))
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

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
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

	resources, err := Render(ctx, loadDefinition(t, root), filepath.Join(root, "definition.yaml"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
}

func TestRenderKustomizeWithOptionsAppliesNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	resources, err := RenderWithOptions(t.Context(), definition, definitionFile, renderer.Options{
		Namespace: "opendatahub",
	})
	g.Expect(err).NotTo(HaveOccurred())

	// The engine's real namespace transformer applies the options'
	// namespace to namespaced resources, including generated ones.
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

	// RoleBinding subjects receive the namespace.
	roleBinding := findResource(t, resources, "RoleBinding", "module-rolebinding")
	g.Expect(roleBinding.Object).To(SatisfyAll(
		jq.Match(`.subjects[0].namespace == "opendatahub"`),
	))

	// Cluster-scoped objects keep their scope.
	crd := findResource(
		t, resources, "CustomResourceDefinition", "syntheticmodules.components.platform.opendatahub.io")
	g.Expect(crd.Object).To(SatisfyAll(
		jq.Match(`.metadata | has("namespace") | not`),
	))
}

func TestRenderKustomizeZeroOptionsReproduceOfflineRender(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	offline, err := Render(t.Context(), definition, definitionFile)
	g.Expect(err).NotTo(HaveOccurred())

	zeroOptions, err := RenderWithOptions(t.Context(), definition, definitionFile, renderer.Options{})
	g.Expect(err).NotTo(HaveOccurred())

	// Render is the zero-option adapter over the same implementation:
	// the offline lint output is reproduced exactly, namespace-less.
	g.Expect(zeroOptions).To(Equal(offline))

	for _, resource := range zeroOptions {
		g.Expect(resource.Object).To(SatisfyAll(
			jq.Match(`.metadata | has("namespace") | not`),
		))
	}
}
