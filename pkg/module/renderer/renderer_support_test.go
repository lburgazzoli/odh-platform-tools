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
	"context"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"
	mkkustomize "github.com/k8s-manifest-kit/renderer-kustomize/pkg"
	mkfs "github.com/k8s-manifest-kit/renderer-kustomize/pkg/util/fs"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
)

// guardModuleInstanceV1YAML is an instance of the configured module
// type at a version other than the configured one: the guard matches
// the GroupKind at any version.
const guardModuleInstanceV1YAML = `apiVersion: components.platform.opendatahub.io/v1
kind: SyntheticModule
metadata:
  name: instance
  namespace: modules
`

// guardModuleInstanceOtherKindYAML is an instance of the configured
// group at a different kind: the guard must let it through.
const guardModuleInstanceOtherKindYAML = `apiVersion: components.platform.opendatahub.io/v1
kind: OtherModule
metadata:
  name: instance
  namespace: modules
`

// guardDefinitionObjectYAML is a config.opendatahub.io
// PlatformModuleDefinition object at a version other than the API's
// current one.
const guardDefinitionObjectYAML = `apiVersion: config.opendatahub.io/v1beta1
kind: PlatformModuleDefinition
metadata:
  name: other
`

// guardReservedCRDYAML is the persisted definition CRD under its
// reserved name.
const guardReservedCRDYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: platformmoduledefinitions.config.opendatahub.io
`

// guardOrdinaryCRDYAML is an unrelated CRD: only the reserved name is
// rejected.
const guardOrdinaryCRDYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: others.config.opendatahub.io
`

// guardOrdinaryConfigMapYAML is an ordinary controller-package
// resource.
const guardOrdinaryConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: module-config
  namespace: modules
`

// guardDefinition returns a definition owning the synthetic module
// type.
func guardDefinition() toolApi.PlatformModuleDefinition {
	return toolApi.PlatformModuleDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: toolApi.APIVersion, Kind: toolApi.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic"},
		Spec: toolApi.PlatformModuleDefinitionSpec{
			Instance: toolApi.Instance{
				Type: toolApi.ModuleType{
					Group:   "components.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "SyntheticModule",
				},
			},
		},
	}
}

// guardResource converts one YAML document to an unstructured object.
func guardResource(t *testing.T, document string) unstructured.Unstructured {
	t.Helper()

	g := NewWithT(t)

	object := unstructured.Unstructured{}
	g.Expect(yaml.Unmarshal([]byte(document), &object)).To(Succeed())

	return object
}

// TestCheckReservedRejectsReservedIdentities proves the shared payload
// guard rejects the objects a module run must never deploy — the
// configured module type's own instances at any version, any
// config.opendatahub.io PlatformModuleDefinition, and the reserved
// definition CRD — with the sentinel reachable through errors.Is.
func TestCheckReservedRejectsReservedIdentities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		document string
	}{
		{"module instance at another version", guardModuleInstanceV1YAML},
		{"platform module definition at another version", guardDefinitionObjectYAML},
		{"reserved definition CRD", guardReservedCRDYAML},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			err := CheckReserved(guardDefinition(), []unstructured.Unstructured{
				guardResource(t, guardOrdinaryConfigMapYAML),
				guardResource(t, testCase.document),
			})

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, ErrReservedResource)).To(BeTrue())
		})
	}
}

// TestCheckReservedAllowsOrdinaryResources proves the guard passes
// ordinary controller-package contents through untouched: only the
// reserved identities are examined, and the rendered objects
// themselves are never modified.
func TestCheckReservedAllowsOrdinaryResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		guardResource(t, guardOrdinaryConfigMapYAML),
		guardResource(t, guardModuleInstanceOtherKindYAML),
		guardResource(t, guardOrdinaryCRDYAML),
	}
	before := make([]unstructured.Unstructured, len(resources))
	for i := range resources {
		before[i] = *resources[i].DeepCopy()
	}

	g.Expect(CheckReserved(guardDefinition(), resources)).To(Succeed())

	for i := range resources {
		g.Expect(resources[i]).To(Equal(before[i]))
	}
}

// guardModuleInstanceTemplateYAML renders an instance of the
// definition's own module type at a version other than the configured
// one: the guard matches the GroupKind at any version.
const guardModuleInstanceTemplateYAML = `apiVersion: components.platform.opendatahub.io/v1
kind: SyntheticModule
metadata:
  name: instance
  namespace: modules
`

// guardDefinitionTemplateYAML renders a config.opendatahub.io
// PlatformModuleDefinition object.
const guardDefinitionTemplateYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: other
`

// guardReservedCRDTemplateYAML renders the persisted definition CRD
// under its reserved name.
const guardReservedCRDTemplateYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: platformmoduledefinitions.config.opendatahub.io
`

// guardOrdinaryConfigMapTemplateYAML renders an ordinary
// controller-package resource.
const guardOrdinaryConfigMapTemplateYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: module-config
  namespace: modules
`

// guardKustomizationYAML lists one payload resource; every guard case
// swaps the resource document it references.
const guardKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- resource.yaml
`

// guardDefinitionFS is the in-memory source of the module definition
// the real loader reads.
var guardDefinitionFS = fstest.MapFS{
	"module/definition.yaml": &fstest.MapFile{Data: []byte(kustomizeDefinitionYAML)},
}

// writeGuardChartFixture writes a chart rendering one payload template
// next to the module definition.
func writeGuardChartFixture(t *testing.T, root string, template string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", helmChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", helmValuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/payload.yaml", template)
}

// newGuardMemoryFs returns a fresh in-memory filesystem holding one
// Kustomize module package whose single resource is the given
// document. Every case renders from its own filesystem: no fixture
// touches a temporary directory or the operating-system disk.
func newGuardMemoryFs(t *testing.T, resource string) filesys.FileSystem {
	t.Helper()

	g := NewWithT(t)

	fsys := mkfs.NewMemoryFs()
	for path, content := range map[string]string{
		"/module/definition.yaml":                                   kustomizeDefinitionYAML,
		"/module/manifests/overlays/development/kustomization.yaml": guardKustomizationYAML,
		"/module/manifests/overlays/development/resource.yaml":      resource,
	} {
		g.Expect(fsys.WriteFile(path, []byte(content))).To(Succeed())
	}

	return fsys
}

// loadGuardDefinition loads the module definition through the real
// loader from an in-memory filesystem, so the guard fixtures perform
// no disk reads either.
func loadGuardDefinition(t *testing.T) toolApi.PlatformModuleDefinition {
	t.Helper()

	g := NewWithT(t)

	loaded, err := loader.Load(t.Context(), guardDefinitionFS, "module/definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())

	return loaded
}

// newGuardNativeKustomize builds the native Kustomize renderer over
// an in-memory filesystem, configured as the backend constructor
// configures it minus the namespace plugin: the guard is engine
// policy, not plugin policy, so the in-memory cases need no plugin.
func newGuardNativeKustomize(t *testing.T, resource string) (enginetypes.Renderer, error) {
	t.Helper()

	native, err := mkkustomize.New(
		[]mkkustomize.Source{{Path: "/module/manifests/overlays/development"}},
		mkkustomize.WithContentHash(false),
		mkkustomize.WithSourceAnnotations(false),
		mkkustomize.WithFileSystem(newGuardMemoryFs(t, resource)),
	)
	if err != nil {
		return nil, err
	}

	return native, nil
}

// TestRenderHelmRejectsReservedResourcesOnEveryPath proves the shared
// payload guard fails both Helm render paths — lint's zero options and
// run's namespace option — on reserved objects: the definition's own
// module type at any version, any PlatformModuleDefinition, and the
// reserved definition CRD. Rejection is a render error carrying the
// sentinel, never a silent drop.
func TestRenderHelmRejectsReservedResourcesOnEveryPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		template string
	}{
		{"module instance at another version", guardModuleInstanceTemplateYAML},
		{"platform module definition", guardDefinitionTemplateYAML},
		{"reserved definition CRD", guardReservedCRDTemplateYAML},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			root := t.TempDir()
			writeGuardChartFixture(t, root, testCase.template)

			definition := loadDefinition(t, root)
			definitionFile := filepath.Join(root, "definition.yaml")

			_, err := Render(t.Context(), definition, definitionFile)
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, ErrReservedResource)).To(BeTrue())

			_, err = Render(t.Context(), definition, definitionFile, WithNamespace("modules"))
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, ErrReservedResource)).To(BeTrue())
		})
	}
}

// TestRenderHelmRejectsReservedCRDFromBundledCRDs proves a reserved
// CRD shipped in the chart's bundled crds/ directory fails the render
// too: bundled CRDs are part of the guarded output.
func TestRenderHelmRejectsReservedCRDFromBundledCRDs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", helmChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", helmValuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/configmap.yaml", guardOrdinaryConfigMapTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/crds/platformmoduledefinitions.yaml", guardReservedCRDTemplateYAML)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	_, err := Render(t.Context(), definition, definitionFile)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, ErrReservedResource)).To(BeTrue())
}

// TestRenderHelmGuardAllowsOrdinaryResources proves an ordinary
// controller-package resource renders on both Helm paths — the guard
// rejects only reserved identities.
func TestRenderHelmGuardAllowsOrdinaryResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeGuardChartFixture(t, root, guardOrdinaryConfigMapTemplateYAML)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	resources, err := Render(t.Context(), definition, definitionFile)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(findResource(t, resources, "ConfigMap", "module-config")).NotTo(BeZero())

	resources, err = Render(t.Context(), definition, definitionFile, WithNamespace("modules"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(findResource(t, resources, "ConfigMap", "module-config")).NotTo(BeZero())
}

// TestExecuteKustomizeRejectsReservedResourcesInMemory proves the
// shared payload guard over the real Kustomize backend, engine, and
// guard with no disk writes: the native renderer is built over a
// fresh in-memory filesystem and driven through the same private
// execution helper the public Render uses. The zero options prove the
// lint path; the runtime variant proves a caller post-renderer that
// enriches the batch still reaches the guard — rejection happens
// after enrichment, never before.
func TestExecuteKustomizeRejectsReservedResourcesInMemory(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		resource string
	}{
		{"module instance at another version", guardModuleInstanceTemplateYAML},
		{"platform module definition", guardDefinitionTemplateYAML},
		{"reserved definition CRD", guardReservedCRDTemplateYAML},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			definition := loadGuardDefinition(t)

			native, err := newGuardNativeKustomize(t, testCase.resource)
			g.Expect(err).NotTo(HaveOccurred())

			_, err = execute(t.Context(), definition, native, RenderOptions{})
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, ErrReservedResource)).To(BeTrue())

			// The runtime variant observes the enrichment rather than
			// re-applying a namespace to an already-built renderer.
			observed := 0

			observe := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
				observed++

				return objects, nil
			}

			native, err = newGuardNativeKustomize(t, testCase.resource)
			g.Expect(err).NotTo(HaveOccurred())

			_, err = execute(t.Context(), definition, native, RenderOptions{
				PostRenderers: []enginetypes.PostRenderer{observe},
			})
			g.Expect(observed).To(Equal(1))
			g.Expect(errors.Is(err, ErrReservedResource)).To(BeTrue())
		})
	}
}

// TestExecuteKustomizeGuardAllowsOrdinaryResourcesInMemory proves an
// ordinary controller-package resource renders through the same
// in-memory path on both option shapes — the guard rejects only
// reserved identities.
func TestExecuteKustomizeGuardAllowsOrdinaryResourcesInMemory(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := loadGuardDefinition(t)

	native, err := newGuardNativeKustomize(t, guardOrdinaryConfigMapTemplateYAML)
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := execute(t.Context(), definition, native, RenderOptions{})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(findResource(t, resources, "ConfigMap", "module-config")).NotTo(BeZero())

	observed := 0

	observe := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		observed++

		return objects, nil
	}

	native, err = newGuardNativeKustomize(t, guardOrdinaryConfigMapTemplateYAML)
	g.Expect(err).NotTo(HaveOccurred())

	resources, err = execute(t.Context(), definition, native, RenderOptions{
		PostRenderers: []enginetypes.PostRenderer{observe},
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(observed).To(Equal(1))
	g.Expect(findResource(t, resources, "ConfigMap", "module-config")).NotTo(BeZero())
}
