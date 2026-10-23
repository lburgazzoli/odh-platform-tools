package crd

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// object builds one unstructured object with the given identity.
func object(apiVersion string, kind string, name string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
	}}
}

// moduleCRD builds one CustomResourceDefinition declaring the given
// type.
func moduleCRD(name string, group string, kind string) unstructured.Unstructured {
	res := object("apiextensions.k8s.io/v1", "CustomResourceDefinition", name)
	res.Object["spec"] = map[string]any{
		"group": group,
		"names": map[string]any{"kind": kind, "plural": "synthetics"},
	}

	return res
}

// testDefinition returns a minimal module definition: its type is the
// SyntheticModule custom resource.
func testDefinition() v1alpha1.PlatformModuleDefinition {
	return v1alpha1.PlatformModuleDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic"},
		Spec: v1alpha1.PlatformModuleDefinitionSpec{
			Instance: v1alpha1.Instance{Type: v1alpha1.ModuleType{
				Group:   "components.platform.opendatahub.io",
				Version: "v1alpha1",
				Kind:    "SyntheticModule",
			}},
		},
	}
}

func TestCRDValidate(t *testing.T) {
	t.Parallel()

	t.Run("matching CRD passes", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		resources := []unstructured.Unstructured{
			object("apps/v1", "Deployment", "controller"),
			moduleCRD("synthetics.components.platform.opendatahub.io",
				"components.platform.opendatahub.io", "SyntheticModule"),
		}

		diagnostics, err := Validate(testDefinition(), resources)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(BeEmpty())
	})

	t.Run("missing CRD is reported on the module", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(testDefinition(), []unstructured.Unstructured{
			object("apps/v1", "Deployment", "controller"),
		})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(Missing))
		g.Expect(diagnostics[0].Severity).To(Equal(linter.SeverityError))
		g.Expect(diagnostics[0].GVK.Group).To(Equal("components.platform.opendatahub.io"))
		g.Expect(diagnostics[0].GVK.Kind).To(Equal("SyntheticModule"))
		g.Expect(diagnostics[0].Message).To(ContainSubstring(
			`no CustomResourceDefinition for "components.platform.opendatahub.io/v1alpha1, Kind=SyntheticModule"`))
	})

	t.Run("CRD for another type does not match", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(testDefinition(), []unstructured.Unstructured{
			moduleCRD("others.example.io", "example.io", "Other"),
		})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(Missing))
	})

	t.Run("duplicate CRDs are reported with sorted names", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		resources := []unstructured.Unstructured{
			moduleCRD("b-synthetics.components.platform.opendatahub.io",
				"components.platform.opendatahub.io", "SyntheticModule"),
			moduleCRD("a-synthetics.components.platform.opendatahub.io",
				"components.platform.opendatahub.io", "SyntheticModule"),
		}

		diagnostics, err := Validate(testDefinition(), resources)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(Duplicate))
		g.Expect(diagnostics[0].Message).To(ContainSubstring(
			"a-synthetics.components.platform.opendatahub.io, b-synthetics.components.platform.opendatahub.io"))
	})

	t.Run("CRD without declared type does not match and is not an error", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		resources := []unstructured.Unstructured{
			object("apiextensions.k8s.io/v1", "CustomResourceDefinition", "incomplete"),
		}

		diagnostics, err := Validate(testDefinition(), resources)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(Missing))
	})

	t.Run("malformed CRD is an evaluation error, never a silent non-match", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		// spec is a scalar: indexing it is a jq evaluation error.
		malformed := object("apiextensions.k8s.io/v1", "CustomResourceDefinition", "broken")
		malformed.Object["spec"] = 5

		_, err := Validate(testDefinition(), []unstructured.Unstructured{malformed})

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("broken"))
	})
}
