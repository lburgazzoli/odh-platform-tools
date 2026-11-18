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
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	authv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	deploycli "github.com/lburgazzoli/odh-platform-tools/cmd/module/deploy"
	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	toolapi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

const mockHealthyExtras = `
- type: Storage
  status: "True"
- type: Notice
  status: "False"
  severity: Info
`

const mockUnready = `
- type: MockReady
  status: "False"
`

const mockDegraded = `
- type: Degraded
  status: "True"
`

const mockInfoDegraded = `
- type: Degraded
  status: "True"
  severity: Info
`

const mockUnknownObservation = `
- type: Probe
  status: "Unknown"
  reason: ObservationPending
  message: waiting for the probe
  observedGeneration: 0
  lastTransitionTime: 2026-04-07T10:02:03.654Z
`

const mockFailedObservation = `
- type: Probe
  status: "False"
`

const mockDuplicateTypes = `
- type: Probe
  status: "True"
- type: Probe
  status: "False"
`

const mockUnknownField = `
- type: Probe
  status: "True"
  unexpected: true
`

const mockReservedReady = `
- type: Ready
  status: "False"
`

const mockReservedProvisioning = `
- type: ProvisioningSucceeded
  status: "False"
`

const mockMalformed = "- ["

func TestMockModuleExamplesPublishStatusUnderPackagedRBAC(t *testing.T) {
	for _, format := range []toolapi.Format{toolapi.FormatHelm, toolapi.FormatKustomize} {
		t.Run(string(format), func(t *testing.T) {
			g := NewWithT(t)
			s := newScenario(t)
			s.format = format
			s.failClosed(t)
			s.createNamespace(t, s.targetNS)
			s.writeExample(t)

			t.Cleanup(func() {
				if t.Failed() {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					s.logControllerOutput(t, ctx)
				}
			})

			imageFile := s.imagesFile(t, "RELATED_IMAGE_MOCK_CONTROLLER", controllerImage(t))
			report, stderr, exit := s.runCLI(t, "--related-images-file", imageFile, "--wait")
			g.Expect(exit).To(Equal(deploycli.ExitSuccess), "%+v\n%s", report, stderr)
			g.Expect(report.Wait).To(HaveField("Completed", true))

			sample, err := os.ReadFile(filepath.Join(s.root, "instance.yaml"))
			g.Expect(err).NotTo(HaveOccurred())
			instances, err := k8s.DecodeResources(sample)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(instances).To(HaveLen(1))
			annotations := maps.Clone(instances[0].GetAnnotations())
			g.Expect(annotations).To(HaveKey(mockAnnotation))
			authored := annotations[mockAnnotation]
			annotations[mockAnnotation] = mockMalformed
			instances[0].SetAnnotations(annotations)
			instance := s.createModuleInstance(t, &instances[0])
			s.registerControllerCleanup(t, s.deployment(t, s.targetNS, mockControllerName), instance)

			// The first reconciliation has no prior persisted status to
			// supply an observed generation or a release.
			g.Expect(instance.GetGeneration()).To(BeNumerically(">", 0))
			awaitMockStatus(t, instance, mockInputFailed("", "InvalidMockAnnotation"))
			setMockAnnotation(t, instance, &authored)
			awaitMockStatus(t, instance, readyMock("True", "2.19.0"))
			setMockAnnotation(t, instance, nil)
			if !t.Run("condition controls and rollout", func(t *testing.T) { s.exerciseMockConditions(t, instance, imageFile) }) {
				return
			}
			if !t.Run("platform config watch and restart", func(t *testing.T) { s.exercisePlatformConfig(t, instance) }) {
				return
			}
			t.Run("status denial records a Kubernetes event", func(t *testing.T) { s.exerciseMockEventRBAC(t, instance) })
		})
	}
}

func (s *scenario) exerciseMockConditions(t *testing.T, instance *unstructured.Unstructured, imageFile string) {
	g := NewWithT(t)
	initial := awaitMockStatus(t, instance, readyMock("True", "2.19.0"),
		mockCondition("MockReady", "True"), mockCondition("Degraded", "False"), absentMockCondition("Progressing"))
	initialReadyTime := initial.Conditions["Ready"]["lastTransitionTime"]

	awaitNextTransitionSecond(t)
	setMockAnnotation(t, instance, new(mockHealthyExtras))
	awaitMockStatus(t, instance, readyMock("True", "2.19.0"),
		mockCondition("Ready", "True", HaveKeyWithValue("lastTransitionTime", initialReadyTime)),
		mockCondition("Storage", "True"),
		mockCondition("Notice", "False", HaveKeyWithValue("severity", "Info")))
	awaitNextTransitionSecond(t)
	setMockAnnotation(t, instance, nil)
	awaitMockStatus(t, instance, readyMock("True", "2.19.0"),
		mockCondition("Ready", "True", HaveKeyWithValue("lastTransitionTime", initialReadyTime)),
		absentMockCondition("Storage"), absentMockCondition("Notice"))

	setMockAnnotation(t, instance, new(mockUnready))
	awaitMockStatus(t, instance, readyMock("False", "2.19.0"),
		mockCondition("MockReady", "False", HaveKeyWithValue("reason", "MockReadyFalse"), HaveKeyWithValue("message", "MockReadyFalse")))

	report, stderr, exit := s.runCLI(t, "--related-images-file", imageFile, "--wait")
	g.Expect(exit).To(Equal(deploycli.ExitSuccess), "%+v\n%s", report, stderr)
	g.Expect(report.Wait).To(HaveField("Completed", true))
	awaitMockStatus(t, instance, readyMock("False", "2.19.0"), mockCondition("MockReady", "False"))
	setMockAnnotation(t, instance, nil)
	awaitMockStatus(t, instance, readyMock("True", "2.19.0"), mockCondition("MockReady", "True"))

	setMockAnnotation(t, instance, new(mockDegraded))
	awaitMockStatus(t, instance, readyMock("False", "2.19.0"), mockCondition("Degraded", "True"))
	setMockAnnotation(t, instance, new(mockInfoDegraded))
	awaitMockStatus(t, instance, readyMock("True", "2.19.0"),
		mockCondition("Degraded", "True", HaveKeyWithValue("severity", "Info")))

	setMockAnnotation(t, instance, new(mockUnknownObservation))
	awaitMockStatus(t, instance, readyMock("Unknown", "2.19.0"), mockCondition("Probe", "Unknown",
		HaveKeyWithValue("reason", "ObservationPending"),
		HaveKeyWithValue("message", "waiting for the probe"),
		HaveKeyWithValue("lastTransitionTime", "2026-04-07T10:02:03Z"),
		SatisfyAny(Not(HaveKey("observedGeneration")), HaveKeyWithValue("observedGeneration", BeNumerically("==", 0)))))

	setMockAnnotation(t, instance, new(mockFailedObservation))
	failed := awaitMockStatus(t, instance, readyMock("False", "2.19.0"), mockCondition("Probe", "False",
		HaveKeyWithValue("reason", "ProbeFalse"), HaveKeyWithValue("message", "ProbeFalse"),
		HaveKeyWithValue("observedGeneration", instance.GetGeneration())))

	// A ConfigMap update forces another reconciliation with identical
	// conditions and observable progress through the release update.
	awaitNextTransitionSecond(t)
	s.setPlatformVersion(t, "2.20.0")
	awaitMockStatus(t, instance, readyMock("False", "2.20.0"),
		mockCondition("Probe", "False", HaveKeyWithValue("lastTransitionTime", failed.Conditions["Probe"]["lastTransitionTime"])),
		mockCondition("Ready", "False", HaveKeyWithValue("lastTransitionTime", failed.Conditions["Ready"]["lastTransitionTime"])))

	setMockAnnotation(t, instance, new(mockMalformed))
	awaitMockStatus(t, instance, mockInputFailed("2.20.0", "InvalidMockAnnotation"), absentMockCondition("Probe"))
	setMockAnnotation(t, instance, nil)
	awaitMockStatus(t, instance, readyMock("True", "2.20.0"))

	for _, invalid := range []string{mockDuplicateTypes, mockUnknownField, mockReservedReady, mockReservedProvisioning} {
		setMockAnnotation(t, instance, &invalid)
		awaitMockStatus(t, instance, mockInputFailed("2.20.0", "InvalidMockAnnotation"))
		setMockAnnotation(t, instance, nil)
		awaitMockStatus(t, instance, readyMock("True", "2.20.0"))
	}

	s.setPlatformVersion(t, "2.19.0")
	awaitMockStatus(t, instance, readyMock("True", "2.19.0"))
}

func (s *scenario) exercisePlatformConfig(t *testing.T, instance *unstructured.Unstructured) {
	g := NewWithT(t)
	s.setPlatformVersion(t, "v3.1")
	awaitMockStatus(t, instance, readyMock("True", "3.1.0"))
	s.deletePlatformConfig(t)
	awaitMockStatus(t, instance, mockInputFailed("3.1.0", "InvalidPlatformConfiguration"))

	s.createPlatformConfig(t, "3.2.0")
	awaitMockStatus(t, instance, readyMock("True", "3.2.0"))
	s.setPlatformVersion(t, "")
	awaitMockStatus(t, instance, mockInputFailed("3.2.0", "InvalidPlatformConfiguration"))
	s.setPlatformVersion(t, "3.3.0")
	awaitMockStatus(t, instance, readyMock("True", "3.3.0"))
	s.setPlatformVersion(t, "not-semver")
	awaitMockStatus(t, instance, mockInputFailed("3.3.0", "InvalidPlatformConfiguration"))

	previous := s.awaitControllerPod(t, "")
	g.Expect(engine(t).Client().Delete(t.Context(), &previous, crclient.Preconditions{UID: &previous.UID})).To(Succeed())
	s.awaitControllerPod(t, previous.UID)
	// Force a fresh reconciliation through the replacement pod and prove
	// its version came from the persisted status, including on bad input.
	setMockAnnotation(t, instance, new(mockMalformed))
	awaitMockStatus(t, instance, mockInputFailed("3.3.0", "InvalidMockAnnotation"))
	setMockAnnotation(t, instance, nil)
	awaitMockStatus(t, instance, mockInputFailed("3.3.0", "InvalidPlatformConfiguration"))

	setMockAnnotation(t, instance, new(mockMalformed))
	awaitMockStatus(t, instance, mockInputFailed("3.3.0", "InvalidMockAnnotation"))
	s.setPlatformVersion(t, "2.19.0")
	g.Consistently(func(g Gomega) {
		current := instance.DeepCopy()
		g.Expect(engine(t).Client().Get(t.Context(), crclient.ObjectKeyFromObject(instance), current)).To(Succeed())
		releases, _, err := unstructured.NestedSlice(current.Object, "status", "releases")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(releases).To(ConsistOf(HaveKeyWithValue("version", "3.3.0")))
	}).WithContext(t.Context()).WithTimeout(2 * time.Second).WithPolling(250 * time.Millisecond).Should(Succeed())
	setMockAnnotation(t, instance, nil)
	awaitMockStatus(t, instance, readyMock("True", "2.19.0"))
}

func (s *scenario) exerciseMockEventRBAC(t *testing.T, instance *unstructured.Unstructured) {
	g := NewWithT(t)
	kube := engine(t).Client()
	role := &rbacv1.ClusterRole{}
	g.Expect(kube.Get(t.Context(), crclient.ObjectKey{Name: s.clusterRoleName()}, role)).To(Succeed())
	g.Expect(role.UID).To(Equal(s.trackedUID(clusterRoleGVK, crclient.ObjectKeyFromObject(role))))
	statusResource := s.moduleResource() + "/status"
	limited := slices.Clone(role.Rules)
	index := slices.IndexFunc(limited, func(rule rbacv1.PolicyRule) bool {
		return slices.Equal(rule.APIGroups, []string{s.group}) && slices.Equal(rule.Resources, []string{statusResource})
	})
	g.Expect(index).To(BeNumerically(">=", 0))
	limited[index].Verbs = []string{"get"}

	restorePending := true
	t.Cleanup(func() {
		if restorePending {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := replaceRoleRules(ctx, kube, role, role.Rules); err != nil {
				t.Errorf("restoring owned controller ClusterRole: %v", err)
			}
		}
	})

	account := s.deployment(t, s.targetNS, mockControllerName).Spec.Template.Spec.ServiceAccountName
	s.awaitStatusPatchPermission(t, account, true)
	g.Expect(replaceRoleRules(t.Context(), kube, role, limited)).To(Succeed())
	s.awaitStatusPatchPermission(t, account, false)
	setMockAnnotation(t, instance, new(mockUnready))

	g.Eventually(func(g Gomega) {
		events, err := s.moduleEvents(t.Context(), kube, instance.GetUID())
		g.Expect(err).NotTo(HaveOccurred())
		event, err := matchingFailureEvent(events, instance.GetUID())
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(event.Namespace).To(Equal(metav1.NamespaceDefault))
		g.Expect(event.Regarding.Name).To(Equal(instance.GetName()))
		g.Expect(event.Regarding.Kind).To(Equal(s.moduleGVK().Kind))
		g.Expect(event.Note).To(ContainSubstring("is forbidden"))
		g.Expect(event.Note).To(ContainSubstring(statusResource))
	}).WithContext(t.Context()).WithTimeout(time.Minute).WithPolling(250 * time.Millisecond).Should(Succeed())
	awaitMockStatus(t, instance, readyMock("True", "2.19.0"))

	g.Expect(replaceRoleRules(t.Context(), kube, role, role.Rules)).To(Succeed())
	restorePending = false
	s.awaitStatusPatchPermission(t, account, true)
	awaitMockStatus(t, instance, readyMock("False", "2.19.0"), mockCondition("MockReady", "False"))
}

// Transition times are persisted to whole seconds. Wait an elapsed second
// before changing inputs so an incorrect reset cannot serialize identically.
func awaitNextTransitionSecond(t *testing.T) {
	t.Helper()

	started := time.Now()
	NewWithT(t).Eventually(func() time.Duration {
		return time.Since(started)
	}).WithContext(t.Context()).WithTimeout(4 * time.Second).WithPolling(50 * time.Millisecond).
		Should(BeNumerically(">=", time.Second))
}

func replaceRoleRules(ctx context.Context, kube crclient.Client, owned *rbacv1.ClusterRole, rules []rbacv1.PolicyRule) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &rbacv1.ClusterRole{}
		if err := kube.Get(ctx, crclient.ObjectKeyFromObject(owned), current); err != nil {
			return err
		}
		if current.UID != owned.UID {
			return fmt.Errorf("controller ClusterRole %s has unexpected UID %s", current.Name, current.UID)
		}
		current.Rules = rules
		return kube.Update(ctx, current)
	})
}

func (s *scenario) awaitStatusPatchPermission(t *testing.T, account string, allowed bool) {
	t.Helper()

	g := NewWithT(t)
	kube := engine(t).Client()
	g.Eventually(func(g Gomega) {
		review := &authv1.SubjectAccessReview{Spec: authv1.SubjectAccessReviewSpec{
			User:   "system:serviceaccount:" + s.targetNS + ":" + account,
			Groups: []string{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:" + s.targetNS},
			ResourceAttributes: &authv1.ResourceAttributes{
				Group: s.group, Version: "v1alpha1", Resource: s.moduleResource(),
				Subresource: "status", Verb: "patch", Name: "default-" + s.name,
			},
		}}
		g.Expect(kube.Create(t.Context(), review)).To(Succeed())
		g.Expect(review.Status.EvaluationError).To(BeEmpty())
		g.Expect(review.Status.Allowed).To(Equal(allowed), "%s", review.Status.Reason)
	}).WithContext(t.Context()).WithTimeout(time.Minute).WithPolling(250 * time.Millisecond).Should(Succeed())
}
