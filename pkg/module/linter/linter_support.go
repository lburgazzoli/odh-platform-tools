package linter

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// rayModuleName is the only module allowed to bundle
// SecurityContextConstraints, mirroring the operator's
// manifests_compliance_test.go module-specific allowlist.
const rayModuleName = "ray"

// kindRule pins the supported version and scope of one allowed
// GroupKind.
type kindRule struct {
	version       string
	clusterScoped bool
}

// manifestKinds is the manifest packaging allowlist, pinned to the
// operator's allowedChartResourceKinds plus the pinned supported
// versions. Kinds absent from the table are findings.
var manifestKinds = map[schema.GroupKind]kindRule{
	{Group: "apps", Kind: "Deployment"}:                       {version: "v1"},
	{Group: "", Kind: "Service"}:                              {version: "v1"},
	{Group: "", Kind: "ServiceAccount"}:                       {version: "v1"},
	{Group: "", Kind: "ConfigMap"}:                            {version: "v1"},
	{Group: "rbac.authorization.k8s.io", Kind: "Role"}:        {version: "v1"},
	{Group: "rbac.authorization.k8s.io", Kind: "RoleBinding"}: {version: "v1"},
	{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}: {version: "v1", clusterScoped: true},
	{Group: "rbac.authorization.k8s.io", Kind: "ClusterRoleBinding"}: {
		version:       "v1",
		clusterScoped: true,
	},
	{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}: {
		version:       "v1",
		clusterScoped: true,
	},
	{Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration"}: {
		version:       "v1",
		clusterScoped: true,
	},
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingWebhookConfiguration"}: {
		version:       "v1",
		clusterScoped: true,
	},
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicy"}: {
		version:       "v1",
		clusterScoped: true,
	},
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicyBinding"}: {
		version:       "v1",
		clusterScoped: true,
	},
	{Group: "cert-manager.io", Kind: "Issuer"}:      {version: "v1"},
	{Group: "cert-manager.io", Kind: "Certificate"}: {version: "v1"},
	{Group: "networking.k8s.io", Kind: "NetworkPolicy"}: {
		version: "v1",
	},
	{Group: "security.openshift.io", Kind: "SecurityContextConstraints"}: {
		version:       "v1",
		clusterScoped: true,
	},
}

// GroupKinds referenced by identity checks and bundle-reference
// resolution.
var (
	sccGroupKind = schema.GroupKind{
		Group: "security.openshift.io",
		Kind:  "SecurityContextConstraints",
	}

	deploymentGroupKind         = schema.GroupKind{Group: "apps", Kind: "Deployment"}
	serviceGroupKind            = schema.GroupKind{Group: "", Kind: "Service"}
	serviceAccountGroupKind     = schema.GroupKind{Group: "", Kind: "ServiceAccount"}
	roleGroupKind               = schema.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "Role"}
	clusterRoleGroupKind        = schema.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}
	roleBindingGroupKind        = schema.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "RoleBinding"}
	clusterRoleBindingGroupKind = schema.GroupKind{
		Group: "rbac.authorization.k8s.io",
		Kind:  "ClusterRoleBinding",
	}
	customResourceDefinitionGroupKind = schema.GroupKind{
		Group: "apiextensions.k8s.io",
		Kind:  "CustomResourceDefinition",
	}
	validatingWebhookConfigurationGroupKind = schema.GroupKind{
		Group: "admissionregistration.k8s.io",
		Kind:  "ValidatingWebhookConfiguration",
	}
)

// rbacAPIGroup is the only apiGroup a roleRef may carry.
const rbacAPIGroup = "rbac.authorization.k8s.io"

// moduleContext is the in-memory definition identity and selector
// context validation runs against. It is derived once and never
// shared with mutation.
type moduleContext struct {
	name           string
	typeGroup      string
	typeVersion    string
	typeKind       string
	deploymentName string
	mainContainer  string
	initContainer  string
}

// moduleGVK returns the custom resource type the module owns.
func (m moduleContext) moduleGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   m.typeGroup,
		Version: m.typeVersion,
		Kind:    m.typeKind,
	}
}

// newModuleContext derives the validation context from a definition.
// An omitted main container name keeps the config default; the input
// definition is never modified.
func newModuleContext(definition config.PlatformModuleDefinition) moduleContext {
	deployment := definition.Spec.Controller.Deployment

	mainContainer := config.DefaultMainContainerName
	if deployment.Container != nil && deployment.Container.Name != nil {
		mainContainer = *deployment.Container.Name
	}

	initContainer := ""
	if deployment.InitContainer != nil && deployment.InitContainer.Name != nil {
		initContainer = *deployment.InitContainer.Name
	}

	return moduleContext{
		name:           definition.Metadata.Name,
		typeGroup:      definition.Spec.Type.Group,
		typeVersion:    definition.Spec.Type.Version,
		typeKind:       definition.Spec.Type.Kind,
		deploymentName: deployment.Name,
		mainContainer:  mainContainer,
		initContainer:  initContainer,
	}
}

// kindRuleFor returns the pinned rule for a GroupKind, honoring the
// fixed Ray-only SecurityContextConstraints exception: the kind never
// leaks to another module.
func kindRuleFor(moduleName string, gk schema.GroupKind) (kindRule, bool) {
	if gk == sccGroupKind && moduleName != rayModuleName {
		return kindRule{}, false
	}

	rule, allowed := manifestKinds[gk]
	return rule, allowed
}

// collector accumulates diagnostics across rule areas.
type collector struct {
	diagnostics []Diagnostic
}

// add appends one diagnostic.
func (c *collector) add(d Diagnostic) {
	c.diagnostics = append(c.diagnostics, d)
}

// objectDiagnostic builds an error diagnostic attached to one
// object. Every object-attached rule finding currently fails
// validation; warnings are built with moduleDiagnostic.
func objectDiagnostic(
	id string,
	phase Phase,
	object *unstructured.Unstructured,
	field, message string,
) Diagnostic {
	return Diagnostic{
		ID:        id,
		Severity:  SeverityError,
		Phase:     phase,
		GVK:       object.GroupVersionKind(),
		Namespace: object.GetNamespace(),
		Name:      object.GetName(),
		Field:     field,
		Message:   message,
	}
}

// moduleDiagnostic builds a diagnostic attached to the module's
// custom resource type rather than a bundled object.
func moduleDiagnostic(
	id string,
	severity Severity,
	phase Phase,
	module moduleContext,
	field, message string,
) Diagnostic {
	return Diagnostic{
		ID:       id,
		Severity: severity,
		Phase:    phase,
		GVK:      module.moduleGVK(),
		Field:    field,
		Message:  message,
	}
}

// effectiveNamespace interprets an omitted namespace as default. It
// is used only for local comparisons; bundled objects are never
// rewritten.
func effectiveNamespace(namespace string) string {
	if namespace == "" {
		return "default"
	}

	return namespace
}

// objectIndex resolves versionless references between bundled
// objects, the way roleRef and subjects work in Kubernetes.
type objectIndex struct {
	identities map[string]struct{}
}

// indexObjects indexes every object with a usable identity. Objects
// without an identity cannot be referenced and are reported by the
// identity rules instead.
func indexObjects(resources []unstructured.Unstructured) *objectIndex {
	index := &objectIndex{identities: make(map[string]struct{}, len(resources))}

	for i := range resources {
		object := &resources[i]

		if object.GetKind() == "" || object.GetName() == "" {
			continue
		}

		if kind := object.GetKind(); kind == "List" || strings.HasSuffix(kind, "List") {
			continue
		}

		index.identities[referenceKey(object.GroupVersionKind().GroupKind(), object.GetNamespace(), object.GetName())] = struct{}{}
	}

	return index
}

// has reports whether an object of the given GroupKind, name, and
// namespace (omitted meaning default) is bundled.
func (i *objectIndex) has(gk schema.GroupKind, namespace, name string) bool {
	_, found := i.identities[referenceKey(gk, namespace, name)]
	return found
}

// referenceKey builds the lookup key for one bundled identity.
func referenceKey(gk schema.GroupKind, namespace, name string) string {
	return gk.String() + "/" + effectiveNamespace(namespace) + "/" + name
}
