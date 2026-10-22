package checks

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"k8s.io/apimachinery/pkg/runtime/schema"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/crd"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/deployments"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/types"
)

// testDefinition returns a minimal module definition: its type is the
// SyntheticModule custom resource.
func testDefinition() config.PlatformModuleDefinition {
	return config.PlatformModuleDefinition{
		Metadata: config.Metadata{Name: "synthetic"},
		Spec: config.Spec{
			Type: config.Type{GroupVersionKind: schema.GroupVersionKind{
				Group:   "components.platform.opendatahub.io",
				Version: "v1alpha1",
				Kind:    "SyntheticModule",
			}},
		},
	}
}

func TestValidateComposesEveryCheck(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata":   map[string]any{"name": "odh-applications"},
		}},
	}

	diagnostics, err := Validate(testDefinition(), resources)

	g.Expect(err).NotTo(HaveOccurred())

	ids := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		ids = append(ids, diagnostic.ID)
	}

	g.Expect(ids).To(ConsistOf(
		types.ResourceKindDisallowed,
		deployments.Missing,
		crd.Missing,
	))
}
