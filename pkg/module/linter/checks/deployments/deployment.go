// Package deployments implements the controller Deployment check: a
// module bundle must contain exactly one Deployment, and the
// containers the definition selects must appear exactly once in
// their configured pod spec list.
package deployments

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	gvk "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/jq"
)

const (
	// Missing reports a module bundle without a Deployment.
	Missing = "DEPLOYMENT_MISSING"

	// Multiple reports a module bundle with more than one Deployment.
	Multiple = "DEPLOYMENT_MULTIPLE"

	// ContainerMissing reports a selected container that is not
	// present in its pod spec list.
	ContainerMissing = "CONTAINER_MISSING"

	// ContainerDuplicate reports a selected container that appears
	// more than once in its pod spec list.
	ContainerDuplicate = "CONTAINER_DUPLICATE"

	// ContainerListMismatch reports a selected container that appears
	// in the wrong pod spec list.
	ContainerListMismatch = "CONTAINER_LIST_MISMATCH"
)

// Deployment is the controller Deployment check.
type Deployment struct{}

// Validate reports the module unless its bundle contains exactly one
// Deployment, and reports every container the definition selects that
// does not appear exactly once in its configured pod spec list.
func (Deployment) Validate(
	definition config.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) ([]linter.Diagnostic, error) {
	var deployments []*unstructured.Unstructured
	for i := range resources {
		if resources[i].GroupVersionKind().GroupKind() == gvk.Deployment.GroupKind() {
			deployments = append(deployments, &resources[i])
		}
	}

	if len(deployments) != 1 {
		id := Missing
		if len(deployments) > 1 {
			id = Multiple
		}

		return []linter.Diagnostic{{
			ID:       id,
			Severity: linter.SeverityError,
			GVK:      definition.Spec.Type.GroupVersionKind,
			Message:  fmt.Sprintf("module must bundle exactly one Deployment, found %d", len(deployments)),
		}}, nil
	}

	deployment := deployments[0]

	var diagnostics []linter.Diagnostic

	// A loaded definition has a resolved name for every configured
	// placement; a nil name means the placement declares no
	// expectation and is skipped.
	for _, selection := range []struct {
		role string
		list string
		name *string
	}{
		{"main container", "containers", selectedName(definition.Spec.Controller.Deployment.Container)},
		{"init container", "initContainers", selectedName(definition.Spec.Controller.Deployment.InitContainer)},
	} {
		if selection.name == nil {
			continue
		}

		findings, err := validateContainerSelection(deployment, selection.role, selection.list, *selection.name)
		if err != nil {
			return nil, err
		}

		diagnostics = append(diagnostics, findings...)
	}

	return diagnostics, nil
}

// Validate runs the check.
func Validate(
	definition config.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) ([]linter.Diagnostic, error) {
	v := Deployment{}

	return v.Validate(definition, resources)
}

// selectedName returns the configured container name, or nil when the
// placement is not configured.
func selectedName(container *config.ContainerDefinition) *string {
	if container == nil {
		return nil
	}

	return container.Name
}

// validateContainerSelection checks that the selected container name
// appears exactly once in its pod spec list and never in the other
// one.
func validateContainerSelection(
	deployment *unstructured.Unstructured,
	role, list, name string,
) ([]linter.Diagnostic, error) {
	wrongList := "initContainers"
	if list == "initContainers" {
		wrongList = "containers"
	}

	correct, err := countContainer(deployment, list, name)
	if err != nil {
		return nil, fmt.Errorf("count %s %q in %s of Deployment %q: %w",
			role, name, list, deployment.GetName(), err)
	}

	wrong, err := countContainer(deployment, wrongList, name)
	if err != nil {
		return nil, fmt.Errorf("count %s %q in %s of Deployment %q: %w",
			role, name, wrongList, deployment.GetName(), err)
	}

	diagnostic := linter.Diagnostic{
		Severity:  linter.SeverityError,
		GVK:       deployment.GroupVersionKind(),
		Namespace: deployment.GetNamespace(),
		Name:      deployment.GetName(),
		Field:     "spec.template.spec." + list,
	}

	switch {
	case correct == 0 && wrong > 0:
		diagnostic.ID = ContainerListMismatch
		diagnostic.Message = fmt.Sprintf(
			"selected %s %q appears in %s instead of %s", role, name, wrongList, list)
	case correct == 0:
		diagnostic.ID = ContainerMissing
		diagnostic.Message = fmt.Sprintf(
			"selected %s %q is not present in %s", role, name, list)
	case correct > 1:
		diagnostic.ID = ContainerDuplicate
		diagnostic.Message = fmt.Sprintf(
			"selected %s %q appears %d times in %s, expected exactly once", role, name, correct, list)
	case wrong > 0:
		diagnostic.ID = ContainerListMismatch
		diagnostic.Message = fmt.Sprintf(
			"selected %s %q also appears in %s; it belongs in exactly one list", role, name, wrongList)
	default:
		return nil, nil
	}

	return []linter.Diagnostic{diagnostic}, nil
}

// countContainer counts the containers with the given name in one pod
// spec list of a Deployment. A missing list counts zero; a malformed
// one is an evaluation error, never a silent zero.
func countContainer(deployment *unstructured.Unstructured, list, name string) (int, error) {
	return jq.Countf(deployment,
		`[.spec.template.spec.%s // [] | .[] | select(.name == %q)] | length`, list, name)
}
