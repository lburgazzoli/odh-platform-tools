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
	"testing"
	"time"

	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	runcli "github.com/lburgazzoli/odh-platform-tools/cmd/module/run"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// The live scenarios are sequential by design: they share one engine
// cluster, and every test relies on the fail-closed precheck over its
// own fresh unique identities.

// TestRunLiveHelmAppliesAndOwns proves the complete Helm chain on a
// real server: the bootstrapped definition CRD, the persisted owner
// with a stable server identity and a normalized unexpanded spec, the
// payload with real placement, references, co-located entries,
// authored values, shared main/init images, main-only injected env
// with the valueFrom removed, the platform config map, and the
// payload-supplied Namespace created by the framework apply without
// owner references — plus the idempotent rerun keeping the owner.
func TestRunLiveHelmAppliesAndOwns(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.createNamespace(t, s.targetNS)
	s.writeHelmModule(t, workloadImage, nil)

	imagesFile := s.imagesFile(t, "RELATED_IMAGE_ALPHA=quay.io/example/alpha:v3\n")

	report, stderr, exitCode := s.runCLI(t, "--related-images-file", imagesFile)

	g.Expect(stderr).To(BeEmpty())
	g.Expect(exitCode).To(Equal(runcli.ExitSuccess))
	g.Expect(report.Status).To(Equal(runcli.StatusApplied))
	g.Expect(report.ApplyCompleted).To(BeTrue())
	g.Expect(report.Wait).To(BeNil())
	g.Expect(report.Owner).NotTo(BeNil())
	g.Expect(report.Owner.Name).To(Equal(s.name))
	g.Expect(report.Owner.Kind).To(Equal("PlatformModuleDefinition"))
	g.Expect(report.Owner.UID).NotTo(BeEmpty())

	s.trackRunObjects(t)

	// The persisted definition keeps the authored, unexpanded spec —
	// the chart path and the authored values — and its server
	// identity is exactly the reported owner.
	definition := s.definition(t)

	g.Expect(definition.UID).To(Equal(types.UID(report.Owner.UID)))
	g.Expect(definition.Spec.Manifests.Format).To(Equal(v1alpha1.FormatHelm))
	g.Expect(definition.Spec.Manifests.Helm.Path).To(Equal("./manifests/chart"))
	g.Expect(string(definition.Spec.Manifests.Helm.Values["message"].Raw)).To(Equal(`"authored-value"`))

	// The payload CRD is established on the server.
	crdEstablished := false

	for _, condition := range s.payloadCRD(t).Status.Conditions {
		if condition.Type == "Established" && condition.Status == "True" {
			crdEstablished = true
		}
	}

	g.Expect(crdEstablished).To(BeTrue())

	// The controller Deployment: authored replica tuning, the shared
	// main/init literal image with no hash, the injected env on the
	// main container only, with the chart's valueFrom replaced.
	deployment := s.deployment(t, s.targetNS, "synthetic-controller")

	g.Expect(*deployment.Spec.Replicas).To(Equal(int32(2)))

	installer := containerByName(t, deployment.Spec.Template.Spec, "installer")
	manager := containerByName(t, deployment.Spec.Template.Spec, "manager")

	g.Expect(installer.Image).To(Equal(workloadImage))
	g.Expect(manager.Image).To(Equal(workloadImage))

	level, present := envVar(manager, "LOG_LEVEL")
	g.Expect(present).To(BeTrue())
	g.Expect(level.Value).To(Equal("debug"))
	g.Expect(level.ValueFrom).To(BeNil())

	applications, present := envVar(manager, "APPLICATIONS_NAMESPACE")
	g.Expect(present).To(BeTrue())
	g.Expect(applications.Value).To(Equal(s.targetNS))

	platformType, present := envVar(manager, "ODH_MODULE_OPERATOR_PLATFORM_TYPE")
	g.Expect(present).To(BeTrue())
	g.Expect(platformType.Value).To(Equal("OpenDataHub"))

	relatedImage, present := envVar(manager, "RELATED_IMAGE_ALPHA")
	g.Expect(present).To(BeTrue())
	g.Expect(relatedImage.Value).To(Equal("quay.io/example/alpha:v3"))

	_, present = envVar(installer, "LOG_LEVEL")
	g.Expect(present).To(BeFalse())
	_, present = envVar(installer, "APPLICATIONS_NAMESPACE")
	g.Expect(present).To(BeFalse())

	// The RBAC references point inside the real placement.
	binding := s.roleBinding(t, s.targetNS, "synthetic-binding")
	g.Expect(binding.RoleRef.Name).To(Equal("synthetic-role"))
	g.Expect(binding.Subjects).To(HaveLen(1))
	g.Expect(binding.Subjects[0].Namespace).To(Equal(s.targetNS))

	// The platform config map carries the platform version.
	platformConfig := s.configMap(t, s.targetNS, "odh-"+s.name+"-config")
	g.Expect(platformConfig.Data["platformVersion"]).To(Equal("2.19.0"))

	// Co-located entries: the same resource name in two real
	// namespaces, with distinct data.
	g.Expect(s.configMap(t, s.targetNS, "shared").Data["placement"]).To(Equal("target"))
	g.Expect(s.configMap(t, s.auxNS, "shared").Data["placement"]).To(Equal("aux"))

	// Authored values reach the server, gate-labeled maps stay
	// ordinary resources, and payload objects carry the owner
	// reference with the definition's real UID.
	values := s.configMap(t, s.targetNS, "values-cm")
	g.Expect(values.Data["message"]).To(Equal("authored-value"))
	g.Expect(values.OwnerReferences).To(HaveLen(1))
	g.Expect(values.OwnerReferences[0].UID).To(Equal(definition.UID))
	g.Expect(values.OwnerReferences[0].Kind).To(Equal("PlatformModuleDefinition"))

	g.Expect(s.configMap(t, s.targetNS, "gate-cm").Labels["app.opendatahub.io/upgrade-gate"]).
		To(Equal("true"))

	// The payload-supplied Namespace was created by the framework
	// apply and left without owner references.
	g.Expect(s.namespace(t, s.auxNS).OwnerReferences).To(BeEmpty())

	// The idempotent rerun keeps the owner identity.
	rerun, _, rerunExit := s.runCLI(t, "--related-images-file", imagesFile)

	g.Expect(rerunExit).To(Equal(runcli.ExitSuccess))
	g.Expect(rerun.Status).To(Equal(runcli.StatusApplied))
	g.Expect(rerun.Owner.UID).To(Equal(report.Owner.UID))
	g.Expect(s.definition(t).UID).To(Equal(definition.UID))
}

// TestRunLiveHelmSpecUpdateAcrossNamespaces proves the persisted
// owner survives spec updates and target namespace changes: the
// definition keeps its identity, the new namespace carries the
// updated workload, and the old namespace is never pruned or
// migrated.
func TestRunLiveHelmSpecUpdateAcrossNamespaces(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.createNamespace(t, s.targetNS)
	s.writeHelmModule(t, workloadImage, nil)

	first, _, exitCode := s.runCLI(t)

	g.Expect(exitCode).To(Equal(runcli.ExitSuccess))
	g.Expect(first.Owner).NotTo(BeNil())

	s.trackRunObjects(t)

	// The updated authored values, rendered into a second target
	// namespace.
	s.message = "updated-value"
	s.replicas = "3"

	secondNS := "run2-" + s.suffix
	s.createNamespace(t, secondNS)
	s.runNS = secondNS
	s.writeHelmModule(t, workloadImage, nil)

	second, _, exitCode := s.runCLI(t)

	g.Expect(exitCode).To(Equal(runcli.ExitSuccess))
	g.Expect(second.Owner.UID).To(Equal(first.Owner.UID))

	// The persisted spec is the updated one.
	definition := s.definition(t)
	g.Expect(string(definition.Spec.Manifests.Helm.Values["message"].Raw)).To(Equal(`"updated-value"`))

	// The new namespace carries the updated workload; the old one is
	// untouched — no auto-pruning, no migration promise.
	updated := s.deployment(t, secondNS, "synthetic-controller")
	g.Expect(*updated.Spec.Replicas).To(Equal(int32(3)))
	g.Expect(s.configMap(t, secondNS, "values-cm").Data["message"]).To(Equal("updated-value"))

	previous := s.deployment(t, s.targetNS, "synthetic-controller")
	g.Expect(*previous.Spec.Replicas).To(Equal(int32(2)))
	g.Expect(s.configMap(t, s.targetNS, "values-cm").Data["message"]).To(Equal("authored-value"))
}

// TestRunLiveImageSourcePrecedence proves the related-image source
// contract on the real chain: the environment wins over the file.
func TestRunLiveImageSourcePrecedence(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.createNamespace(t, s.targetNS)

	// The controller image is a whole-token related-image reference.
	s.writeHelmModule(t, sourceImageRef, nil)

	imagesFile := s.imagesFile(t, "RELATED_IMAGE_ALPHA=quay.io/example/alpha:file\n")

	report, _, exitCode := s.runCLIWithEnv(t,
		[]string{"RELATED_IMAGE_ALPHA=quay.io/example/alpha:env"},
		"--related-images-file", imagesFile)

	g.Expect(exitCode).To(Equal(runcli.ExitSuccess))
	g.Expect(report.Status).To(Equal(runcli.StatusApplied))

	s.trackRunObjects(t)

	deployment := s.deployment(t, s.targetNS, "synthetic-controller")
	manager := containerByName(t, deployment.Spec.Template.Spec, "manager")

	g.Expect(manager.Image).To(Equal("quay.io/example/alpha:env"))
}

// TestRunLiveKustomizeApplies proves the Kustomize chain on a real
// server: the engine's namespace plugin places the payload, the
// RoleBinding subject's namespace is really rewritten, and the
// payload-supplied Namespace is created unowned.
func TestRunLiveKustomizeApplies(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.createNamespace(t, s.targetNS)
	s.writeKustomizeModule(t, workloadImage)

	report, _, exitCode := s.runCLI(t)

	g.Expect(exitCode).To(Equal(runcli.ExitSuccess))
	g.Expect(report.Status).To(Equal(runcli.StatusApplied))
	g.Expect(report.Owner).NotTo(BeNil())

	s.trackRunObjects(t)

	deployment := s.deployment(t, s.targetNS, "synthetic-controller")

	g.Expect(*deployment.Spec.Replicas).To(Equal(int32(2)))

	installer := containerByName(t, deployment.Spec.Template.Spec, "installer")
	manager := containerByName(t, deployment.Spec.Template.Spec, "manager")

	g.Expect(installer.Image).To(Equal(workloadImage))
	g.Expect(manager.Image).To(Equal(workloadImage))

	level, present := envVar(manager, "LOG_LEVEL")
	g.Expect(present).To(BeTrue())
	g.Expect(level.Value).To(Equal("debug"))
	g.Expect(level.ValueFrom).To(BeNil())

	binding := s.roleBinding(t, s.targetNS, "synthetic-binding")
	g.Expect(binding.Subjects).To(HaveLen(1))
	g.Expect(binding.Subjects[0].Namespace).To(Equal(s.targetNS))

	g.Expect(s.configMap(t, s.targetNS, "shared").Data["placement"]).To(Equal("target"))

	values := s.configMap(t, s.targetNS, "values-cm")
	g.Expect(values.Data["message"]).To(Equal("authored-value"))
	g.Expect(values.OwnerReferences).To(HaveLen(1))

	g.Expect(s.namespace(t, s.auxNS).OwnerReferences).To(BeEmpty())
}

// TestRunLiveForeignAdoption proves the approved production
// behavior over a foreign, unowned object: the force apply lands the
// payload data, merges with the foreign fields, and adopts the
// object under the definition owner.
func TestRunLiveForeignAdoption(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.createNamespace(t, s.targetNS)

	// The foreign object exists before the run, unowned, with data
	// the payload does not carry.
	foreign := &corev1.ConfigMap{}
	foreign.SetName("adopted-cm")
	foreign.SetNamespace(s.targetNS)
	foreign.Data = map[string]string{"foreign": "true"}

	g.Expect(engine(t).Client().Create(t.Context(), foreign)).To(Succeed())
	s.track(t, configMapGVK, s.targetNS, "adopted-cm")

	s.writeHelmModule(t, workloadImage, map[string]string{
		"foreign-cm.yaml": foreignConfigMapTemplate,
	})

	report, _, exitCode := s.runCLI(t)

	g.Expect(exitCode).To(Equal(runcli.ExitSuccess))
	g.Expect(report.Status).To(Equal(runcli.StatusApplied))

	s.trackRunObjects(t)

	adopted := s.configMap(t, s.targetNS, "adopted-cm")

	g.Expect(adopted.Data["payload"]).To(Equal("value"))
	g.Expect(adopted.Data["foreign"]).To(Equal("true"))
	g.Expect(adopted.OwnerReferences).To(HaveLen(1))
	g.Expect(adopted.OwnerReferences[0].UID).To(Equal(types.UID(report.Owner.UID)))
}

// TestRunLiveReservedPayloadRejectedAtRender proves the reserved
// resource guard on the real chain: rendering the configured module
// type's own kind is rejected before any write, with the definition
// never persisted.
func TestRunLiveReservedPayloadRejectedAtRender(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.writeHelmModule(t, workloadImage, map[string]string{
		"reserved.yaml": reservedResourceTemplate,
	})

	report, _, exitCode := s.runCLI(t)

	g.Expect(exitCode).To(Equal(runcli.ExitFailure))
	g.Expect(report.Status).To(Equal(runcli.StatusFailed))
	g.Expect(report.Failure).NotTo(BeNil())
	g.Expect(report.Failure.Category).To(Equal(runcli.FailureRuntime))
	g.Expect(report.Attempted).To(Equal("render"))
	g.Expect(report.Completed).To(Equal("load"))
	g.Expect(report.Owner).To(BeNil())

	// Nothing was written.
	found := &unstructured.Unstructured{}
	found.SetGroupVersionKind(definitionGVK)

	err := engine(t).Client().Get(t.Context(), crclient.ObjectKey{Name: s.name}, found)
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
}

// TestRunLiveInvalidPayloadRetainsOwner proves a real admission
// failure is bounded and reported as partial state: the owner stays
// confirmed, the payload applies continue past the rejected object
// (the framework deploys with continue-on-error), the valid objects
// stand, and only the object the server rejected is absent.
func TestRunLiveInvalidPayloadRetainsOwner(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.createNamespace(t, s.targetNS)
	s.writeHelmModule(t, workloadImage, map[string]string{
		"invalid-cm.yaml": invalidConfigMapTemplate,
	})

	report, _, exitCode := s.runCLI(t)

	g.Expect(exitCode).To(Equal(runcli.ExitFailure))
	g.Expect(report.Status).To(Equal(runcli.StatusFailed))
	g.Expect(report.Failure).NotTo(BeNil())
	g.Expect(report.Failure.Category).To(Equal(runcli.FailureRuntime))
	g.Expect(report.Attempted).To(Equal("deploy"))
	g.Expect(report.Completed).To(Equal("render"))
	g.Expect(report.Owner).NotTo(BeNil())
	g.Expect(report.ApplyCompleted).To(BeFalse())

	// The confirmed owner is retained on the server.
	definition := s.definition(t)
	g.Expect(definition.UID).To(Equal(types.UID(report.Owner.UID)))

	s.trackRunObjects(t)

	// The object the server rejected is absent; everything else the
	// action could apply stands as partial state.
	rejected := &unstructured.Unstructured{}
	rejected.SetGroupVersionKind(configMapGVK)

	err := engine(t).Client().Get(t.Context(),
		crclient.ObjectKey{Namespace: s.targetNS, Name: "invalid-cm"}, rejected)
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

	g.Expect(s.configMap(t, s.targetNS, "values-cm").Data["message"]).To(Equal("authored-value"))

	deployment := s.deployment(t, s.targetNS, "synthetic-controller")
	manager := containerByName(t, deployment.Spec.Template.Spec, "manager")
	g.Expect(manager.Image).To(Equal(workloadImage))
}

// TestRunLiveDefaultDoesNotWaitForWorkload proves default command
// success is apply completion alone: an intentionally nonconverged
// workload never blocks the run and the report claims no wait.
func TestRunLiveDefaultDoesNotWaitForWorkload(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.createNamespace(t, s.targetNS)
	s.writeHelmModule(t, unpullableImage, nil)

	started := time.Now()

	report, _, exitCode := s.runCLI(t)

	g.Expect(exitCode).To(Equal(runcli.ExitSuccess))
	g.Expect(report.Status).To(Equal(runcli.StatusApplied))
	g.Expect(report.ApplyCompleted).To(BeTrue())
	g.Expect(report.Wait).To(BeNil())
	g.Expect(time.Since(started)).To(BeNumerically("<", 2*time.Minute))

	s.trackRunObjects(t)

	// The workload exists and can never converge: the unpullable
	// image is exactly what was applied, no hash.
	deployment := s.deployment(t, s.targetNS, "synthetic-controller")
	manager := containerByName(t, deployment.Spec.Template.Spec, "manager")
	g.Expect(manager.Image).To(Equal(unpullableImage))
}

// TestRunLiveWaitCompletesRollout proves the opt-in wait observes
// the real controller rollout — with a file-resolved image source
// converging through the pinned generic image — and reports exactly
// the selected controller Deployment as its target.
func TestRunLiveWaitCompletesRollout(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.createNamespace(t, s.targetNS)

	s.writeHelmModule(t, sourceImageRef, nil)

	imagesFile := s.imagesFile(t, "RELATED_IMAGE_ALPHA="+workloadImage+"\n")

	report, _, exitCode := s.runCLI(t,
		"--related-images-file", imagesFile,
		"--wait",
		"--wait-timeout", "3m")

	g.Expect(exitCode).To(Equal(runcli.ExitSuccess))
	g.Expect(report.Status).To(Equal(runcli.StatusApplied))
	g.Expect(report.Wait).NotTo(BeNil())
	g.Expect(report.Wait.Requested).To(BeTrue())
	g.Expect(report.Wait.Completed).To(BeTrue())
	g.Expect(report.Wait.Targets).To(Equal([]runcli.Target{
		{Namespace: s.targetNS, Name: "synthetic-controller"},
	}))

	s.trackRunObjects(t)

	// The file-resolved image converged: the rollout really finished.
	g.Eventually(func() int32 {
		return s.deployment(t, s.targetNS, "synthetic-controller").Status.ReadyReplicas
	}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Equal(int32(2)))

	manager := containerByName(t,
		s.deployment(t, s.targetNS, "synthetic-controller").Spec.Template.Spec, "manager")
	g.Expect(manager.Image).To(Equal(workloadImage))
}

// TestRunLiveWaitShortTimeoutRetainsOwner proves a wait that cannot
// complete within its explicit budget fails the command — the
// deadline classifies as a cancellation, which always wins — while
// the owner and the apply completion stay confirmed.
func TestRunLiveWaitShortTimeoutRetainsOwner(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.createNamespace(t, s.targetNS)

	s.writeHelmModule(t, sourceImageRef, nil)

	imagesFile := s.imagesFile(t, "RELATED_IMAGE_ALPHA="+unpullableImage+"\n")

	started := time.Now()

	report, _, exitCode := s.runCLI(t,
		"--related-images-file", imagesFile,
		"--wait",
		"--wait-timeout", "15s")

	g.Expect(exitCode).To(Equal(runcli.ExitFailure))
	g.Expect(report.Status).To(Equal(runcli.StatusFailed))
	g.Expect(report.Failure).NotTo(BeNil())
	g.Expect(report.Failure.Category).To(Equal(runcli.FailureCancelled))
	g.Expect(report.Owner).NotTo(BeNil())
	g.Expect(report.ApplyCompleted).To(BeTrue())
	g.Expect(report.Wait).NotTo(BeNil())
	g.Expect(report.Wait.Completed).To(BeFalse())
	g.Expect(report.Wait.Targets).To(Equal([]runcli.Target{
		{Namespace: s.targetNS, Name: "synthetic-controller"},
	}))
	g.Expect(time.Since(started)).To(BeNumerically(">=", 15*time.Second))
	g.Expect(time.Since(started)).To(BeNumerically("<", 2*time.Minute))

	// The confirmed owner is retained on the server.
	definition := s.definition(t)
	g.Expect(definition.UID).To(Equal(types.UID(report.Owner.UID)))

	s.trackRunObjects(t)
}

// TestRunLiveWaitOverallDeadlineWins proves the requested wait
// budget can never extend the overall invocation deadline.
func TestRunLiveWaitOverallDeadlineWins(t *testing.T) {
	g := NewWithT(t)

	s := newScenario(t)
	s.failClosed(t)
	s.createNamespace(t, s.targetNS)

	s.writeHelmModule(t, sourceImageRef, nil)

	imagesFile := s.imagesFile(t, "RELATED_IMAGE_ALPHA="+unpullableImage+"\n")

	started := time.Now()

	report, _, exitCode := s.runCLI(t,
		"--related-images-file", imagesFile,
		"--wait",
		"--wait-timeout", "10m",
		"--timeout", "30s")

	g.Expect(exitCode).To(Equal(runcli.ExitFailure))
	g.Expect(report.Status).To(Equal(runcli.StatusFailed))
	g.Expect(report.Owner).NotTo(BeNil())
	g.Expect(report.ApplyCompleted).To(BeTrue())

	if report.Wait != nil {
		g.Expect(report.Wait.Completed).To(BeFalse())
	}

	// The overall deadline won: far short of the requested wait
	// budget.
	elapsed := time.Since(started)
	g.Expect(elapsed).To(BeNumerically("<", 2*time.Minute))

	s.trackRunObjects(t)
}
