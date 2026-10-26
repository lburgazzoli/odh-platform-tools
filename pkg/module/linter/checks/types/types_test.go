package types

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	. "github.com/onsi/gomega"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
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

// objectWithNamespace builds one namespaced unstructured object.
func objectWithNamespace(apiVersion string, kind string, namespace string, name string) unstructured.Unstructured {
	res := object(apiVersion, kind, name)
	res.Object["metadata"] = map[string]any{"namespace": namespace, "name": name}

	return res
}

// testDefinition returns a minimal module definition: its type is the
// SyntheticModule custom resource.
func testDefinition() toolApi.PlatformModuleDefinition {
	return toolApi.PlatformModuleDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic"},
		Spec: toolApi.PlatformModuleDefinitionSpec{
			Instance: toolApi.Instance{Type: toolApi.ModuleType{
				Group:   "components.platform.opendatahub.io",
				Version: "v1alpha1",
				Kind:    "SyntheticModule",
			}},
		},
	}
}

func TestTypesValidateCustomAllowlist(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	// A caller-configured allowlist replaces the default: a ConfigMap
	// is disallowed here even though the default allows it.
	check := Types{AllowedKinds: []schema.GroupKind{
		{Group: "apps", Kind: "Deployment"},
	}}

	resources := []unstructured.Unstructured{
		object("apps/v1", "Deployment", "controller"),
		objectWithNamespace("v1", "ConfigMap", "odh-applications", "settings"),
	}

	diagnostics, err := check.Validate(testDefinition(), resources)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(diagnostics).To(HaveLen(1))
	g.Expect(diagnostics[0].ID).To(Equal(ResourceKindDisallowed))
	g.Expect(diagnostics[0].Severity).To(Equal(linter.SeverityError))
	g.Expect(diagnostics[0].GVK.Kind).To(Equal("ConfigMap"))
	g.Expect(diagnostics[0].Namespace).To(Equal("odh-applications"))
	g.Expect(diagnostics[0].Name).To(Equal("settings"))
}

func TestTypesValidateNilAllowlistAllowsNothing(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	diagnostics, err := (Types{}).Validate(testDefinition(), []unstructured.Unstructured{
		object("apps/v1", "Deployment", "controller"),
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(diagnostics).To(HaveLen(1))
}

func TestValidateDefaultAllowlist(t *testing.T) {
	t.Parallel()

	t.Run("operator allowlist passes", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		resources := []unstructured.Unstructured{
			object("apps/v1", "Deployment", "controller"),
			object("v1", "Service", "svc"),
			object("v1", "ServiceAccount", "sa"),
			object("v1", "ConfigMap", "cm"),
			object("rbac.authorization.k8s.io/v1", "Role", "role"),
			object("rbac.authorization.k8s.io/v1", "RoleBinding", "rb"),
			object("rbac.authorization.k8s.io/v1", "ClusterRole", "cr"),
			object("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", "crb"),
			object("apiextensions.k8s.io/v1", "CustomResourceDefinition", "crds"),
			object("admissionregistration.k8s.io/v1", "MutatingWebhookConfiguration", "mwc"),
			object("admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", "vwc"),
			object("admissionregistration.k8s.io/v1", "ValidatingAdmissionPolicy", "vap"),
			object("admissionregistration.k8s.io/v1", "ValidatingAdmissionPolicyBinding", "vapb"),
			object("cert-manager.io/v1", "Issuer", "issuer"),
			object("cert-manager.io/v1", "Certificate", "cert"),
			object("networking.k8s.io/v1", "NetworkPolicy", "netpol"),
		}

		diagnostics, err := Validate(testDefinition(), resources)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(BeEmpty())
	})

	t.Run("disallowed kind is reported with object identity", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(testDefinition(), []unstructured.Unstructured{
			object("v1", "Namespace", "odh-applications"),
		})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(ResourceKindDisallowed))
		g.Expect(diagnostics[0].GVK).To(Equal(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}))
		g.Expect(diagnostics[0].Name).To(Equal("odh-applications"))
	})

	t.Run("SCC is disallowed for every module", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		// There are no per-module exceptions: the operator's ray-only
		// SCC is not allowed for any module, including ray itself.
		diagnostics, err := Validate(testDefinition(), []unstructured.Unstructured{
			object("security.openshift.io/v1", "SecurityContextConstraints", "ray-scc"),
		})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(ResourceKindDisallowed))
	})
}
