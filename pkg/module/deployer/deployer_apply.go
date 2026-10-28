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

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// Apply runs Load, Render, Deploy, and the requested Wait under Options.Timeout.
// It requires a fresh deployer and reports confirmed state after a failure.
func (r *Deployer) Apply(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if err := r.options.Validate(); err != nil {
		return Result{}, err
	}

	if r.stage != stageNew {
		return Result{}, moduleerrors.New(
			moduleerrors.ErrStageSequence,
			"invalid deployer stage sequence: Apply requires a fresh deployer",
		)
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

	// A wait failure retains the completed deployment in the result.
	if r.options.Wait {
		if err := r.Wait(ctx); err != nil {
			return r.reportCompleted(StageDeploy, StageDeploy, err)
		}
	}

	return r.report(StageDeploy, nil)
}

func (r *Deployer) report(attempted Stage, err error) (Result, error) {
	completed := attempted
	if err != nil {
		completed = attempted.preceding()
	}

	return r.reportCompleted(attempted, completed, err)
}

func (r *Deployer) reportCompleted(attempted Stage, completed Stage, err error) (Result, error) {
	return Result{
		Attempted:      attempted,
		Completed:      completed,
		Owner:          r.owner,
		ApplyCompleted: r.applied,
		WaitRequested:  r.options.Wait,
		WaitCompleted:  r.waitCompleted,
		WaitTargets:    r.waitTargets,
	}, err
}
