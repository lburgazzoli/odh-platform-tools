// Package checks composes module manifest validations.
package checks

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// And runs validations in order, combining findings until an evaluation fails.
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
