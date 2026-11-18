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
	"slices"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const mockAnnotation = "mock.platform.opendatahub.io/conditions"
const mockControllerName = "mock-controller"

type observedMockStatus struct {
	Phase      string
	Conditions map[string]map[string]any
	Releases   []any
}

func (s *scenario) createModuleInstance(t *testing.T, sample *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()

	g := NewWithT(t)
	kube := engine(t).Client()
	gvk := s.moduleGVK()
	name := "default-" + s.name
	instance := sample
	if instance == nil {
		instance = &unstructured.Unstructured{}
		instance.SetGroupVersionKind(gvk)
		instance.SetName(name)
	}

	g.Expect(instance.GroupVersionKind()).To(Equal(gvk))
	g.Expect(instance.GetName()).To(Equal(name))
	g.Expect(instance.GetNamespace()).To(BeEmpty())

	found := &unstructured.Unstructured{}
	found.SetGroupVersionKind(gvk)
	err := kube.Get(t.Context(), crclient.ObjectKey{Name: name}, found)
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "deploy must not create the module instance: %v", err)

	wrongName := instance.DeepCopy()
	wrongName.SetName("wrong-" + s.suffix)
	err = kube.Create(t.Context(), wrongName)
	if err == nil {
		s.record(t, gvk, wrongName)
	}
	g.Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the singleton rule must reject a second name: %v", err)

	g.Expect(kube.Create(t.Context(), instance)).To(Succeed())
	s.record(t, gvk, instance)
	return instance
}

func setMockAnnotation(t *testing.T, instance *unstructured.Unstructured, value *string) {
	t.Helper()

	g := NewWithT(t)
	kube := engine(t).Client()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := instance.DeepCopy()
		if err := kube.Get(t.Context(), crclient.ObjectKeyFromObject(instance), current); err != nil {
			return err
		}
		if current.GetUID() != instance.GetUID() {
			return fmt.Errorf("mock instance %s has unexpected UID %s", current.GetName(), current.GetUID())
		}
		before := current.DeepCopy()
		annotations := current.GetAnnotations()
		if value == nil {
			delete(annotations, mockAnnotation)
		} else {
			if annotations == nil {
				annotations = make(map[string]string)
			}
			annotations[mockAnnotation] = *value
		}
		current.SetAnnotations(annotations)
		return kube.Patch(t.Context(), current, crclient.MergeFromWithOptions(before, crclient.MergeFromWithOptimisticLock{}))
	})
	g.Expect(err).To(Succeed())
}

func awaitMockStatus(
	t *testing.T,
	instance *unstructured.Unstructured,
	checks ...gomegatypes.GomegaMatcher,
) observedMockStatus {
	t.Helper()

	g := NewWithT(t)
	kube := engine(t).Client()
	var observed observedMockStatus
	g.Eventually(func(g Gomega) {
		current := instance.DeepCopy()
		g.Expect(kube.Get(t.Context(), crclient.ObjectKeyFromObject(instance), current)).To(Succeed())
		g.Expect(current.GetUID()).To(Equal(instance.GetUID()))
		g.Expect(current.GetGeneration()).To(Equal(instance.GetGeneration()), "annotation/configmap changed metadata.generation")

		generation, found, err := unstructured.NestedInt64(current.Object, "status", "observedGeneration")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(found).To(BeTrue(), "missing status.observedGeneration: %v", current.Object["status"])
		g.Expect(generation).To(Equal(current.GetGeneration()))

		phase, _, err := unstructured.NestedString(current.Object, "status", "phase")
		g.Expect(err).NotTo(HaveOccurred())
		entries, _, err := unstructured.NestedSlice(current.Object, "status", "conditions")
		g.Expect(err).NotTo(HaveOccurred())
		conditions := make(map[string]map[string]any, len(entries))
		for _, entry := range entries {
			condition, ok := entry.(map[string]any)
			g.Expect(ok).To(BeTrue())
			name, ok := condition["type"].(string)
			g.Expect(ok).To(BeTrue())
			g.Expect(conditions).NotTo(HaveKey(name))
			conditions[name] = condition
		}
		releases, _, err := unstructured.NestedSlice(current.Object, "status", "releases")
		g.Expect(err).NotTo(HaveOccurred())

		observed = observedMockStatus{Phase: phase, Conditions: conditions, Releases: releases}
		g.Expect(observed).To(SatisfyAll(checks...))
	}).WithContext(t.Context()).WithTimeout(time.Minute).WithPolling(250 * time.Millisecond).Should(Succeed())

	return observed
}

func readyMock(ready string, version string) gomegatypes.GomegaMatcher {
	phase := "Not Ready"
	if ready == "True" {
		phase = "Ready"
	}

	releases := BeEmpty()
	if version != "" {
		releases = ConsistOf(SatisfyAll(
			HaveKeyWithValue("name", "platform"),
			HaveKeyWithValue("version", version),
		))
	}
	return SatisfyAll(
		HaveField("Phase", phase),
		mockCondition("Ready", ready, HaveKey("lastTransitionTime")),
		mockCondition("ProvisioningSucceeded", "True"),
		HaveField("Releases", releases),
	)
}

func mockCondition(kind string, status string, checks ...gomegatypes.GomegaMatcher) gomegatypes.GomegaMatcher {
	matchers := append([]gomegatypes.GomegaMatcher{HaveKeyWithValue("status", status)}, checks...)
	return HaveField("Conditions", HaveKeyWithValue(kind, SatisfyAll(matchers...)))
}

func absentMockCondition(kind string) gomegatypes.GomegaMatcher {
	return HaveField("Conditions", Not(HaveKey(kind)))
}

func mockInputFailed(version string, reason string) gomegatypes.GomegaMatcher {
	return SatisfyAll(
		readyMock("False", version),
		mockCondition("MockReady", "False", HaveKeyWithValue("reason", reason), HaveKeyWithValue("message", Not(BeEmpty()))),
		mockCondition("Degraded", "True", HaveKeyWithValue("reason", reason), HaveKeyWithValue("message", Not(BeEmpty()))),
	)
}

func (s *scenario) setPlatformVersion(t *testing.T, version string) {
	t.Helper()

	current := s.ownedPlatformConfig(t)
	before := current.DeepCopy()
	current.Data["platformVersion"] = version
	NewWithT(t).Expect(engine(t).Client().Patch(t.Context(), current,
		crclient.MergeFromWithOptions(before, crclient.MergeFromWithOptimisticLock{}))).To(Succeed())
}

func (s *scenario) ownedPlatformConfig(t *testing.T) *corev1.ConfigMap {
	t.Helper()

	g := NewWithT(t)
	key := crclient.ObjectKey{Namespace: s.targetNS, Name: s.platformConfigName()}
	uid := s.trackedUID(configMapGVK, key)
	g.Expect(uid).NotTo(BeEmpty(), "expected the scenario ConfigMap %s to be tracked", key)
	current := s.configMap(t, key.Namespace, key.Name)
	g.Expect(current.UID).To(Equal(uid), "refusing to mutate a replacement ConfigMap %s", key)
	return current
}

func (s *scenario) deletePlatformConfig(t *testing.T) {
	t.Helper()

	g := NewWithT(t)
	kube := engine(t).Client()
	current := s.ownedPlatformConfig(t)
	key := crclient.ObjectKeyFromObject(current)
	uid := s.trackedUID(configMapGVK, key)
	g.Expect(kube.Delete(t.Context(), current, crclient.Preconditions{UID: &uid})).To(Succeed())
	g.Eventually(func() error {
		return kube.Get(t.Context(), key, &corev1.ConfigMap{})
	}).WithContext(t.Context()).WithTimeout(10 * time.Second).Should(MatchError(apierrors.IsNotFound, "is a NotFound error"))

	// The test intentionally recreates this one identity after deleting its
	// original UID. Record the new UID only from the subsequent Create.
	s.tracked = slices.DeleteFunc(s.tracked, func(object trackedObject) bool {
		return object.gvk == configMapGVK && object.namespace == key.Namespace &&
			object.name == key.Name && object.uid == uid
	})
}

func (s *scenario) createPlatformConfig(t *testing.T, version string) {
	t.Helper()

	config := &corev1.ConfigMap{
		Name:      s.platformConfigName(),
		Namespace: s.targetNS,
		Data:      map[string]string{"platformVersion": version},
	}
	NewWithT(t).Expect(engine(t).Client().Create(t.Context(), config)).To(Succeed())
	s.record(t, configMapGVK, config)
}

func (s *scenario) awaitControllerPod(t *testing.T, previous types.UID) corev1.Pod {
	t.Helper()

	g := NewWithT(t)
	kube := engine(t).Client()
	var current corev1.Pod
	g.Eventually(func(g Gomega) {
		pods := &corev1.PodList{}
		g.Expect(kube.List(t.Context(), pods, crclient.InNamespace(s.targetNS),
			crclient.MatchingLabels{"app": mockControllerName})).To(Succeed())
		g.Expect(pods.Items).To(HaveLen(1))
		current = pods.Items[0]
		g.Expect(current.DeletionTimestamp).To(BeNil())
		g.Expect(current.UID).NotTo(Equal(previous))
		replicaOwner := metav1.GetControllerOf(&current)
		g.Expect(replicaOwner).NotTo(BeNil())
		g.Expect(replicaOwner.Kind).To(Equal("ReplicaSet"))
		replica := &appsv1.ReplicaSet{}
		g.Expect(kube.Get(t.Context(), crclient.ObjectKey{Namespace: s.targetNS, Name: replicaOwner.Name}, replica)).To(Succeed())
		g.Expect(replica.UID).To(Equal(replicaOwner.UID))
		g.Expect(metav1.IsControlledBy(replica, &appsv1.Deployment{
			UID: s.trackedUID(deploymentGVK, crclient.ObjectKey{Namespace: s.targetNS, Name: mockControllerName}),
		})).To(BeTrue())
		g.Expect(current.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", corev1.PodReady),
			HaveField("Status", corev1.ConditionTrue),
		)))
	}).WithContext(t.Context()).WithTimeout(time.Minute).WithPolling(250 * time.Millisecond).Should(Succeed())
	return current
}

func (s *scenario) moduleEvents(ctx context.Context, kube crclient.Client, instanceUID types.UID) ([]eventsv1.Event, error) {
	events := &eventsv1.EventList{}
	err := kube.List(ctx, events, crclient.InNamespace(metav1.NamespaceDefault),
		crclient.MatchingFields{"regarding.uid": string(instanceUID)})
	return events.Items, err
}

// Retire the Deployment before capturing its last events so retries cannot
// create more after the scenario's exact-UID cleanup begins.
func (s *scenario) registerControllerCleanup(t *testing.T, deployment *appsv1.Deployment, instance *unstructured.Unstructured) {
	t.Helper()

	active := engine(t)
	kube := active.Client()
	uid := deployment.UID
	key := crclient.ObjectKeyFromObject(deployment)
	NewWithT(t).Expect(uid).To(Equal(s.trackedUID(deploymentGVK, key)), "refusing to retire an untracked Deployment")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		if t.Failed() {
			s.logControllerOutput(t, ctx)
			events, err := s.moduleEvents(ctx, kube, instance.GetUID())
			if err == nil {
				for _, event := range events {
					t.Logf("module event %s: %s", event.Reason, event.Note)
				}
			}
		}

		err := kube.Delete(ctx, deployment, crclient.Preconditions{UID: &uid}, crclient.PropagationPolicy(metav1.DeletePropagationForeground))
		switch {
		case err == nil:
			err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true, func(ctx context.Context) (bool, error) {
				found := &appsv1.Deployment{}
				err := kube.Get(ctx, key, found)
				switch {
				case apierrors.IsNotFound(err):
					return true, nil
				case err != nil:
					return false, err
				default:
					return found.UID != uid, nil
				}
			})
		case apierrors.IsNotFound(err):
			err = nil
		}
		if err != nil {
			t.Errorf("retiring mock Deployment %s (%s): %v", key, uid, err)
		}

		events, err := s.moduleEvents(ctx, kube, instance.GetUID())
		if err != nil {
			t.Errorf("tracking events for mock instance %s: %v", instance.GetUID(), err)
			return
		}
		for i := range events {
			// Defend the cleanup boundary independently of the field selector.
			if events[i].Regarding.UID == instance.GetUID() {
				s.record(t, eventsv1.SchemeGroupVersion.WithKind("Event"), &events[i])
			}
		}
	})
}

func (s *scenario) logControllerOutput(t *testing.T, ctx context.Context) {
	t.Helper()

	active := engine(t)
	pods := &corev1.PodList{}
	if err := active.Client().List(ctx, pods, crclient.InNamespace(s.targetNS),
		crclient.MatchingLabels{"app": mockControllerName}); err != nil {
		t.Logf("listing mock controller pods: %v", err)
		return
	}
	kube, err := kubernetes.NewForConfig(active.Config())
	if err != nil {
		t.Logf("creating the controller log client: %v", err)
		return
	}
	for _, pod := range pods.Items {
		output, err := kube.CoreV1().Pods(s.targetNS).GetLogs(pod.Name,
			&corev1.PodLogOptions{Container: "manager", LimitBytes: new(int64(16384))}).DoRaw(ctx)
		t.Logf("mock controller pod %s logs (%v): %s", pod.Name, err, output)
	}
}

func matchingFailureEvent(events []eventsv1.Event, uid types.UID) (eventsv1.Event, error) {
	for _, event := range events {
		if event.Regarding.UID == uid && event.Reason == "ReconcileError" {
			return event, nil
		}
	}
	return eventsv1.Event{}, fmt.Errorf("no ReconcileError event for %s", uid)
}
