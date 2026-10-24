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

	moduleowner "github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/owner"
)

// Run is the default apply-only orchestration: Load, then Render,
// then Deploy, over one invocation context derived from the caller's
// context and the options' timeout. The default run is the whole
// command on its own — a successful apply is command success, with
// no convergence check, no workload monitoring, and no readiness
// claim. When Options.Wait is set, the opt-in controller rollout
// wait runs once after the successful apply, over the same
// still-live invocation context; see Wait.
//
// The caller's cancellation and the invocation settings are checked
// before the invocation timer is constructed, so an already-cancelled
// caller fails with its own cancellation identity and a nonpositive
// timeout stays an options error instead of becoming an artificial
// deadline error. The invocation context stays alive until Run
// returns: no worker is abandoned and no context is cancelled early.
//
// The returned Result reports what the invocation confirmed — the
// attempted and last-successful stages, the confirmed owner identity,
// the publication outcome, and apply completion — and the returned
// error preserves its cause chain through Unwrap for callers that
// classify failures. Neither ever echoes authored definition,
// dotenv, or API-returned values; the framework action's own logging
// is already discarded by the deploy service, and this orchestration
// adds no logging of its own.
//
// An explicitly requested controller wait is layered after Deploy
// through Options.Wait; the default run performs and reports apply
// only. Run requires a fresh Runner: it rejects a Runner whose
// stages were already driven, directly or by a previous Run.
func (r *Runner) Run(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return rejectedResult(), err
	}

	if err := r.options.validate(); err != nil {
		return rejectedResult(), err
	}

	if r.stage != stageNew {
		return rejectedResult(), fmt.Errorf("%w: Run requires a fresh runner", ErrStageSequence)
	}

	ctx, cancel := context.WithTimeout(ctx, r.options.Timeout)
	defer cancel()

	if err := r.Load(ctx); err != nil {
		return r.report(StageLoad, err)
	}

	if err := r.Render(ctx); err != nil {
		return r.report(StageRender, err)
	}

	if err := r.Deploy(ctx); err != nil {
		return r.report(StageDeploy, err)
	}

	// The opt-in wait runs once, after the successful apply, over the
	// same still-live invocation context; a wait failure is a runtime
	// error that retains everything the deploy confirmed.
	if r.options.Wait {
		if err := r.Wait(ctx); err != nil {
			return r.report(StageDeploy, err)
		}
	}

	return r.report(StageDeploy, nil)
}

// report snapshots the runner's confirmed state as the run result
// beside the stage error. The last-successful stage is the attempted
// one on success and the stage preceding it on failure; an unset
// publication normalizes to not attempted.
func (r *Runner) report(attempted Stage, err error) (Result, error) {
	completed := attempted
	if err != nil {
		completed = attempted.preceding()
	}

	publication := r.publication
	if publication == "" {
		publication = moduleowner.OutcomeNotAttempted
	}

	return Result{
		Attempted:      attempted,
		Completed:      completed,
		Owner:          r.owner,
		Publication:    publication,
		ApplyCompleted: r.applied,
		WaitRequested:  r.options.Wait,
		WaitCompleted:  r.waitCompleted,
		WaitTargets:    r.waitTargets,
	}, err
}

// rejectedResult is the report of an invocation rejected before any
// stage ran: nothing was attempted, nothing was published.
func rejectedResult() Result {
	return Result{
		Publication: moduleowner.OutcomeNotAttempted,
	}
}
