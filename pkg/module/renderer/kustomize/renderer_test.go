package kustomize

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"

	mkkustomize "github.com/k8s-manifest-kit/renderer-kustomize/pkg"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
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

// jsonKustomizationContent is JSON content in a recognized
// kustomization file name.
const jsonKustomizationContent = `{"apiVersion":"kustomize.config.k8s.io/v1beta1","kind":"Kustomization","resources":["deployment.yaml"]}
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

// TestNewKustomizeYmlExtension proves the same overlay, authored as
// kustomization.yml, renders through the entry file name wiring
// instead of the engine default lookup.
func TestNewKustomizeYmlExtension(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	if err := os.Rename(
		filepath.Join(root, "manifests/overlays/development/kustomization.yaml"),
		filepath.Join(root, "manifests/overlays/development/kustomization.yml"),
	); err != nil {
		t.Fatal(err)
	}

	native, err := New(loadDefinition(t, root), filepath.Join(root, "definition.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(findResource(t, resources, "Service", "module-service")).NotTo(BeZero())
}

// TestNewKustomizationFilename proves Kustomization — like
// kustomization.yaml and kustomization.yml — is a recognized entry
// file name.
func TestNewKustomizationFilename(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	if err := os.Rename(
		filepath.Join(root, "manifests/overlays/development/kustomization.yaml"),
		filepath.Join(root, "manifests/overlays/development/Kustomization"),
	); err != nil {
		t.Fatal(err)
	}

	native, err := New(loadDefinition(t, root), filepath.Join(root, "definition.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(findResource(t, resources, "Service", "module-service")).NotTo(BeZero())
}

// TestNewKustomizeJSONContentInRecognizedFilename proves JSON content
// under a recognized kustomization file name renders: the entry file
// name selects the kustomization, and its content parses as the JSON
// it is.
func TestNewKustomizeJSONContentInRecognizedFilename(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	if err := os.WriteFile(
		filepath.Join(root, "manifests/overlays/development/kustomization.yaml"),
		[]byte(jsonKustomizationContent), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	native, err := New(loadDefinition(t, root), filepath.Join(root, "definition.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(findResource(t, resources, "Deployment", "module-controller")).NotTo(BeZero())
}

// TestNewKustomizeRejectsKustomizationJSON proves kustomization.json
// is not a recognized entry file name: a directory carrying only that
// file fails with the renderer's own no-kustomization-file error
// instead of silently building.
func TestNewKustomizeRejectsKustomizationJSON(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.json", jsonKustomizationContent)
	writeModuleFile(t, root, "manifests/overlays/development/deployment.yaml", deploymentResourceYAML)

	native, err := New(loadDefinition(t, root), filepath.Join(root, "definition.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(resources).To(BeNil())
	g.Expect(errors.Is(err, mkkustomize.ErrNoKustomizationFile)).To(BeTrue())
}

// TestNewKustomizeAddsNoRendererMetadata proves the explicit
// ContentHash=false and SourceAnnotations=false construction flags
// preserve the old framework engine's effective output: no rendered
// object gains a content hash or source annotation.
func TestNewKustomizeAddsNoRendererMetadata(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	native, err := New(loadDefinition(t, root), filepath.Join(root, "definition.yaml"), "")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resources).NotTo(BeEmpty())

	assertNoRendererMetadata(t, resources)
}
