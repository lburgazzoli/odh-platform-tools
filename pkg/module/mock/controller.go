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

// Package mock runs the platform module status simulator.
package mock

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"

	"github.com/go-logr/logr"
	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	fwconditions "github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/reconciler"
	fwmanager "github.com/opendatahub-io/odh-platform-utilities/framework/manager"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	moduleapi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

const probeAddress = ":8081"

// newModuleScheme builds a fresh Scheme per invocation: the selected GVK
// maps onto the shared ModuleObject types so the framework can read and
// write status through its platform interfaces, the List kind backs
// cache listing, and the corev1 types back the ConfigMap watch.
func newModuleScheme(gvk schema.GroupVersionKind) (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(gvk, &moduleapi.ModuleObject{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &moduleapi.ModuleObjectList{})
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register ConfigMap types in the mock scheme: %w", err)
	}

	return scheme, nil
}

// Controller runs the framework's status-only reconciler for one module kind.
type Controller struct {
	config    *rest.Config
	gvk       schema.GroupVersionKind
	configMap client.ObjectKey
}

// New creates a controller for the selected resource and ConfigMap.
func New(config *rest.Config, options Options) *Controller {
	return &Controller{
		config: config,
		gvk:    options.GVK,
		configMap: client.ObjectKey{
			Namespace: cmp.Or(options.Namespace, metav1.NamespaceDefault),
			Name:      options.PlatformConfigMap,
		},
	}
}

// Run serves the status-only controller in the foreground until the
// caller cancels ctx. Cancellation is the expected shutdown path: the
// canceled context is still returned so the command reports it and
// exits 1, the contract for a supervised foreground controller.
func (c *Controller) Run(ctx context.Context) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case c.config == nil:
		return moduleerrors.New(moduleerrors.ErrMockRuntime, "Kubernetes REST configuration is required")
	case c.gvk.Group == "":
		return moduleerrors.New(moduleerrors.ErrInvalidMockOptions, "mock controller requires a custom resource group")
	}

	// Discovery runs before the manager is built so option and
	// existence problems surface before the probes bind.
	mapper, err := discoverModuleMapper(ctx, c.config, c.gvk)
	if err != nil {
		return err
	}
	scheme, err := newModuleScheme(c.gvk)
	if err != nil {
		return err
	}

	ctrlManager, err := ctrl.NewManager(c.config, c.managerOptions(mapper, scheme))
	if err != nil {
		return moduleerrors.New(moduleerrors.ErrMockRuntime, "could not create the mock controller manager", moduleerrors.WithCause(err))
	}

	mgr := fwmanager.New(ctrlManager)
	if err := c.registerStatusReconciler(ctx, mgr); err != nil {
		return err
	}
	if err := addProbes(mgr); err != nil {
		return err
	}
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("run mock controller: %w", err)
	}

	return ctx.Err()
}

// managerOptions scopes the manager to this controller: the
// pre-discovered REST mapper, health probes without metrics, and a
// ConfigMap cache restricted to the exact platform ConfigMap. Missing
// informers fail reads instead of silently serving an empty cache, and
// the cache serves unstructured objects because the framework registers
// its secondary watches that way.
func (c *Controller) managerOptions(mapper meta.RESTMapper, scheme *runtime.Scheme) ctrl.Options {
	return ctrl.Options{
		Scheme: scheme,
		MapperProvider: func(_ *rest.Config, _ *http.Client) (meta.RESTMapper, error) {
			return mapper, nil
		},
		Logger:                 logr.FromSlogHandler(slog.Default().Handler()),
		HealthProbeBindAddress: probeAddress,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		Cache: cache.Options{
			ReaderFailOnMissingInformer: true,
			ByObject: map[client.Object]cache.ByObject{
				&corev1.ConfigMap{}: {
					Namespaces: map[string]cache.Config{c.configMap.Namespace: {}},
					Field:      fields.OneTermEqualSelector("metadata.name", c.configMap.Name),
				},
			},
		},
		Client: client.Options{Cache: &client.CacheOptions{Unstructured: true}},
	}
}

// registerStatusReconciler wires the framework reconciler for the
// selected kind: the status action and post-status hook, the finalizer
// disabled because the mock reports status only, and the ConfigMap
// watch that re-reconciles every instance when the platform version
// changes.
func (c *Controller) registerStatusReconciler(ctx context.Context, mgr ctrl.Manager) error {
	// The bootstrap aggregator only initializes ProvisioningSucceeded.
	// Seeding other dependencies could change persisted Ready before the
	// action has captured its previous status.
	bootstrap, err := fwconditions.NewAggregator(frameworkapi.ConditionTypeProvisioningSucceeded)
	if err != nil {
		return fmt.Errorf("configure initial mock condition manager: %w", err)
	}

	if _, err := reconciler.ReconcilerFor(mgr, &moduleapi.ModuleObject{}).
		WithReconcilerOpts(reconciler.WithConditionAggregator(bootstrap), reconciler.WithFinalizerName("")).
		WithAction(c.reconcileStatus).
		WithPostStatusFn(finishMockStatus).
		Watches(&corev1.ConfigMap{},
			reconciler.WithPredicates(configMapEvents(c.configMap)),
			reconciler.WithEventMapper(moduleRequests(mgr.GetClient())),
		).
		Build(ctx); err != nil {
		return fmt.Errorf("configure mock reconciler: %w", err)
	}

	return nil
}

func addProbes(mgr ctrl.Manager) error {
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("configure mock health probe: %w", err)
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("configure mock readiness probe: %w", err)
	}

	return nil
}

// configMapEvents enqueues the exact platform ConfigMap on create,
// delete, and data changes. Metadata-only updates and other ConfigMaps
// are ignored so the watch cannot loop on the controller's own
// activity.
func configMapEvents(key client.ObjectKey) predicate.Predicate {
	return predicate.And(
		predicate.NewPredicateFuncs(func(object client.Object) bool {
			return client.ObjectKeyFromObject(object) == key
		}),
		predicate.Funcs{UpdateFunc: func(update event.UpdateEvent) bool {
			return !reflect.DeepEqual(configMapEventData(update.ObjectOld), configMapEventData(update.ObjectNew))
		}},
	)
}

// The framework registers secondary watches as unstructured resources.
func configMapEventData(object client.Object) any {
	if config, ok := object.(*unstructured.Unstructured); ok {
		return config.Object["data"]
	}

	return nil
}

// moduleRequests maps a platform ConfigMap change to reconcile requests
// for every instance of the selected kind.
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
