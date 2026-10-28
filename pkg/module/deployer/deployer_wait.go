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

package deployer

import (
	"context"
	"errors"
	"fmt"
	"time"

	gvk "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubectl/pkg/polymorphichelpers"
)

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
// context — the still-live invocation context when Apply drives it —
// so an explicit WaitTimeout can never extend the remaining overall
// timeout. Every observation is a fresh uncached GET, and the wait
// follows its targets by namespace and name only: a target that is
// absent, terminating, or replaced by a new object stays pending
// until a live object under that identity rolls out. Live objects go
// straight to the public helper with no revision constraint, so
// merged replicas, generation lag, incomplete updates,
// old-replica termination, availability, and the progress deadline
// are judged exactly as kubectl judges them — no extra readiness,
// paused, or ReplicaFailure policy exists here.
//
// A wait failure is a runtime error: the published owner and apply
// completion are retained, and the
// report never claims readiness. Wait failures are safe shared
// moduleerrors.ErrRolloutWait errors:
// their text names the operation and the target identity — namespace
// and name, already safe report identity — and never the API's or the
// status helper's own text; underlying causes stay reachable only
// through Unwrap for errors.Is and errors.As.
func (r *Deployer) Wait(ctx context.Context) error {
	if r.stage != stageDeployed {
		return moduleerrors.New(
			moduleerrors.ErrStageSequence,
			"invalid deployer stage sequence: Wait requires a successful Deploy",
		)
	}

	if r.waited {
		return moduleerrors.New(
			moduleerrors.ErrStageSequence,
			"invalid deployer stage sequence: Wait already ran",
		)
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
		return moduleerrors.New(
			moduleerrors.ErrWaitTargetMissing,
			fmt.Sprintf("no rendered controller deployment matched the wait target: module %s controller deployment %q",
				r.definition.Name,
				r.controllerDeploymentName()),
		)
	}

	if err := r.observeUntilRolledOut(ctx); err != nil {
		r.fail()
		return err
	}

	r.waitCompleted = true

	return nil
}

// waitBudget is the shared wait budget: the explicit override when
// positive, the default otherwise.
func (r *Deployer) waitBudget() time.Duration {
	if r.options.WaitTimeout > 0 {
		return r.options.WaitTimeout
	}

	return DefaultWaitTimeout
}

// controllerDeploymentName mirrors the env injector's target
// selection: the configured controller deployment name, falling back
// to the module's metadata.name.
func (r *Deployer) controllerDeploymentName() string {
	if name := r.definition.Spec.Controller.Deployment.Name; name != "" {
		return name
	}

	return r.definition.Name
}

// selectWaitTargets picks the wait's targets out of the rendered
// payload: apps/v1 Deployments whose name matches the configured
// controller deployment name, deduplicated by namespace and name.
func (r *Deployer) selectWaitTargets() []types.NamespacedName {
	name := r.controllerDeploymentName()

	var targets []types.NamespacedName
	seen := sets.New[types.NamespacedName]()

	for i := range r.resources {
		resource := &r.resources[i]

		if resource.GroupVersionKind() != gvk.Deployment {
			continue
		}

		if resource.GetName() != name {
			continue
		}

		target := types.NamespacedName{Namespace: resource.GetNamespace(), Name: resource.GetName()}
		if seen.Has(target) {
			continue
		}

		seen.Insert(target)
		targets = append(targets, target)
	}

	return targets
}

// observeUntilRolledOut polls the live targets through the shared
// Kubernetes wait helper until the public status helper reports every
// one rolled out. The first observation is immediate and later rounds
// follow the poll interval; the shared wait budget, derived from the
// invocation context, bounds the whole poll.
func (r *Deployer) observeUntilRolledOut(ctx context.Context) error {
	err := wait.PollUntilContextTimeout(
		ctx,
		r.waitPollInterval(),
		r.waitBudget(),
		true,
		r.observeRolledOut,
	)
	if err == nil {
		return nil
	}

	// The poll's own termination — the budget expiring or the caller
	// cancelling — becomes the shared wait-failure message with the
	// context identity as its cause. A condition failure is already
	// one of the safe target failures and is returned as-is.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return moduleerrors.New(
			moduleerrors.ErrRolloutWait,
			fmt.Sprintf("waiting for module %s controller rollout failed: the wait was cancelled or its budget expired",
				r.definition.Name),
			moduleerrors.WithCause(err),
		)
	}

	return err
}

// observeRolledOut is one wait observation round over the recorded
// targets: every target is GET fresh, targets are followed by
// namespace and name only — an absent, terminating, or replaced
// target is pending — and live objects go to the public status
// helper. It reports whether every target is rolled out, and every
// failure is a safe shared ErrRolloutWait error whose cause carries
// the raw API or helper error.
func (r *Deployer) observeRolledOut(ctx context.Context) (bool, error) {
	viewer := &polymorphichelpers.DeploymentStatusViewer{}

	rolledOut := true

	for _, target := range r.waitTargets {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(gvk.Deployment)

		err := r.client.Get(ctx, target, live)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// An absent target is pending whenever it
				// happens: the wait follows the namespace
				// and name, never one object instance.
				rolledOut = false
				continue
			}

			return false, moduleerrors.New(
				moduleerrors.ErrRolloutWait,
				fmt.Sprintf("waiting for deployment %s/%s failed: the observation failed",
					target.Namespace, target.Name),
				moduleerrors.WithCause(err),
			)
		}

		// A terminating target is pending — never done,
		// never a failure on its own.
		if live.GetDeletionTimestamp() != nil {
			rolledOut = false
			continue
		}

		// The public helper is the only rollout judge: live
		// object, no revision constraint, no extra policy.
		_, done, err := viewer.Status(live, 0)
		if err != nil {
			return false, moduleerrors.New(
				moduleerrors.ErrRolloutWait,
				fmt.Sprintf("waiting for deployment %s/%s failed: the rollout status check failed",
					target.Namespace, target.Name),
				moduleerrors.WithCause(err),
			)
		}

		if !done {
			rolledOut = false
		}
	}

	return rolledOut, nil
}
