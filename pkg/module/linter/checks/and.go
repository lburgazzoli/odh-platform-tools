// Package checks composes module manifest checks, mirroring the
// compliance checks the opendatahub-operator applies to its own
// module manifests (manifests_compliance_test.go). Each check lives
// in its own package with a Validate function; And composes them,
// and the wiring site chooses the set.
package checks

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// And composes validations into one that runs each in order and
// concatenates their findings, aborting with the first evaluation
// error. Composing no validation yields no findings.
func And(validations ...linter.Validation) linter.Validation {
	return func(
		definition toolApi.PlatformModuleDefinition,
		resources []unstructured.Unstructured,
	) ([]linter.Diagnostic, error) {
		var diagnostics []linter.Diagnostic

		for _, validation := range validations {
			findings, err := validation(definition, resources)
			if err != nil {
				return nil, err
			}

			diagnostics = append(diagnostics, findings...)
		}

		return diagnostics, nil
	}
}
