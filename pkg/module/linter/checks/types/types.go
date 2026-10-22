// Package types implements the resource kind allowlist check: every
// object a module bundles must be of an allowed kind, and an allowed
// kind is allowed for every module.
package types

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"

	gvk "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// ResourceKindDisallowed reports a bundled object whose kind is not
// allowed for module manifests.
const ResourceKindDisallowed = "RESOURCE_KIND_DISALLOWED"

// Types is the resource kind allowlist check.
type Types struct {
	// AllowedKinds lists the resource kinds module manifests may
	// bundle. A nil or empty list allows nothing.
	AllowedKinds []schema.GroupKind
}

// Validate reports every bundled object whose kind is not on the
// allowlist.
func (t Types) Validate(
	_ config.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) ([]linter.Diagnostic, error) {
	var diagnostics []linter.Diagnostic

	allowed := sets.New(t.AllowedKinds...)

	for i := range resources {
		object := &resources[i]

		if allowed.Has(object.GroupVersionKind().GroupKind()) {
			continue
		}

		diagnostics = append(diagnostics, linter.Diagnostic{
			ID:        ResourceKindDisallowed,
			Severity:  linter.SeverityError,
			GVK:       object.GroupVersionKind(),
			Namespace: object.GetNamespace(),
			Name:      object.GetName(),
			Message:   "resource kind is not allowed for module manifests",
		})
	}

	return diagnostics, nil
}

// Validate runs the check with the default allowlist, mirroring the
// operator's allowedChartResourceKinds.
func Validate(
	definition config.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) ([]linter.Diagnostic, error) {
	v := Types{}
	v.AllowedKinds = []schema.GroupKind{
		gvk.Deployment.GroupKind(),
		gvk.Service.GroupKind(),
		gvk.ServiceAccount.GroupKind(),
		gvk.ConfigMap.GroupKind(),
		gvk.Role.GroupKind(),
		gvk.RoleBinding.GroupKind(),
		gvk.ClusterRole.GroupKind(),
		gvk.ClusterRoleBinding.GroupKind(),
		gvk.CustomResourceDefinition.GroupKind(),
		gvk.MutatingWebhookConfiguration.GroupKind(),
		gvk.ValidatingWebhookConfiguration.GroupKind(),
		gvk.ValidatingAdmissionPolicy.GroupKind(),
		gvk.ValidatingAdmissionPolicyBinding.GroupKind(),
		gvk.CertManagerIssuer.GroupKind(),
		gvk.CertManagerCertificate.GroupKind(),
		gvk.NetworkPolicy.GroupKind(),
	}

	return v.Validate(definition, resources)
}
