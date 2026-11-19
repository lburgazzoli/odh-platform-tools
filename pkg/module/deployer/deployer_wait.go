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

	gvk "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubectl/pkg/polymorphichelpers"
)

// Wait observes matching controller Deployments after a successful Deploy.
// Targets are deduplicated by namespace/name and judged by kubectl rollout
// semantics under one shared budget. Missing, replaced, or terminating
// objects remain pending. A failed observation is a problem, not a
// verdict: the wait keeps polling under the budget, and a wait that
// fails reports the most recent problem alongside the budget outcome.
// A failed wait retains the completed apply.
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
				r.definition.ControllerDeploymentName()),
		)
	}

	if err := r.observeUntilRolledOut(ctx); err != nil {
		r.fail()
		return err
	}

	r.waitCompleted = true

	return nil
}

func (r *Deployer) selectWaitTargets() []types.NamespacedName {
	var targets []types.NamespacedName
	seen := sets.New[types.NamespacedName]()

	for i := range r.resources {
		resource := &r.resources[i]

		if !r.definition.IsControllerDeployment(resource.GroupVersionKind(), resource.GetName()) {
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

func (r *Deployer) observeUntilRolledOut(ctx context.Context) error {
	// lastProblem carries the most recent observation problem into the
	// failure report. Each observation resets it: a problem a later
	// observation superseded is noise, not the verdict.
	var lastProblem *waitObservationProblem

	err := wait.PollUntilContextTimeout(
		ctx,
		r.options.WaitPollInterval,
		r.options.WaitTimeout,
		true,
		func(ctx context.Context) (bool, error) {
			rolledOut, problem := r.observeRollout(ctx)
			lastProblem = problem

			// One failed observation does not decide the wait: the
			// poll continues under the shared budget, and the problem
			// is reported only if the wait ends while observations
			// keep failing.
			if problem != nil {
				return false, nil
			}

			return rolledOut, nil
		},
	)
	if err == nil {
		return nil
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		message := fmt.Sprintf(
			"waiting for module %s controller rollout failed: the wait was cancelled or its budget expired",
			r.definition.Name)
		cause := err

		if lastProblem != nil {
			message += "; " + safeFailureMessage(lastProblem.operation, lastProblem)
			cause = errors.Join(err, lastProblem)
		}

		return moduleerrors.New(
			moduleerrors.ErrRolloutWait,
			message,
			moduleerrors.WithCause(cause),
		)
	}

	return err
}

// observeRollout reports whether every wait target is rolled out. A
// failed observation is returned as a problem for the caller to
// remember and report, never as a verdict: one failed GET or rollout
// status check does not decide the wait.
func (r *Deployer) observeRollout(ctx context.Context) (bool, *waitObservationProblem) {
	viewer := &polymorphichelpers.DeploymentStatusViewer{}

	rolledOut := true

	for _, target := range r.waitTargets {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(gvk.Deployment)

		err := r.client.Get(ctx, target, live)
		if err != nil {
			if apierrors.IsNotFound(err) {
				rolledOut = false
				continue
			}

			return false, &waitObservationProblem{
				operation: fmt.Sprintf("observing deployment %s/%s failed",
					target.Namespace, target.Name),
				cause: err,
			}
		}

		if live.GetDeletionTimestamp() != nil {
			rolledOut = false
			continue
		}

		_, done, err := viewer.Status(live, 0)
		if err != nil {
			return false, &waitObservationProblem{
				operation: fmt.Sprintf("checking the rollout status of deployment %s/%s failed",
					target.Namespace, target.Name),
				cause: err,
			}
		}

		if !done {
			rolledOut = false
		}
	}

	return rolledOut, nil
}

// waitObservationProblem reports one failed observation during the
// rollout wait: the operation that failed, carrying the target
// identity, and the raw cause. It never fails the wait by itself; the
// wait keeps polling under its budget and reports the most recent
// problem when it fails. Error echoes the raw cause for the diagnostic
// chain; the public summary goes through safeFailureMessage.
type waitObservationProblem struct {
	operation string
	cause     error
}

func (p *waitObservationProblem) Error() string {
	return p.operation + ": " + p.cause.Error()
}

func (p *waitObservationProblem) Unwrap() error {
	return p.cause
}
