// Package linter validates in-memory module manifest objects against
// the platform module packaging contract.
//
// Validation is a pure function of the rendered object list and the
// definition's identity and selector context: it reads no files or
// environment, contacts no cluster, invokes no renderer, and never
// mutates its inputs. Manifest defects are aggregated as diagnostics
// with stable check IDs and object paths; a nil execution error means
// validation ran to completion, not that the manifests are valid.
// Only cancellation and internal execution problems return an error.
package linter

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// Severity classifies the impact of a diagnostic.
type Severity string

const (
	// SeverityError findings fail validation.
	SeverityError Severity = "error"

	// SeverityWarning findings report claims that could not be
	// verified statically; they do not fail validation.
	SeverityWarning Severity = "warning"

	// SeverityInfo findings are informational and never fail.
	SeverityInfo Severity = "info"
)

// Phase names the rule area that produced a diagnostic. It groups
// checks for reports; the check ID remains the stable identifier.
type Phase string

const (
	// PhaseResources covers object identity, API version, scope, and
	// the manifest kind allowlist.
	PhaseResources Phase = "resources"

	// PhaseDeployment covers the controller Deployment and its
	// selected containers, selector, and service account.
	PhaseDeployment Phase = "deployment"

	// PhaseRBAC covers RoleBinding role references and ServiceAccount
	// subjects.
	PhaseRBAC Phase = "rbac"

	// PhaseCRD covers the selected Module custom resource definition.
	PhaseCRD Phase = "crd"

	// PhaseSingleton covers singleton name enforcement evidence.
	PhaseSingleton Phase = "singleton"
)

// Stable check identifiers.
const (
	CheckObjectIdentityMissing      = "OBJECT_IDENTITY_MISSING"
	CheckObjectListUnsupported      = "OBJECT_LIST_UNSUPPORTED"
	CheckResourceKindDisallowed     = "RESOURCE_KIND_DISALLOWED"
	CheckResourceVersionUnsupported = "RESOURCE_VERSION_UNSUPPORTED"
	CheckResourceScopeMismatch      = "RESOURCE_SCOPE_MISMATCH"
	CheckDuplicateObject            = "DUPLICATE_OBJECT"

	CheckDeploymentMissing          = "DEPLOYMENT_MISSING"
	CheckDeploymentMultiple         = "DEPLOYMENT_MULTIPLE"
	CheckDeploymentNameMismatch     = "DEPLOYMENT_NAME_MISMATCH"
	CheckContainerMissing           = "CONTAINER_MISSING"
	CheckContainerDuplicate         = "CONTAINER_DUPLICATE"
	CheckContainerListMismatch      = "CONTAINER_LIST_MISMATCH"
	CheckDeploymentSelectorMissing  = "DEPLOYMENT_SELECTOR_MISSING"
	CheckDeploymentSelectorMismatch = "DEPLOYMENT_SELECTOR_MISMATCH"
	CheckServiceAccountUnresolved   = "SERVICEACCOUNT_UNRESOLVED"

	CheckRoleReferenceUnresolved = "ROLE_REFERENCE_UNRESOLVED"
	CheckRoleReferenceMalformed  = "ROLE_REFERENCE_MALFORMED"

	CheckModuleCRDMissing           = "MODULE_CRD_MISSING"
	CheckModuleCRDDuplicate         = "MODULE_CRD_DUPLICATE"
	CheckModuleCRDMalformed         = "MODULE_CRD_MALFORMED"
	CheckModuleCRDScope             = "MODULE_CRD_SCOPE"
	CheckModuleCRDName              = "MODULE_CRD_NAME"
	CheckModuleCRDVersionNotServed  = "MODULE_CRD_VERSION_NOT_SERVED"
	CheckModuleCRDVersionDuplicate  = "MODULE_CRD_VERSION_DUPLICATE"
	CheckModuleCRDStorage           = "MODULE_CRD_STORAGE"
	CheckModuleCRDStatusSubresource = "MODULE_CRD_STATUS_SUBRESOURCE"
	CheckModuleCRDSchemaStructural  = "MODULE_CRD_SCHEMA_STRUCTURAL"
	CheckModuleCRDStatusSchema      = "MODULE_CRD_STATUS_SCHEMA"

	CheckSingletonEvidenceMissing   = "SINGLETON_EVIDENCE_MISSING"
	CheckSingletonWebhookUnverified = "SINGLETON_WEBHOOK_UNVERIFIED"
)

// Diagnostic is one validation finding. GVK, Namespace, Name, and Field
// identify the affected object location when relevant; Message is the
// actionable explanation.
type Diagnostic struct {
	ID        string
	Severity  Severity
	Phase     Phase
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
	Field     string
	Message   string
}

// Path returns the stable object path the diagnostic refers to, such
// as "apps/v1, Kind=Deployment/odh-applications/synthetic-controller/
// spec.template.spec.containers", or an empty string for findings that
// are not attached to one object.
func (d Diagnostic) Path() string {
	var path []string

	if !d.GVK.Empty() {
		path = append(path, d.GVK.String())
	}

	for _, part := range []string{d.Namespace, d.Name, d.Field} {
		if part != "" {
			path = append(path, part)
		}
	}

	return strings.Join(path, "/")
}

// String renders the diagnostic as one report line.
func (d Diagnostic) String() string {
	return fmt.Sprintf("%s %s %s: %s", d.Severity, d.ID, d.Path(), d.Message)
}

// Result is the aggregated outcome of one validation run.
type Result struct {
	// Diagnostics are sorted deterministically: by check ID, then
	// object identity, then field and message. The order does not
	// depend on the input object order.
	Diagnostics []Diagnostic
}

// HasErrors reports whether any diagnostic has error severity.
func (r Result) HasErrors() bool {
	return slices.ContainsFunc(r.Diagnostics, func(d Diagnostic) bool {
		return d.Severity == SeverityError
	})
}

// HasWarnings reports whether any diagnostic has warning severity.
func (r Result) HasWarnings() bool {
	return slices.ContainsFunc(r.Diagnostics, func(d Diagnostic) bool {
		return d.Severity == SeverityWarning
	})
}

// Validate validates the objects a manifest backend rendered for the
// module described by definition, and returns every finding.
//
// The definition supplies the in-memory identity and selector context
// only: the module name (used by the fixed Ray SecurityContextConstraints
// exception), the module custom resource type, the controller
// Deployment name, and the selected container names. An omitted main
// container name is interpreted as the config default; the definition
// itself is never modified.
//
// A nil error means validation ran to completion over the whole list.
// Findings, including error-severity ones, are returned in the Result;
// only a cancelled context or an internal failure returns an error.
func Validate(
	ctx context.Context,
	definition config.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	module := newModuleContext(definition)

	c := &collector{}
	index := indexObjects(resources)

	validateObjectContract(c, module, resources)
	validateDeploymentContract(c, module, resources, index)
	validateRBACContract(c, resources, index)

	crd := validateModuleCRD(c, module, resources)
	validateSingletonEvidence(c, module, resources, index, crd)

	diagnostics := c.diagnostics
	slices.SortStableFunc(diagnostics, func(a, b Diagnostic) int {
		return cmp.Or(
			cmp.Compare(a.ID, b.ID),
			cmp.Compare(a.GVK.String(), b.GVK.String()),
			cmp.Compare(a.Namespace, b.Namespace),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Field, b.Field),
			cmp.Compare(a.Message, b.Message),
		)
	})

	return Result{Diagnostics: diagnostics}, nil
}
