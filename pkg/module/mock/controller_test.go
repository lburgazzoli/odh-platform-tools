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

package mock

import (
	"context"
	"errors"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	fwtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	moduleapi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

const invalidConditionControls = `[{"type":"Ready","status":"True"}]`

func TestStatusActionAppliesCurrentConditionsAndPublishesThePlatformVersion(t *testing.T) {
	g := NewWithT(t)
	ctx := t.Context()
	key := client.ObjectKey{Namespace: "mock-system", Name: "platform-config"}
	config := &corev1.ConfigMap{
		Name:      key.Name,
		Namespace: key.Namespace,
		Data:      map[string]string{platformVersionKey: "v3.12"},
	}
	obj := &moduleapi.ModuleObject{
		Generation:  4,
		Annotations: map[string]string{conditionsAnnotation: yamlSimulatedConditions},
	}
	reader := fake.NewClientBuilder().WithObjects(config).Build()
	request := &fwtypes.ReconciliationRequest{Client: reader, Instance: obj}

	g.Expect(runMockStatusAction(ctx, key, request)).To(Succeed())

	g.Expect(obj.GetReleaseStatus().GetPlatformRelease()).To(Equal("3.12.0"))
	g.Expect(obj.GetStatus().ObservedGeneration).To(Equal(int64(4)))
	g.Expect(request.Conditions.GetTopLevelCondition()).To(gstruct.PointTo(HaveField("Status", metav1.ConditionFalse)))
	g.Expect(request.Conditions.GetCondition(conditionTypeMockReady)).To(gstruct.PointTo(SatisfyAll(
		HaveField("ObservedGeneration", int64(4)),
		HaveField("Reason", "Maintenance"),
	)))
	g.Expect(request.Conditions.GetCondition(frameworkapi.ConditionTypeProvisioningSucceeded)).To(
		gstruct.PointTo(HaveField("Status", metav1.ConditionTrue)),
	)

	delete(obj.Annotations, conditionsAnnotation)
	config.Data[platformVersionKey] = "3.13.0"
	g.Expect(reader.Update(ctx, config)).To(Succeed())

	g.Expect(runMockStatusAction(ctx, key, request)).To(Succeed())
	g.Expect(request.Conditions.GetTopLevelCondition()).To(gstruct.PointTo(HaveField("Status", metav1.ConditionTrue)))
	g.Expect(request.Conditions.GetCondition("example.org/Custom")).To(BeNil())
	g.Expect(obj.GetReleaseStatus().GetPlatformRelease()).To(Equal("3.13.0"))
}

func TestStatusActionReturnsInputErrorsWithHealthyDefaults(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		annotation string
		config     map[string]string
		category   error
	}{
		{name: "missing ConfigMap", category: moduleerrors.ErrInvalidMockPlatformConfiguration},
		{name: "missing version", config: map[string]string{}, category: moduleerrors.ErrInvalidMockPlatformConfiguration},
		{name: "invalid version", config: map[string]string{platformVersionKey: "newest"}, category: moduleerrors.ErrInvalidMockPlatformConfiguration},
		{name: "valid annotation is not applied when the ConfigMap is missing", annotation: yamlSimulatedConditions, category: moduleerrors.ErrInvalidMockPlatformConfiguration},
		{name: "valid annotation is not applied when the version is invalid", annotation: yamlSimulatedConditions, config: map[string]string{platformVersionKey: "newest"}, category: moduleerrors.ErrInvalidMockPlatformConfiguration},
		{name: "invalid annotation takes precedence over missing config", annotation: invalidConditionControls, category: moduleerrors.ErrInvalidMockAnnotation},
		{name: "invalid annotation blocks a new version", annotation: invalidConditionControls, config: map[string]string{platformVersionKey: "4.0.0"}, category: moduleerrors.ErrInvalidMockAnnotation},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			g := NewWithT(t)
			key := client.ObjectKey{Namespace: "mock-system", Name: "platform-config"}
			builder := fake.NewClientBuilder()
			if scenario.config != nil {
				builder.WithObjects(&corev1.ConfigMap{
					Name:      key.Name,
					Namespace: key.Namespace,
					Data:      scenario.config,
				})
			}
			obj := &moduleapi.ModuleObject{Generation: 5}
			obj.GetReleaseStatus().SetPlatformRelease("3.0.0")
			obj.SetConditions([]frameworkapi.Condition{{Type: "Custom", Status: metav1.ConditionTrue, ObservedGeneration: 4}})
			if scenario.annotation != "" {
				obj.Annotations = map[string]string{conditionsAnnotation: scenario.annotation}
			}
			request := &fwtypes.ReconciliationRequest{Client: builder.Build(), Instance: obj}

			err := runMockStatusAction(t.Context(), key, request)

			g.Expect(err).To(MatchError(scenario.category))
			g.Expect(obj.GetReleaseStatus().GetPlatformRelease()).To(Equal("3.0.0"))
			g.Expect(obj.GetStatus().ObservedGeneration).To(Equal(int64(5)))
			g.Expect(request.Conditions.GetCondition(conditionTypeMockReady)).To(gstruct.PointTo(SatisfyAll(
				HaveField("Status", metav1.ConditionTrue),
				HaveField("ObservedGeneration", int64(5)),
				HaveField("Reason", "MockReadyTrue"),
			)))
			g.Expect(request.Conditions.GetCondition(frameworkapi.ConditionTypeDegraded)).To(gstruct.PointTo(SatisfyAll(
				HaveField("Status", metav1.ConditionFalse),
				HaveField("ObservedGeneration", int64(5)),
			)))
			g.Expect(request.Conditions.GetCondition("Custom")).To(BeNil())
			g.Expect(request.Conditions.GetCondition("example.org/Custom")).To(BeNil())
			g.Expect(request.Conditions.GetCondition(frameworkapi.ConditionTypeProvisioningSucceeded)).To(
				gstruct.PointTo(HaveField("Status", metav1.ConditionFalse)),
			)
			g.Expect(request.Conditions.GetTopLevelCondition()).To(gstruct.PointTo(HaveField("Status", metav1.ConditionFalse)))
		})
	}
}

func TestStatusActionFailsFirstReconcileAndRecoversAfterCorrection(t *testing.T) {
	g := NewWithT(t)
	ctx := t.Context()
	key := client.ObjectKey{Namespace: "mock-system", Name: "platform-config"}
	reader := fake.NewClientBuilder().Build()
	obj := &moduleapi.ModuleObject{Generation: 2}
	request := &fwtypes.ReconciliationRequest{Client: reader, Instance: obj}

	g.Expect(runMockStatusAction(ctx, key, request)).To(MatchError(moduleerrors.ErrInvalidMockPlatformConfiguration))
	g.Expect(obj.GetReleaseStatus().GetPlatformRelease()).To(BeEmpty())
	g.Expect(obj.GetStatus().ObservedGeneration).To(Equal(int64(2)))
	g.Expect(request.Conditions.GetCondition(conditionTypeMockReady)).To(gstruct.PointTo(
		HaveField("ObservedGeneration", int64(2)),
	))
	g.Expect(request.Conditions.GetCondition(frameworkapi.ConditionTypeProvisioningSucceeded)).To(gstruct.PointTo(SatisfyAll(
		HaveField("Status", metav1.ConditionFalse),
		HaveField("ObservedGeneration", int64(2)),
	)))

	config := &corev1.ConfigMap{
		Name:      key.Name,
		Namespace: key.Namespace,
		Data:      map[string]string{platformVersionKey: "3.1.0"},
	}
	g.Expect(reader.Create(ctx, config)).To(Succeed())

	g.Expect(runMockStatusAction(ctx, key, request)).To(Succeed())
	g.Expect(obj.GetReleaseStatus().GetPlatformRelease()).To(Equal("3.1.0"))
	g.Expect(request.Conditions.GetTopLevelCondition()).To(gstruct.PointTo(HaveField("Status", metav1.ConditionTrue)))
	g.Expect(request.Conditions.GetCondition(frameworkapi.ConditionTypeProvisioningSucceeded)).To(
		gstruct.PointTo(HaveField("Status", metav1.ConditionTrue)),
	)
}

func TestStatusActionReturnsRealAPIErrors(t *testing.T) {
	g := NewWithT(t)
	key := client.ObjectKey{Namespace: "mock-system", Name: "platform-config"}
	reader := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, key.Name, errors.New("access denied"))
		},
	}).Build()
	obj := &moduleapi.ModuleObject{
		Generation:  2,
		Annotations: map[string]string{conditionsAnnotation: yamlSimulatedConditions},
	}
	request := &fwtypes.ReconciliationRequest{Client: reader, Instance: obj}

	err := runMockStatusAction(t.Context(), key, request)

	g.Expect(apierrors.IsForbidden(err)).To(BeTrue())
	g.Expect(obj.GetReleaseStatus().GetPlatformRelease()).To(BeEmpty())
	g.Expect(obj.GetStatus().ObservedGeneration).To(Equal(int64(2)))
	g.Expect(request.Conditions.GetCondition(conditionTypeMockReady)).To(gstruct.PointTo(
		HaveField("Status", metav1.ConditionTrue),
	))
	g.Expect(request.Conditions.GetCondition("example.org/Custom")).To(BeNil())
	g.Expect(request.Conditions.GetCondition(frameworkapi.ConditionTypeProvisioningSucceeded)).To(
		gstruct.PointTo(HaveField("Status", metav1.ConditionFalse)),
	)
}

func TestConfigMapEventsEnqueueTheDiscoveredInstanceForDataChanges(t *testing.T) {
	g := NewWithT(t)
	key := client.ObjectKey{Name: "platform-config", Namespace: "mock-system"}
	old := new(unstructured.Unstructured)
	old.SetName(key.Name)
	old.SetNamespace(key.Namespace)
	old.Object["data"] = map[string]any{platformVersionKey: "1.0.0"}
	changed := old.DeepCopy()
	changed.Object["data"] = map[string]any{platformVersionKey: "2.0.0"}
	unrelated := changed.DeepCopy()
	unrelated.SetNamespace("other-system")
	filter := configMapEvents(key)

	g.Expect(filter.Create(event.CreateEvent{Object: old})).To(BeTrue())
	g.Expect(filter.Delete(event.DeleteEvent{Object: old})).To(BeTrue())
	g.Expect(filter.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: old.DeepCopy()})).To(BeFalse())
	g.Expect(filter.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: changed})).To(BeTrue())
	g.Expect(filter.Create(event.CreateEvent{Object: unrelated})).To(BeFalse())

	gvk := schema.GroupVersionKind{Group: "testing.example.org", Version: "v1", Kind: "SelectedModule"}
	scheme := newModuleScheme(gvk)
	obj := &moduleapi.ModuleObject{Name: "discovered-singleton"}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()

	g.Expect(moduleRequests(reader)(t.Context(), changed)).To(ConsistOf(
		ctrl.Request{NamespacedName: client.ObjectKey{Name: "discovered-singleton"}},
	))
}

func TestModuleMapperUsesDiscoveredResourceNameAndScope(t *testing.T) {
	g := NewWithT(t)
	gvk := schema.GroupVersionKind{Group: "testing.example.org", Version: "v1alpha1", Kind: "SelectedModule"}
	resources := []metav1.APIResource{
		{Name: "installedmodules/status", Kind: gvk.Kind},
		{Name: "installedmodules", SingularName: "installedmodule", Kind: gvk.Kind},
	}

	mapper, err := moduleMapper(gvk, resources)

	g.Expect(err).NotTo(HaveOccurred())
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(mapping.Resource).To(Equal(gvk.GroupVersion().WithResource("installedmodules")))
	g.Expect(mapping.Scope.Name()).To(Equal(meta.RESTScopeNameRoot))

	resources[1].Namespaced = true
	_, err = moduleMapper(gvk, resources)
	g.Expect(err).To(MatchError(moduleerrors.ErrInvalidMockOptions))
	g.Expect(err.Error()).To(ContainSubstring("must be cluster-scoped"))

	_, err = moduleMapper(gvk, resources[:1])
	g.Expect(err).To(MatchError(moduleerrors.ErrInvalidMockOptions))
	g.Expect(err.Error()).To(ContainSubstring("is not discoverable"))
}

func TestControllerRejectsCoreGVKBeforeDiscovery(t *testing.T) {
	g := NewWithT(t)
	controller := New(&rest.Config{}, Options{
		GVK:               corev1.SchemeGroupVersion.WithKind("Namespace"),
		PlatformConfigMap: "platform-config",
	})

	g.Expect(controller.Run(t.Context())).To(MatchError(moduleerrors.ErrInvalidMockOptions))
}

// runMockStatusAction runs the action and mimics the pinned framework
// apply sequence around it: provisioning result, stale cleanup, happiness,
// then the post-status hook. It does not prove real persistence, retries,
// or events.
func runMockStatusAction(ctx context.Context, key client.ObjectKey, rr *fwtypes.ReconciliationRequest) error {
	controller := &Controller{configMap: key}
	err := controller.reconcileStatus(ctx, rr)
	if err != nil {
		rr.Conditions.MarkFalse(frameworkapi.ConditionTypeProvisioningSucceeded)
	} else {
		rr.Conditions.MarkTrue(frameworkapi.ConditionTypeProvisioningSucceeded)
	}
	rr.Conditions.CleanupStaleConditions()
	rr.Conditions.RecomputeHappiness()
	return errors.Join(err, finishMockStatus(ctx, rr, rr.Conditions.IsHappy()))
}
