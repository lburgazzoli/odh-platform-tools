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
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

const probeAddress = ":8081"

// Run serves the status-only controller until the caller cancels ctx.
func Run(ctx context.Context, config *rest.Config, options Options) error {
	gvk, err := options.groupVersionKind()
	switch {
	case err != nil:
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	case config == nil:
		return moduleerrors.New(moduleerrors.ErrMockRuntime, "Kubernetes REST configuration is required")
	}

	scheme := newModuleScheme(gvk)
	if err := corev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register mock ConfigMap watch: %w", err)
	}
	configMap := client.ObjectKey{
		Namespace: cmp.Or(options.Namespace, metav1.NamespaceDefault),
		Name:      options.PlatformConfigMap,
	}
	mapper, err := discoverModuleMapper(ctx, config, gvk)
	if err != nil {
		return err
	}

	ctrlManager, err := ctrl.NewManager(config, ctrl.Options{
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
					Namespaces: map[string]cache.Config{configMap.Namespace: {}},
					Field:      fields.OneTermEqualSelector("metadata.name", configMap.Name),
				},
			},
		},
		Client: client.Options{Cache: &client.CacheOptions{Unstructured: true}},
	})
	if err != nil {
		return moduleerrors.New(moduleerrors.ErrMockRuntime, "could not create the mock controller manager", moduleerrors.WithCause(err))
	}

	mgr := fwmanager.New(ctrlManager)
	if err := configureController(ctx, mgr, configMap); err != nil {
		return err
	}
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("run mock controller: %w", err)
	}

	return ctx.Err()
}

func configureController(ctx context.Context, mgr ctrl.Manager, key client.ObjectKey) error {
	// The bootstrap manager only initializes ProvisioningSucceeded.
	// Seeding other dependencies could change persisted Ready before the
	// action has captured its previous status.
	bootstrap, err := fwconditions.NewAggregator(frameworkapi.ConditionTypeProvisioningSucceeded)
	if err != nil {
		return fmt.Errorf("configure initial mock condition manager: %w", err)
	}

	if _, err := reconciler.ReconcilerFor(mgr, &moduleObject{}).
		WithReconcilerOpts(reconciler.WithConditionAggregator(bootstrap), reconciler.WithFinalizerName("")).
		WithAction(mockStatusAction(key)).
		WithPostStatusFn(finishMockStatus).
		Watches(&corev1.ConfigMap{},
			reconciler.WithPredicates(configMapEvents(key)),
			reconciler.WithEventMapper(moduleRequests(mgr.GetClient())),
		).
		Build(ctx); err != nil {
		return fmt.Errorf("configure mock reconciler: %w", err)
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("configure mock health probe: %w", err)
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("configure mock readiness probe: %w", err)
	}

	return nil
}

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
