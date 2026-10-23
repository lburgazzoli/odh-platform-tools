// Package crd implements the module CRD presence check: a module
// bundle must contain exactly one CustomResourceDefinition for the
// custom resource type the module owns.
package crd

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	gvk "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/jq"
)

const (
	// Missing reports a module bundle without the CRD for its own
	// resource type.
	Missing = "MODULE_CRD_MISSING"

	// Duplicate reports a module bundle with more than one CRD for
	// its own resource type.
	Duplicate = "MODULE_CRD_DUPLICATE"
)

// CRD is the module CRD presence check.
type CRD struct{}

// Validate reports the module unless its bundle contains exactly one
// CustomResourceDefinition declaring the module's own group and kind.
func (CRD) Validate(
	definition v1alpha1.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) ([]linter.Diagnostic, error) {
	var matches []*unstructured.Unstructured

	for i := range resources {
		object := &resources[i]

		if object.GroupVersionKind().GroupKind() != gvk.CustomResourceDefinition.GroupKind() {
			continue
		}

		// A CRD declares its type through spec.group and
		// spec.names.kind, not its own metadata. A missing field is
		// null and simply does not match; a failed evaluation is an
		// error, never a silent non-match.
		match, err := jq.Matchesf(
			object,
			`.spec.group == %q and .spec.names.kind == %q`,
			definition.Spec.Instance.Type.Group,
			definition.Spec.Instance.Type.Kind,
		)

		if err != nil {
			return nil, fmt.Errorf("match CustomResourceDefinition %q against module type %q: %w",
				object.GetName(),
				definition.Spec.Instance.Type.String(),
				err,
			)
		}

		if match {
			matches = append(matches, object)
		}
	}

	if len(matches) == 1 {
		return nil, nil
	}

	diagnostic := linter.Diagnostic{
		Severity: linter.SeverityError,
		GVK:      definition.Spec.Instance.Type.GroupVersionKind(),
	}

	switch len(matches) {
	case 0:
		diagnostic.ID = Missing
		diagnostic.Message = fmt.Sprintf(
			"no CustomResourceDefinition for %q in the rendered set", definition.Spec.Instance.Type.String())
	default:
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.GetName())
		}
		slices.Sort(names)

		diagnostic.ID = Duplicate
		diagnostic.Message = fmt.Sprintf(
			"multiple CustomResourceDefinitions for %q: %s", definition.Spec.Instance.Type.String(), strings.Join(names, ", "))
	}

	return []linter.Diagnostic{diagnostic}, nil
}

// Validate runs the check.
func Validate(
	definition v1alpha1.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) ([]linter.Diagnostic, error) {
	v := CRD{}

	return v.Validate(definition, resources)
}
