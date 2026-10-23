package helm

import (
	"errors"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

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

// writeGuardChartFixture writes a chart rendering one payload template
// next to the module definition.
func writeGuardChartFixture(t *testing.T, root string, template string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/payload.yaml", template)
}

// TestRenderHelmRejectsReservedResourcesOnEveryPath proves the shared
// payload guard fails both render paths — lint's zero options and
// run's runtime options — on reserved objects: the definition's own
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
			g.Expect(errors.Is(err, renderer.ErrReservedResource)).To(BeTrue())

			_, err = RenderWithOptions(t.Context(), definition, definitionFile, renderer.Options{
				Namespace: "modules",
			})
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, renderer.ErrReservedResource)).To(BeTrue())
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
	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/configmap.yaml", guardOrdinaryConfigMapTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/crds/platformmoduledefinitions.yaml", guardReservedCRDTemplateYAML)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	_, err := Render(t.Context(), definition, definitionFile)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, renderer.ErrReservedResource)).To(BeTrue())
}

// TestRenderHelmGuardAllowsOrdinaryResources proves an ordinary
// controller-package resource renders on both paths — the guard
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

	resources, err = RenderWithOptions(t.Context(), definition, definitionFile, renderer.Options{
		Namespace: "modules",
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(findResource(t, resources, "ConfigMap", "module-config")).NotTo(BeZero())
}
