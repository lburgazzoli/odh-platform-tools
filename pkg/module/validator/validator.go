/*
Copyright 2026 Luca Burgazzoli

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package validator evaluates the live state of a deployed module against
// the platform contract. Evaluation is a pure function over one observation
// cycle; fetching and polling live state is owned by later layers.
package validator

import (
	"fmt"
	"strings"
	"time"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/adapt"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// DefaultTimeout is the overall validation budget the command applies when
// the caller does not set one.
const DefaultTimeout = 30 * time.Second

// Check identifiers, in the stable report order.
const (
	CheckModuleAPI        = "MODULE_API"
	CheckModuleExists     = "MODULE_EXISTS"
	CheckModuleContract   = "MODULE_CONTRACT"
	CheckModuleReady      = "MODULE_READY"
	CheckDeployment       = "DEPLOYMENT"
	CheckPlatformConfig   = "PLATFORM_CONFIG"
	CheckPlatformConsumed = "PLATFORM_CONSUMED"
)

// Check observation states. A skipped or errored check never counts as a
// pass.
const (
	// StatusPass reports a satisfied check.
	StatusPass = "pass"

	// StatusFail reports an observed defect or a not-yet-healthy state.
	StatusFail = "fail"

	// StatusSkipped reports a check whose required input is unavailable.
	StatusSkipped = "skipped"

	// StatusError reports an observation failure, not an object defect.
	StatusError = "error"
)

// Options configures one validation invocation. The namespace is optional
// here: the command resolves it and re-validates it before observing the
// cluster, so option validation must not require it.
type Options struct {
	// DefinitionFile is the required, explicit module definition path.
	DefinitionFile string

	// Namespace is optional until the command resolves it.
	Namespace string

	// Timeout is the overall budget; it must be positive.
	Timeout time.Duration
}

// Validate checks invocation settings, distinct from definition validity.
// The namespace may be unresolved at this boundary.
func (o Options) Validate() error {
	switch {
	case strings.TrimSpace(o.DefinitionFile) == "":
		return moduleerrors.New(
			moduleerrors.ErrInvalidValidateOptions,
			"validation requires a definition file (--config)",
		)
	case o.Timeout <= 0:
		return moduleerrors.New(
			moduleerrors.ErrInvalidValidateOptions,
			fmt.Sprintf("validation timeout must be positive, got %s", o.Timeout),
		)
	default:
		return nil
	}
}

// Selectors identifies every object one validation observes. It is derived
// from a loaded, defaulted definition before any cluster access.
type Selectors struct {
	// DefinitionName is the module definition name; it also keys the
	// platform ConfigMap naming rule.
	DefinitionName string

	// Module is the cluster-scoped module custom resource type.
	Module schema.GroupVersionKind

	// ModuleName is the module custom resource instance name.
	ModuleName string

	// DeploymentName is the controller Deployment name.
	DeploymentName string

	// ConfigMapName is the derived platform ConfigMap name.
	ConfigMapName string
}

// SelectorsFromDefinition derives selectors from a loaded, defaulted
// definition. Loader defaults fill the instance and container names.
func SelectorsFromDefinition(definition v1alpha1.PlatformModuleDefinition) Selectors {
	return Selectors{
		DefinitionName: definition.Name,
		Module:         definition.Spec.Instance.Type.GroupVersionKind(),
		ModuleName:     definition.Spec.Instance.Name,
		DeploymentName: definition.ControllerDeploymentName(),
		ConfigMapName:  adapt.PlatformConfigName(definition.Name),
	}
}

// ValidateSelectors checks the local inputs validation needs before any
// cluster access: nonblank valid definition, instance, and Deployment
// names; a complete and syntactically valid custom-resource GVK (group a
// DNS subdomain, version a DNS label, kind a DNS label ignoring case,
// mirroring CRD validation); and a derived platform ConfigMap name that
// is itself a valid object name, so an overlength definition name fails
// locally instead of as a repeated live finding. An empty namespace is
// allowed here; ValidateNamespace covers the resolved value.
func ValidateSelectors(selectors Selectors, namespace string) error {
	var problems []string

	appendNameProblem := func(field, name string) {
		if errs := apivalidation.NameIsDNSSubdomain(name, false); len(errs) > 0 {
			problems = append(problems, fmt.Sprintf("%s %q is not a valid object name: %s",
				field, name, strings.Join(errs, ", ")))
		}
	}

	switch {
	case strings.TrimSpace(selectors.DefinitionName) == "":
		problems = append(problems, "definition name must not be blank")
	default:
		appendNameProblem("definition name", selectors.DefinitionName)
	}

	switch {
	case strings.TrimSpace(selectors.ModuleName) == "":
		problems = append(problems, "module instance name must not be blank")
	default:
		appendNameProblem("module instance name", selectors.ModuleName)
	}

	switch {
	case strings.TrimSpace(selectors.DeploymentName) == "":
		problems = append(problems, "controller deployment name must not be blank")
	default:
		appendNameProblem("controller deployment name", selectors.DeploymentName)
	}

	switch {
	case selectors.Module.Empty() ||
		strings.TrimSpace(selectors.Module.Kind) == "" ||
		strings.TrimSpace(selectors.Module.Version) == "" ||
		strings.TrimSpace(selectors.Module.Group) == "":
		problems = append(problems, fmt.Sprintf(
			"module instance type %s must set a nonempty group, version, and kind", selectors.Module))
	default:
		// The syntax rules mirror CRD validation: the group is a DNS
		// subdomain, the version a DNS label, and the kind a DNS label
		// ignoring case, so a malformed GVK fails locally instead of
		// waiting on discovery.
		appendTypeProblem := func(field, value string, errs []string) {
			problems = append(problems, fmt.Sprintf(
				"module instance type %s %q is not valid: %s", field, value, strings.Join(errs, ", ")))
		}

		if errs := utilvalidation.IsDNS1123Subdomain(selectors.Module.Group); len(errs) > 0 {
			appendTypeProblem("group", selectors.Module.Group, errs)
		}

		if errs := utilvalidation.IsDNS1035Label(selectors.Module.Version); len(errs) > 0 {
			appendTypeProblem("version", selectors.Module.Version, errs)
		}

		if errs := utilvalidation.IsDNS1035Label(strings.ToLower(selectors.Module.Kind)); len(errs) > 0 {
			appendTypeProblem("kind", selectors.Module.Kind, errs)
		}
	}

	appendNameProblem("derived platform ConfigMap name", selectors.ConfigMapName)

	if namespace != "" {
		if err := ValidateNamespace(namespace); err != nil {
			problems = append(problems, err.Error())
		}
	}

	if len(problems) == 0 {
		return nil
	}

	return moduleerrors.New(
		moduleerrors.ErrInvalidDefinition,
		"invalid module definition selectors: "+strings.Join(problems, "; "),
	)
}

// ValidateNamespace checks one resolved namespace. The command calls it
// immediately after namespace resolution.
func ValidateNamespace(namespace string) error {
	if errs := apivalidation.NameIsDNSLabel(namespace, false); len(errs) > 0 {
		return moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			fmt.Sprintf("namespace %q is not valid: %s", namespace, strings.Join(errs, ", ")),
		)
	}

	return nil
}

// CheckResult reports one check's observation of one identified object.
type CheckResult struct {
	// ID is one of the Check constants, in stable order.
	ID string

	// Status is one of the Status constants.
	Status string

	// GVK identifies the observed object's type.
	GVK schema.GroupVersionKind

	// Namespace is empty for cluster-scoped objects.
	Namespace string

	// Name is empty for checks that observe a type rather than an object.
	Name string

	// Field optionally names the first offending field path.
	Field string

	// Message is the actionable observation, skip reason, or error text.
	Message string
}

// Result is the outcome of one complete observation cycle.
type Result struct {
	// Namespace is the resolved namespace the cycle observed.
	Namespace string

	// Checks are ordered by the stable check order.
	Checks []CheckResult
}

// Passed reports whether every check in the cycle passed. A skipped or
// errored check never counts as a pass, and an empty cycle does not pass.
func (r Result) Passed() bool {
	if len(r.Checks) == 0 {
		return false
	}

	for i := range r.Checks {
		if r.Checks[i].Status != StatusPass {
			return false
		}
	}

	return true
}
