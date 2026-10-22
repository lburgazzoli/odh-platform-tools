// Package checks composes the default module manifest check set,
// mirroring the compliance checks the opendatahub-operator applies to
// its own module manifests (manifests_compliance_test.go). Each check
// lives in its own package with a Validate function; callers needing
// a different check set compose their own function over them.
package checks

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/crd"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/deployments"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/types"
)

// Validate runs every default module manifest check and collects the
// findings. The findings are returned unsorted; the linter action
// aggregates and sorts them deterministically. A check that fails to
// evaluate aborts the run with its error.
func Validate(
	definition config.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) ([]linter.Diagnostic, error) {
	var diagnostics []linter.Diagnostic

	for _, check := range []linter.Validation{
		types.Validate,
		deployments.Validate,
		crd.Validate,
	} {
		findings, err := check(definition, resources)
		if err != nil {
			return nil, err
		}

		diagnostics = append(diagnostics, findings...)
	}

	return diagnostics, nil
}
