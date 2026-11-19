//go:build e2e

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

package e2e

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	deploycli "github.com/lburgazzoli/odh-platform-tools/cmd/module/deploy"
	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/validator"
)

// The held-negative budget: every degraded state is established and
// observed before validation starts, so a healthy CLI always exhausts
// this budget and reports the held findings.
const validateHeldTimeout = "10s"

// TestValidateRunningModuleExamples exercises the real validate CLI
// against both packaged mock module controllers: the default waiting
// through instance creation, an all-pass report over a quiescent
// module, and run-owned failure states — an unready condition, an
// invalid platform version, and a scaled-zero controller — each held
// through a short budget, reported with its finding, and recovered
// from. The CLI only ever reads; every mutation is test-owned.
func TestValidateRunningModuleExamples(t *testing.T) {
	for _, format := range []toolApi.Format{toolApi.FormatHelm, toolApi.FormatKustomize} {
		t.Run(string(format), func(t *testing.T) {
			g := NewWithT(t)
			s := newScenario(t)
			s.format = format
			s.failClosed(t)
			s.createNamespace(t, s.targetNS)
			s.writeExample(t)

			imageFile := s.imagesFile(t, "RELATED_IMAGE_MOCK_CONTROLLER", controllerImage(t))
			report, stderr, exit := s.runCLI(t, "--related-images-file", imageFile, "--wait")
			g.Expect(exit).To(Equal(deploycli.ExitSuccess), "%+v\n%s", report, stderr)
			g.Expect(report.Wait).To(HaveField("Completed", true))

			if !t.Run("waits for the module instance", func(t *testing.T) {
				s.validateWaitsForInstance(t)
			}) {
				return
			}

			// The controller cleanup is parent-level, like the mock
			// contract suite: registering it inside the first subtest
			// would retire the Deployment before the later ones run.
			instance := s.mockInstance(t)
			s.registerControllerCleanup(t, s.deployment(t, s.targetNS, mockControllerName), instance)

			if !t.Run("all-pass is read-only", func(t *testing.T) {
				s.validateAllPassIsReadOnly(t, instance)
			}) {
				return
			}
			if !t.Run("unready condition", func(t *testing.T) {
				s.validateUnreadyCondition(t, instance)
			}) {
				return
			}
			if !t.Run("invalid platform version", func(t *testing.T) {
				s.validateInvalidPlatformVersion(t, instance)
			}) {
				return
			}
			t.Run("scaled-zero controller", func(t *testing.T) {
				s.validateScaledZeroController(t, instance)
			})
		})
	}
}

// mockInstance returns the scenario's created module instance. It is
// created once by the first subtest and shared by the later ones.
func (s *scenario) mockInstance(t *testing.T) *unstructured.Unstructured {
	t.Helper()

	g := NewWithT(t)

	instance := &unstructured.Unstructured{}
	instance.SetGroupVersionKind(s.moduleGVK())

	g.Expect(engine(t).Client().Get(t.Context(),
		crclient.ObjectKey{Name: "default-" + s.name}, instance)).To(Succeed())

	return instance
}

// validateWaitsForInstance proves one invocation started before the
// module instance exists waits through its absence and succeeds once
// the test creates the instance and the controller reconciles it.
func (s *scenario) validateWaitsForInstance(t *testing.T) {
	t.Helper()

	g := NewWithT(t)

	// The invocation starts against a module API with no instance.
	process := s.startValidateCLI(t, "--timeout", "2m")

	instance := s.createInstanceFromExample(t)

	awaitMockStatus(t, instance, readyMock("True", platformDeployVersion))

	outcome := process.wait(t)

	s.assertAllPass(t, outcome)
	g.Expect(outcome.stderr).To(BeEmpty())
}

// validateAllPassIsReadOnly proves a quiescent all-pass invocation
// leaves the platform ConfigMap byte-identical: UID, resourceVersion,
// and data are unchanged across the invocation.
func (s *scenario) validateAllPassIsReadOnly(t *testing.T, instance *unstructured.Unstructured) {
	t.Helper()

	g := NewWithT(t)

	before := s.ownedPlatformConfig(t)

	outcome := s.runValidateCLI(t)

	s.assertAllPass(t, outcome)
	g.Expect(outcome.stderr).To(BeEmpty())

	after := s.ownedPlatformConfig(t)
	g.Expect(after.UID).To(Equal(before.UID))
	g.Expect(after.ResourceVersion).To(Equal(before.ResourceVersion),
		"the platform ConfigMap changed across a validate invocation")
	g.Expect(after.Data).To(Equal(before.Data))
}

// validateUnreadyCondition proves a valid mock unready condition
// produces a readiness finding while the platform handshake still
// passes, is held through a short budget, and recovers.
func (s *scenario) validateUnreadyCondition(t *testing.T, instance *unstructured.Unstructured) {
	t.Helper()

	g := NewWithT(t)

	setMockAnnotation(t, instance, new(mockUnready))
	awaitMockStatus(t, instance, readyMock("False", platformDeployVersion),
		mockCondition("MockReady", "False"))

	outcome := s.runValidateCLI(t, "--timeout", validateHeldTimeout)

	g.Expect(outcome.exitCode).To(Equal(1), "%+v\n%s", outcome.report, outcome.stderr)
	g.Expect(outcome.stderr).To(ContainSubstring("timeout"))

	g.Expect(outcome.report.Status).To(Equal("failed"))
	g.Expect(outcome.report.Failure).NotTo(BeNil())
	g.Expect(outcome.report.Failure.Category).To(Equal("timeout"))

	checks := validateChecks(outcome.report)
	g.Expect(checks[validator.CheckModuleReady].Status).To(Equal(validator.StatusFail))
	g.Expect(checks[validator.CheckModuleReady].Field).To(Equal("status.phase"))

	// The platform handshake still passes.
	g.Expect(checks[validator.CheckPlatformConfig].Status).To(Equal(validator.StatusPass))
	g.Expect(checks[validator.CheckPlatformConsumed].Status).To(Equal(validator.StatusPass))

	// Restoring health permits a subsequent successful invocation.
	setMockAnnotation(t, instance, nil)
	awaitMockStatus(t, instance, readyMock("True", platformDeployVersion))

	restored := s.runValidateCLI(t)
	s.assertAllPass(t, restored)
}

// validateInvalidPlatformVersion proves an invalid platform version
// fails the ConfigMap check — the controller's retained last-good
// release cannot make validation pass — is held through a short
// budget, and recovers.
func (s *scenario) validateInvalidPlatformVersion(t *testing.T, instance *unstructured.Unstructured) {
	t.Helper()

	g := NewWithT(t)

	s.setPlatformVersion(t, "not-semver")
	// The controller retains its last-good release while failing.
	awaitMockStatus(t, instance, mockInputFailed(platformDeployVersion, configFailureNote))

	outcome := s.runValidateCLI(t, "--timeout", validateHeldTimeout)

	g.Expect(outcome.exitCode).To(Equal(1), "%+v\n%s", outcome.report, outcome.stderr)
	g.Expect(outcome.stderr).To(ContainSubstring("timeout"))

	checks := validateChecks(outcome.report)
	g.Expect(checks[validator.CheckPlatformConfig].Status).To(Equal(validator.StatusFail))
	g.Expect(checks[validator.CheckPlatformConfig].Field).To(Equal("data.platformVersion"))

	// The retained release cannot be compared against an invalid
	// ConfigMap version: consumption is skipped, never passed.
	g.Expect(checks[validator.CheckPlatformConsumed].Status).To(Equal(validator.StatusSkipped))
	g.Expect(checks[validator.CheckDeployment].Status).To(Equal(validator.StatusPass))

	// Restoring valid data permits success.
	s.setPlatformVersion(t, platformDeployVersion)
	awaitMockStatus(t, instance, readyMock("True", platformDeployVersion))

	restored := s.runValidateCLI(t)
	s.assertAllPass(t, restored)
}

// validateScaledZeroController proves a scaled-zero run-owned
// controller Deployment fails at a short explicit budget with the
// Deployment finding asserted, even though the custom resource keeps
// its old healthy status, and recovers.
func (s *scenario) validateScaledZeroController(t *testing.T, instance *unstructured.Unstructured) {
	t.Helper()

	g := NewWithT(t)

	s.scaleController(t, 0)

	outcome := s.runValidateCLI(t, "--timeout", validateHeldTimeout)

	g.Expect(outcome.exitCode).To(Equal(1), "%+v\n%s", outcome.report, outcome.stderr)
	g.Expect(outcome.stderr).To(ContainSubstring("timeout"))

	checks := validateChecks(outcome.report)
	g.Expect(checks[validator.CheckDeployment].Status).To(Equal(validator.StatusFail))
	g.Expect(checks[validator.CheckDeployment].Field).To(Equal("spec.replicas"))

	// The custom resource keeps its old healthy status.
	g.Expect(checks[validator.CheckModuleReady].Status).To(Equal(validator.StatusPass))

	// Scaling back permits a subsequent successful invocation.
	s.scaleController(t, 1)
	s.awaitControllerScale(t, 1)

	restored := s.runValidateCLI(t)
	s.assertAllPass(t, restored)
}

// assertAllPass proves one invocation succeeded with every check
// passing.
func (s *scenario) assertAllPass(t *testing.T, outcome validateCLIOutcome) {
	t.Helper()

	g := NewWithT(t)

	g.Expect(outcome.exitCode).To(Equal(0), "%+v\n%s", outcome.report, outcome.stderr)

	g.Expect(outcome.report.Status).To(Equal("pass"))
	g.Expect(outcome.report.Namespace).To(Equal(s.runNS))
	g.Expect(outcome.report.Failure).To(BeNil())
	g.Expect(outcome.report.Checks).To(HaveLen(7))

	for id, check := range validateChecks(outcome.report) {
		g.Expect(check.Status).To(Equal(validator.StatusPass), "check %s: %s", id, check.Message)
	}
}

// createInstanceFromExample creates the example's module instance
// from its packaged sample, exactly as a module author would.
func (s *scenario) createInstanceFromExample(t *testing.T) *unstructured.Unstructured {
	t.Helper()

	g := NewWithT(t)

	sample, err := os.ReadFile(filepath.Join(s.root, "instance.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	instances, err := k8s.DecodeResources(sample)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(instances).To(HaveLen(1))

	return s.createModuleInstance(t, &instances[0])
}

// platformDeployVersion is the platform version every deploy in this
// suite passes, so the ConfigMap, the module's reported release, and
// validation all agree on one value.
const platformDeployVersion = "2.19.0"
