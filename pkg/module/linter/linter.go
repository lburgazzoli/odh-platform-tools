// Package linter runs manifest validators over rendered module
// objects and reports their findings.
//
// The linter itself owns no rules: checks are linter.Validation
// functions injected into the action, so the check set is the
// caller's choice. Validation is a pure function of the rendered
// object list and the definition: it reads no files or environment,
// contacts no cluster, invokes no renderer, and never mutates its
// inputs. Findings are aggregated as diagnostics with stable check
// IDs; a nil execution error means validation ran to completion, not
// that the manifests are valid.
package linter

import (
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

// Validation runs the manifest checks over the objects a manifest
// backend rendered for one module and returns the findings. It is a
// pure function of the definition and the rendered objects; the
// checks package provides the default implementation. A non-nil
// error means a check failed to evaluate — it is an execution error,
// never a finding about the manifests.
type Validation func(
	definition config.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) ([]Diagnostic, error)

// Diagnostic is one validation finding. GVK, Namespace, Name, and Field
// identify the affected object location when relevant; Message is the
// actionable explanation.
type Diagnostic struct {
	ID        string
	Severity  Severity
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
