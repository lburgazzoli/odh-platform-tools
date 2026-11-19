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

package validator

import (
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/adapt"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// TestOptionsValidate checks the invocation-settings boundary: the
// namespace may stay unresolved, and failures carry the invalid-options
// category, not the definition category.
func TestOptionsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options Options
		invalid bool
	}{
		{
			name: "valid with unresolved namespace",
			options: Options{
				DefinitionFile: "definition.yaml",
				Timeout:        DefaultTimeout,
			},
		},
		{
			name: "valid with resolved namespace",
			options: Options{
				DefinitionFile: "definition.yaml",
				Namespace:      "opendatahub",
				Timeout:        time.Second,
			},
		},
		{
			name: "blank definition file",
			options: Options{
				DefinitionFile: "  ",
				Timeout:        time.Second,
			},
			invalid: true,
		},
		{
			name: "zero timeout",
			options: Options{
				DefinitionFile: "definition.yaml",
			},
			invalid: true,
		},
		{
			name: "negative timeout",
			options: Options{
				DefinitionFile: "definition.yaml",
				Timeout:        -time.Second,
			},
			invalid: true,
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			err := test.options.Validate()

			if test.invalid {
				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.Is(err, moduleerrors.ErrInvalidValidateOptions)).To(BeTrue())
				g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeFalse())

				return
			}

			g.Expect(err).NotTo(HaveOccurred())
		})
	}
}

// TestSelectorsFromDefinition checks selector derivation from a loaded,
// defaulted definition, including the ConfigMap naming rule.
func TestSelectorsFromDefinition(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	explicit := v1alpha1.PlatformModuleDefinition{}
	explicit.Name = "mock"
	explicit.Spec.Instance.Name = "mock-instance"
	explicit.Spec.Instance.Type = v1alpha1.ModuleType{
		Group:   "mock.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "MockModule",
	}
	explicit.Spec.Controller.Deployment.Name = "mock-controller"

	selectors := SelectorsFromDefinition(explicit)

	g.Expect(selectors).To(Equal(Selectors{
		DefinitionName: "mock",
		Module: schema.GroupVersionKind{
			Group:   "mock.opendatahub.io",
			Version: "v1alpha1",
			Kind:    "MockModule",
		},
		ModuleName:     "mock-instance",
		DeploymentName: "mock-controller",
		ConfigMapName:  "odh-mock-config",
	}))

	fallback := v1alpha1.PlatformModuleDefinition{}
	fallback.Name = "mock"
	fallback.Spec.Instance.Type = v1alpha1.ModuleType{
		Group:   "mock.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "MockModule",
	}

	// Loader defaults apply before selectors are derived: the instance
	// name falls back to the definition name and the deployment name to
	// the module name.
	fallback.Spec.Instance.Name = fallback.Name

	g.Expect(SelectorsFromDefinition(fallback).ModuleName).To(Equal("mock"))
	g.Expect(SelectorsFromDefinition(fallback).DeploymentName).To(Equal("mock"))
}

// TestValidateSelectors checks local selector validation before any
// cluster access, including the derived ConfigMap name.
func TestValidateSelectors(t *testing.T) {
	t.Parallel()

	valid := Selectors{
		DefinitionName: "mock",
		Module: schema.GroupVersionKind{
			Group:   "mock.opendatahub.io",
			Version: "v1alpha1",
			Kind:    "MockModule",
		},
		ModuleName:     "mock-instance",
		DeploymentName: "mock-controller",
		ConfigMapName:  "odh-mock-config",
	}

	overlength := valid
	overlength.DefinitionName = strings.Repeat("a", 250)
	overlength.ConfigMapName = adapt.PlatformConfigName(overlength.DefinitionName)

	tests := []struct {
		name      string
		selectors Selectors
		namespace string
		problem   string
	}{
		{
			name:      "valid with unresolved namespace",
			selectors: valid,
		},
		{
			name:      "valid with resolved namespace",
			selectors: valid,
			namespace: "opendatahub",
		},
		{
			name: "blank definition name",
			selectors: func() Selectors {
				blank := valid
				blank.DefinitionName = ""
				blank.ConfigMapName = adapt.PlatformConfigName("")

				return blank
			}(),
			problem: "definition name must not be blank",
		},
		{
			name:      "overlength definition name fails on the derived ConfigMap name",
			selectors: overlength,
			problem:   "derived platform ConfigMap name",
		},
		{
			name: "blank instance name",
			selectors: func() Selectors {
				blank := valid
				blank.ModuleName = ""

				return blank
			}(),
			problem: "module instance name must not be blank",
		},
		{
			name: "blank deployment name",
			selectors: func() Selectors {
				blank := valid
				blank.DeploymentName = ""

				return blank
			}(),
			problem: "controller deployment name must not be blank",
		},
		{
			name: "incomplete module type",
			selectors: func() Selectors {
				incomplete := valid
				incomplete.Module = schema.GroupVersionKind{Version: "v1alpha1", Kind: "MockModule"}

				return incomplete
			}(),
			problem: "must set a nonempty group, version, and kind",
		},
		{
			name: "invalid module type group",
			selectors: func() Selectors {
				invalid := valid
				invalid.Module.Group = "Bad Group"

				return invalid
			}(),
			problem: `module instance type group "Bad Group" is not valid`,
		},
		{
			name: "invalid module type version",
			selectors: func() Selectors {
				invalid := valid
				invalid.Module.Version = "1v-Alpha"

				return invalid
			}(),
			problem: `module instance type version "1v-Alpha" is not valid`,
		},
		{
			name: "invalid module type kind",
			selectors: func() Selectors {
				invalid := valid
				invalid.Module.Kind = "Mock_Module"

				return invalid
			}(),
			problem: `module instance type kind "Mock_Module" is not valid`,
		},
		{
			name:      "invalid resolved namespace",
			selectors: valid,
			namespace: "Invalid_NS",
			problem:   "namespace",
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			err := ValidateSelectors(test.selectors, test.namespace)

			if test.problem == "" {
				g.Expect(err).NotTo(HaveOccurred())

				return
			}

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
			g.Expect(err.Error()).To(ContainSubstring(test.problem))
		})
	}
}

// TestValidateNamespace checks the resolved-namespace boundary.
func TestValidateNamespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		namespace string
		valid     bool
	}{
		{name: "dns label", namespace: "opendatahub", valid: true},
		{name: "uppercase", namespace: "OpenDataHub"},
		{name: "underscore", namespace: "open_datahub"},
		{name: "overlength", namespace: strings.Repeat("a", 64)},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			err := ValidateNamespace(test.namespace)

			if test.valid {
				g.Expect(err).NotTo(HaveOccurred())

				return
			}

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
		})
	}
}

// TestResultPassed checks the overall pass rule: every check must pass,
// and skipped or errored checks never count.
func TestResultPassed(t *testing.T) {
	t.Parallel()

	passing := func(status string) CheckResult {
		return CheckResult{ID: CheckModuleAPI, Status: status}
	}

	tests := []struct {
		name   string
		status string
		passed bool
	}{
		{name: "all pass", status: StatusPass, passed: true},
		{name: "one fail", status: StatusFail},
		{name: "one skipped", status: StatusSkipped},
		{name: "one error", status: StatusError},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			result := Result{Checks: []CheckResult{passing(StatusPass), passing(test.status)}}

			g.Expect(result.Passed()).To(Equal(test.passed))
		})
	}

	t.Run("empty cycle", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		g.Expect(Result{}.Passed()).To(BeFalse())
	})
}
