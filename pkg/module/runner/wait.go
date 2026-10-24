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

package runner

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubectl/pkg/polymorphichelpers"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// deploymentGVK is the only workload kind the opt-in wait covers:
// rendered apps/v1 Deployments. Non-controller workloads are out of
// scope by standing rule.
var deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

// waitPollInterval is the pause between observation rounds. It is
// unexported test state, not a public knob: the wait budget, not the
// interval, is the user-facing control.
const defaultWaitPollInterval = time.Second

// Wait is the opt-in controller rollout wait: after a successful
// Deploy, it observes the live rendered apps/v1 controller
// Deployments until the public kubectl deployment status helper
// reports every one rolled out, or the shared wait budget expires.
// It never creates, updates, or patches anything.
//
// The waited targets are the rendered Deployments matching the
// configured controller deployment name — the same target selection
// the env injector uses, falling back to the module's metadata.name
// — deduplicated by namespace and name, in their actual rendered
// namespaces. Unrelated Deployments are ignored, and a payload with
// no matching Deployment fails here, after the apply, never during
// local definition validation.
//
// One shared budget starts when Wait runs, as a child of the given
// context — the still-live invocation context when Run drives it —
// so an explicit WaitTimeout can never extend the remaining overall
// timeout. Every observation is a fresh uncached GET; a target with
// a deletionTimestamp fails on every observation including the
// first, even when its replica counts look complete; the first live
// UID is pinned and a replacement or later disappearance fails; an
// initial NotFound may keep waiting. Live objects go straight to the
// public helper with no revision constraint, so merged replicas,
// generation lag, incomplete updates, old-replica termination,
// availability, and the progress deadline are judged exactly as
// kubectl judges them — no extra readiness, paused, or
// ReplicaFailure policy exists here.
//
// A wait failure is a runtime error: the published owner, the
// confirmed publication, and apply completion are retained, and the
// report never claims readiness. Underlying errors and the helper's
// progress text are never echoed into public output; causes stay
// reachable through Unwrap.
func (r *Runner) Wait(ctx context.Context) error {
	if r.stage != stageDeployed {
		return fmt.Errorf("%w: Wait requires a successful Deploy", ErrStageSequence)
	}

	if r.waited {
		return fmt.Errorf("%w: Wait already ran", ErrStageSequence)
	}

	r.waited = true

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	targets := r.selectWaitTargets()
	r.waitTargets = targets

	if len(targets) == 0 {
		r.fail()
		return fmt.Errorf("%w: module %s controller deployment %q", ErrWaitTargetMissing, r.definition.Name, r.controllerDeploymentName())
	}

	ctx, cancel := context.WithTimeout(ctx, r.waitBudget())
	defer cancel()

	if err := r.observeUntilRolledOut(ctx, targets); err != nil {
		r.fail()
		return err
	}

	r.waitCompleted = true

	return nil
}

// waitBudget is the shared wait budget: the explicit override when
// positive, the default otherwise.
func (r *Runner) waitBudget() time.Duration {
	if r.options.WaitTimeout > 0 {
		return r.options.WaitTimeout
	}

	return DefaultWaitTimeout
}

// controllerDeploymentName mirrors the env injector's target
// selection: the configured controller deployment name, falling back
// to the module's metadata.name.
func (r *Runner) controllerDeploymentName() string {
	if name := r.definition.Spec.Controller.Deployment.Name; name != "" {
		return name
	}

	return r.definition.Name
}

// selectWaitTargets picks the wait's targets out of the rendered
// payload: apps/v1 Deployments whose name matches the configured
// controller deployment name, deduplicated by namespace and name.
func (r *Runner) selectWaitTargets() []Target {
	name := r.controllerDeploymentName()

	var targets []Target
	seen := make(map[Target]struct{})

	for i := range r.resources {
		resource := &r.resources[i]

		if resource.GroupVersionKind() != deploymentGVK {
			continue
		}

		if resource.GetName() != name {
			continue
		}

		target := Target{Namespace: resource.GetNamespace(), Name: resource.GetName()}
		if _, duplicate := seen[target]; duplicate {
			continue
		}

		seen[target] = struct{}{}
		targets = append(targets, target)
	}

	return targets
}

// observeUntilRolledOut polls the live targets until the public
// status helper reports every one rolled out. Each round GETs every
// target fresh, applies the termination and identity rules, and asks
// the helper; the shared context bounds the whole loop.
func (r *Runner) observeUntilRolledOut(ctx context.Context, targets []Target) error {
	viewer := &polymorphichelpers.DeploymentStatusViewer{}
	pinned := make(map[Target]types.UID, len(targets))

	ticker := time.NewTicker(r.waitPollInterval)
	defer ticker.Stop()

	for {
		rolledOut := true

		for _, target := range targets {
			live := &unstructured.Unstructured{}
			live.SetGroupVersionKind(deploymentGVK)

			err := r.connection.Client().Get(ctx, crclient.ObjectKey{
				Namespace: target.Namespace,
				Name:      target.Name,
			}, live)
			if err != nil {
				if apierrors.IsNotFound(err) {
					// An initial NotFound may keep waiting; a target
					// that was live and then vanished fails.
					if _, wasLive := pinned[target]; wasLive {
						return fmt.Errorf("waiting for deployment %s/%s: the deployment disappeared: %w",
							target.Namespace, target.Name, err)
					}

					rolledOut = false
					continue
				}

				return fmt.Errorf("waiting for deployment %s/%s: %w",
					target.Namespace, target.Name, err)
			}

			// A terminating target fails on every observation,
			// including the first, whatever its replica counts say.
			if live.GetDeletionTimestamp() != nil {
				return fmt.Errorf("waiting for deployment %s/%s: the deployment is terminating",
					target.Namespace, target.Name)
			}

			// The first live UID is pinned; a replacement fails.
			if uid, wasLive := pinned[target]; wasLive {
				if live.GetUID() != uid {
					return fmt.Errorf("waiting for deployment %s/%s: the deployment was replaced",
						target.Namespace, target.Name)
				}
			} else {
				pinned[target] = live.GetUID()
			}

			// The public helper is the only rollout judge: live
			// object, no revision constraint, no extra policy.
			_, done, err := viewer.Status(live, 0)
			if err != nil {
				return fmt.Errorf("waiting for deployment %s/%s: %w",
					target.Namespace, target.Name, err)
			}

			if !done {
				rolledOut = false
			}
		}

		if rolledOut {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for module %s controller rollout: %w", r.definition.Name, ctx.Err())
		case <-ticker.C:
		}
	}
}
