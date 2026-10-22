// Package deployments implements the controller Deployment check: a
// module bundle must contain exactly one Deployment, and the
// containers the definition selects must exist in its pod spec.
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
)

// Deployment is the controller Deployment check.
type Deployment struct{}

// Validate reports the module unless its bundle contains exactly one
// Deployment, and reports every container the definition selects that
// does not exist in the Deployment's pod spec.
func (d Deployment) Validate(
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
			GVK:      definition.Spec.Instance.Type.GroupVersionKind,
			Message:  fmt.Sprintf("module must bundle exactly one Deployment, found %d", len(deployments)),
		}}, nil
	}

	deployment := deployments[0]

	var diagnostics []linter.Diagnostic

	// The main container is required; the init container is optional
	// and only validated when the definition configures one. A loaded
	// definition has a resolved name for every configured placement.
	if container := definition.Spec.Controller.Deployment.Container; container != nil && container.Name != "" {
		findings, err := d.validateContainerExists(deployment, "containers", container.Name)
		if err != nil {
			return nil, err
		}

		diagnostics = append(diagnostics, findings...)
	}

	if init := definition.Spec.Controller.Deployment.InitContainer; init != nil && init.Name != "" {
		findings, err := d.validateContainerExists(deployment, "initContainers", init.Name)
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

// validateContainerExists reports the selected container unless it is
// present in its pod spec list. A missing list is a clean non-match; a
// malformed one is an evaluation error, never a silent non-match.
func (Deployment) validateContainerExists(
	deployment *unstructured.Unstructured,
	list string,
	name string,
) ([]linter.Diagnostic, error) {
	exists, err := jq.Matchesf(deployment,
		`any(.spec.template.spec.%s // [] | .[]; .name == %q)`, list, name)
	if err != nil {
		return nil, fmt.Errorf("match container %q in %s of Deployment %q: %w",
			name, list, deployment.GetName(), err)
	}

	if exists {
		return nil, nil
	}

	return []linter.Diagnostic{{
		ID:        ContainerMissing,
		Severity:  linter.SeverityError,
		GVK:       deployment.GroupVersionKind(),
		Namespace: deployment.GetNamespace(),
		Name:      deployment.GetName(),
		Field:     "spec.template.spec." + list,
		Message:   fmt.Sprintf("container %q is not present in %s", name, list),
	}}, nil
}
