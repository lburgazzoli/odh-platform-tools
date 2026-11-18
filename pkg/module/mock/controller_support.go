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
	"fmt"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions"
	fwconditions "github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	fwtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

const (
	platformVersionKey = "platformVersion"
	mockStatusStateKey = "mock.platform.opendatahub.io/status"
)

type mockStatusState struct {
	previous []frameworkapi.Condition
	desired  map[string]frameworkapi.Condition
}

func discoverModuleMapper(ctx context.Context, config *rest.Config, gvk schema.GroupVersionKind) (meta.RESTMapper, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, moduleerrors.New(moduleerrors.ErrMockRuntime, "could not create a discovery client", moduleerrors.WithCause(err))
	}

	resources, err := discoveryClient.ServerResourcesForGroupVersionWithContext(ctx, gvk.GroupVersion().String())
	switch {
	case apierrors.IsNotFound(err):
		return moduleMapper(gvk, nil)
	case err != nil:
		return nil, moduleerrors.New(moduleerrors.ErrMockRuntime, fmt.Sprintf("could not discover custom resource %s", gvk), moduleerrors.WithCause(err))
	}

	return moduleMapper(gvk, resources.APIResources)
}

func moduleMapper(gvk schema.GroupVersionKind, resources []metav1.APIResource) (meta.RESTMapper, error) {
	version := gvk.GroupVersion()
	for _, resource := range resources {
		if resource.Kind != gvk.Kind || strings.Contains(resource.Name, "/") {
			continue
		}
		if resource.Namespaced {
			return nil, moduleerrors.New(moduleerrors.ErrInvalidMockOptions, fmt.Sprintf("custom resource %s must be cluster-scoped", gvk))
		}

		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{version, corev1.SchemeGroupVersion})
		mapper.AddSpecific(gvk, version.WithResource(resource.Name), version.WithResource(resource.SingularName), meta.RESTScopeRoot)
		mapper.Add(corev1.SchemeGroupVersion.WithKind("ConfigMap"), meta.RESTScopeNamespace)

		return mapper, nil
	}

	return nil, moduleerrors.New(moduleerrors.ErrInvalidMockOptions, fmt.Sprintf("custom resource %s is not discoverable", gvk))
}

func mockStatusAction(key client.ObjectKey) actions.Fn {
	return func(ctx context.Context, rr *fwtypes.ReconciliationRequest) error {
		obj := rr.Instance.(*moduleObject)
		previous := obj.GetStatus().DeepCopy().Conditions
		injected, version, err := readMockInputs(ctx, rr.Client, key, obj)
		desired := desiredConditions(injected, obj.Generation)
		aggregator, aggregatorErr := fwconditions.NewAggregator(frameworkapi.ConditionTypeReady, conditionDependents(desired)...)
		if aggregatorErr != nil {
			return fmt.Errorf("configure mock readiness: %w", aggregatorErr)
		}

		rr.Conditions = fwconditions.NewManager(obj, aggregator)
		rr.Conditions.Reset()
		for _, condition := range desired {
			rr.Conditions.SetCondition(condition)
		}
		if rr.Extensions == nil {
			rr.Extensions = make(map[string]any)
		}
		rr.Extensions[mockStatusStateKey] = mockStatusState{previous: previous, desired: desired}

		if version != "" {
			obj.SetReleaseStatus(frameworkapi.ComponentReleaseStatus{
				Releases: []frameworkapi.ComponentRelease{{Name: frameworkapi.ReleasePlatform, Version: version}},
			})
		}

		return err
	}
}

func readMockInputs(ctx context.Context, reader client.Reader, key client.ObjectKey, obj *moduleObject) ([]frameworkapi.Condition, string, error) {
	conditions, err := readConditions(obj.Annotations, obj.Generation)
	if err != nil {
		return invalidInputConditions(obj.Generation, "InvalidMockAnnotation", err), "", nil
	}

	version, err := readPlatformVersion(ctx, reader, key)
	if errors.Is(err, moduleerrors.ErrInvalidMockPlatformConfiguration) {
		return invalidInputConditions(obj.Generation, "InvalidPlatformConfiguration", err), "", nil
	}

	return conditions, version, err
}

func invalidInputConditions(generation int64, reason string, cause error) []frameworkapi.Condition {
	return []frameworkapi.Condition{
		{Type: string(conditionTypeMockReady), Status: metav1.ConditionFalse, ObservedGeneration: generation, Reason: reason, Message: cause.Error()},
		{Type: string(frameworkapi.ConditionTypeDegraded), Status: metav1.ConditionTrue, ObservedGeneration: generation, Reason: reason, Message: cause.Error()},
	}
}

func finishMockStatus(_ context.Context, rr *fwtypes.ReconciliationRequest, _ bool) error {
	state, ok := rr.Extensions[mockStatusStateKey].(mockStatusState)
	if !ok {
		// A failed action may not have created a per-request status state.
		return nil
	}

	normalizeConditionMetadata(rr.Instance.GetStatus(), state.previous, state.desired, rr.Instance.GetGeneration(), time.Now())

	return nil
}

func readPlatformVersion(ctx context.Context, reader client.Reader, key client.ObjectKey) (string, error) {
	var configMap corev1.ConfigMap
	err := reader.Get(ctx, key, &configMap)
	switch {
	case apierrors.IsNotFound(err):
		return "", moduleerrors.New(moduleerrors.ErrInvalidMockPlatformConfiguration, fmt.Sprintf("create platform ConfigMap %s with data.%s", key, platformVersionKey))
	case err != nil:
		return "", fmt.Errorf("read platform ConfigMap %s: %w", key, err)
	}

	version, err := semver.ParseTolerant(configMap.Data[platformVersionKey])
	if err != nil {
		return "", moduleerrors.New(moduleerrors.ErrInvalidMockPlatformConfiguration,
			fmt.Sprintf("set data.%s in ConfigMap %s to a semantic version", platformVersionKey, key), moduleerrors.WithCause(err))
	}

	return version.String(), nil
}

func moduleRequests(reader client.Reader) handler.MapFunc {
	return func(ctx context.Context, _ client.Object) []ctrl.Request {
		var instances moduleObjectList
		if err := reader.List(ctx, &instances); err != nil {
			log.FromContext(ctx).Error(err, "list module instances after platform ConfigMap change")
			return nil
		}

		requests := make([]ctrl.Request, 0, len(instances.Items))
		for i := range instances.Items {
			requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&instances.Items[i])})
		}

		return requests
	}
}
