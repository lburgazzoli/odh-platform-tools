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

package kustomize

import (
	"errors"
	"testing"
	"testing/fstest"

	. "github.com/onsi/gomega"

	mkfs "github.com/k8s-manifest-kit/renderer-kustomize/pkg/util/fs"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// guardModuleInstanceResourceYAML is a resource of the definition's
// own module type at a version other than the configured one: the
// guard matches the GroupKind at any version.
const guardModuleInstanceResourceYAML = `apiVersion: components.platform.opendatahub.io/v1
kind: SyntheticModule
metadata:
  name: instance
  namespace: modules
`

// guardDefinitionResourceYAML is a config.opendatahub.io
// PlatformModuleDefinition resource.
const guardDefinitionResourceYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: other
`

// guardReservedCRDResourceYAML is the persisted definition CRD under
// its reserved name.
const guardReservedCRDResourceYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: platformmoduledefinitions.config.opendatahub.io
`

// guardOrdinaryConfigMapResourceYAML is an ordinary
// controller-package resource.
const guardOrdinaryConfigMapResourceYAML = `apiVersion: v1
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
// the real loader reads: the same document as the on-disk
// definitionYAML, at the path the guard fixtures treat as the
// definition file.
var guardDefinitionFS = fstest.MapFS{
	"module/definition.yaml": &fstest.MapFile{Data: []byte(definitionYAML)},
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
		"/module/definition.yaml":                                   definitionYAML,
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
func loadGuardDefinition(t *testing.T) (v1alpha1.PlatformModuleDefinition, string) {
	t.Helper()

	g := NewWithT(t)

	loaded, err := loader.Load(t.Context(), guardDefinitionFS, "module/definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())

	return loaded, "/module/definition.yaml"
}

// TestRenderKustomizeRejectsReservedResourcesOnEveryPath proves the
// shared payload guard fails both render paths — the zero options lint
// uses and the runtime options run uses — on reserved objects, through
// the same implementation the public render entrypoints call: the
// definition's own module type at any version, any
// PlatformModuleDefinition, and the reserved definition CRD. Rejection
// is a render error carrying the sentinel, never a silent drop, and
// the whole fixture lives in a fresh in-memory filesystem.
func TestRenderKustomizeRejectsReservedResourcesOnEveryPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		resource string
	}{
		{"module instance at another version", guardModuleInstanceResourceYAML},
		{"platform module definition", guardDefinitionResourceYAML},
		{"reserved definition CRD", guardReservedCRDResourceYAML},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			definition, definitionFile := loadGuardDefinition(t)

			_, err := render(t.Context(), definition, definitionFile, renderer.Options{}, newGuardMemoryFs(t, testCase.resource))
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, renderer.ErrReservedResource)).To(BeTrue())

			_, err = render(t.Context(), definition, definitionFile, renderer.Options{
				Namespace: "modules",
			}, newGuardMemoryFs(t, testCase.resource))
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, renderer.ErrReservedResource)).To(BeTrue())
		})
	}
}

// TestRenderKustomizeGuardAllowsOrdinaryResources proves an ordinary
// controller-package resource renders on both in-memory paths — the
// guard rejects only reserved identities.
func TestRenderKustomizeGuardAllowsOrdinaryResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition, definitionFile := loadGuardDefinition(t)

	resources, err := render(t.Context(), definition, definitionFile, renderer.Options{}, newGuardMemoryFs(t, guardOrdinaryConfigMapResourceYAML))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(findResource(t, resources, "ConfigMap", "module-config")).NotTo(BeZero())

	resources, err = render(t.Context(), definition, definitionFile, renderer.Options{
		Namespace: "modules",
	}, newGuardMemoryFs(t, guardOrdinaryConfigMapResourceYAML))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(findResource(t, resources, "ConfigMap", "module-config")).NotTo(BeZero())
}
