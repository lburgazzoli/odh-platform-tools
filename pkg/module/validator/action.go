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
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// Service repeatedly observes a module's live state and evaluates a fresh
// complete cycle until every check passes together, the caller's deadline
// expires, or an execution failure occurs. All operations are reads; the
// caller's context bounds discovery, every read, and the poll loop.
type Service struct {
	discovery DiscoveryFunc
	reader    ObjectReader
	options   ServiceOptions
}

// NewService returns a validator service over the injected context-aware
// discovery and explicit-GVR reader seams.
func NewService(discovery DiscoveryFunc, reader ObjectReader, opts ...ServiceOption) *Service {
	options := ServiceOptions{}.preset()

	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&options)
		}
	}

	return &Service{
		discovery: discovery,
		reader:    reader,
		options:   options,
	}
}

// Validate observes the module described by the loaded, defaulted,
// selector-validated definition in namespace until every check passes in
// one cycle. It returns the latest cycle's result and, on execution
// failure, an error classified with ErrValidateRuntime; context
// cancellation and deadline errors keep their identity. It never returns
// success after the caller's deadline.
func (s *Service) Validate(
	ctx context.Context,
	definition v1alpha1.PlatformModuleDefinition,
	namespace string,
) (Result, error) {
	selectors := SelectorsFromDefinition(definition)

	if err := ValidateSelectors(selectors, namespace); err != nil {
		return Result{Namespace: namespace}, err
	}

	if err := ctx.Err(); err != nil {
		return Result{Namespace: namespace}, err
	}

	for {
		result, err := s.observe(ctx, selectors, namespace)
		if err != nil {
			return result, err
		}

		if result.Passed() {
			// Success is only reported while the budget is still live;
			// the deadline outcome takes precedence.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}

			return result, nil
		}

		timer := time.NewTimer(s.options.PollInterval)

		select {
		case <-ctx.Done():
			timer.Stop()

			return result, ctx.Err()
		case <-timer.C:
		}
	}
}

// observe performs one complete observation cycle: discovery, the module
// CR read over the discovered mapping, and the independent Deployment and
// ConfigMap reads, then evaluates the fresh snapshot. Execution failures
// still evaluate and return the partial cycle; independent checks are
// collected while the context permits.
func (s *Service) observe(
	ctx context.Context,
	selectors Selectors,
	namespace string,
) (Result, error) {
	snapshot := Snapshot{
		Selectors: selectors,
		Namespace: namespace,
	}

	failures := &cycleError{}

	// Discovery runs every cycle so a previously missing API is
	// rediscovered.
	resources, err := s.discovery(ctx, selectors.Module.GroupVersion().String())
	failures.record(err, fmt.Sprintf("discover API %s", selectors.Module.GroupVersion()))

	switch {
	case err != nil && apierrors.IsNotFound(err):
		snapshot.Discovery = APIDiscovery{NotFound: true}
	case err != nil:
		snapshot.Discovery = APIDiscovery{Err: err}
	default:
		snapshot.Discovery = APIDiscovery{Resources: resources.APIResources}
	}

	// The module CR is read only through a usable cluster-scoped mapping;
	// a namespaced mapping is never used as a substitute. The plural comes
	// from discovery, never English pluralization.
	if resource, found := ModuleMapping(selectors.Module, snapshot.Discovery.Resources); found && !resource.Namespaced {
		gvr := selectors.Module.GroupVersion().WithResource(resource.Name)

		object, readErr := s.reader(ctx, gvr, "", selectors.ModuleName)
		snapshot.Module = failures.observation(object, readErr, fmt.Sprintf(
			"read module %s %q", selectors.Module.Kind, selectors.ModuleName))
	}

	// Independent checks are collected while the context permits; reads
	// against a dead context fail fast anyway.
	if ctx.Err() == nil {
		object, readErr := s.reader(ctx, deploymentGVR, namespace, selectors.DeploymentName)
		snapshot.Deployment = failures.observation(object, readErr, fmt.Sprintf(
			"read Deployment %q in namespace %q", selectors.DeploymentName, namespace))

		object, readErr = s.reader(ctx, configMapGVR, namespace, selectors.ConfigMapName)
		snapshot.ConfigMap = failures.observation(object, readErr, fmt.Sprintf(
			"read ConfigMap %q in namespace %q", selectors.ConfigMapName, namespace))
	}

	result := Evaluate(snapshot)

	return result, failures.err()
}
