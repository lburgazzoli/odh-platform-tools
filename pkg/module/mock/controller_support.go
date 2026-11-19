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
	"fmt"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
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

	moduleapi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

const platformVersionKey = "platformVersion"

// persistedConditionsKey carries the conditions observed before the
// reconcile from the action to the post-status hook through
// rr.Extensions. It names per-request in-memory state, not an annotation
// persisted on the resource.
const persistedConditionsKey = "mock-persisted-conditions"

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

// reconcileStatus is the reconciler action. It resolves the annotation and
// platform version inputs, then installs the current condition set for the
// framework to aggregate. Input failures are returned as errors for the
// framework to report through ProvisioningSucceeded; they still install
// the healthy defaults so the framework can clean up stale simulated
// conditions and the post-status hook can stamp metadata.
func (c *Controller) reconcileStatus(ctx context.Context, rr *fwtypes.ReconciliationRequest) error {
	obj := rr.Instance.(*moduleapi.ModuleObject)
	persisted := obj.GetStatus().DeepCopy().Conditions

	// The annotation is resolved first: an invalid annotation must not
	// read or publish a platform version for this pass.
	injected, err := readAnnotationConditions(obj.Annotations)
	var version string
	if err == nil {
		version, err = readPlatformVersion(ctx, rr.Client, c.configMap)
	}
	if err != nil {
		// An input failure still installs the healthy defaults; a valid
		// annotation must not be partly applied when the platform
		// version cannot be resolved.
		injected = nil
	}

	current := currentConditions(injected, obj.Generation)
	aggregator, aggregatorErr := fwconditions.NewAggregator(frameworkapi.ConditionTypeReady, readyDependents(current)...)
	if aggregatorErr != nil {
		return fmt.Errorf("configure mock readiness: %w", aggregatorErr)
	}

	rr.Conditions = fwconditions.NewManager(obj, aggregator)
	rr.Conditions.Reset()
	for _, condition := range current {
		rr.Conditions.SetCondition(condition)
	}
	if rr.Extensions == nil {
		rr.Extensions = make(map[string]any)
	}
	rr.Extensions[persistedConditionsKey] = persisted

	if err == nil {
		obj.SetReleaseStatus(frameworkapi.ComponentReleaseStatus{
			Releases: []frameworkapi.ComponentRelease{{Name: frameworkapi.ReleasePlatform, Version: version}},
		})
	}

	return err
}

// finishMockStatus stamps the metadata the controller owns after the
// framework aggregated Ready and cleaned up stale conditions. It runs for
// successful and failed actions alike.
func finishMockStatus(_ context.Context, rr *fwtypes.ReconciliationRequest, _ bool) error {
	persisted, ok := rr.Extensions[persistedConditionsKey].([]frameworkapi.Condition)
	if !ok {
		// A failed action may not have created the per-request state.
		return nil
	}

	stampAutomaticMetadata(rr.Instance.GetStatus(), persisted, rr.Instance.GetGeneration(), time.Now())

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
		var instances moduleapi.ModuleObjectList
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
