// Package linter runs injected manifest checks and aggregates diagnostics.
package linter

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
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

// Validation checks a definition and rendered resources without mutating them.
// Errors indicate evaluation failure; manifest defects are diagnostics.
type Validation func(
	definition toolApi.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) ([]Diagnostic, error)

// Diagnostic identifies a finding and its resource location.
type Diagnostic struct {
	ID        string
	Severity  Severity
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
	Field     string
	Message   string
}

// Path formats the finding's location, or empty if unattached.
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
	// Diagnostics are ordered by check ID, identity, field, and message.
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
