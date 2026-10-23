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
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleowner "github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/owner"
)

// Stage names one Runner stage in a run report. The empty Stage means
// no stage was attempted: the invocation was rejected before any
// input or output happened.
type Stage string

const (
	// StageLoad is the local preparation stage.
	StageLoad Stage = "load"

	// StageRender is the rendering stage.
	StageRender Stage = "render"

	// StageDeploy is the deployment stage.
	StageDeploy Stage = "deploy"
)

// preceding returns the stage that runs before this one in the run
// sequence, or the empty Stage for StageLoad.
func (s Stage) preceding() Stage {
	switch s {
	case StageDeploy:
		return StageRender
	case StageRender:
		return StageLoad
	default:
		return ""
	}
}

// Result is the honest, apply-only outcome of one Run invocation. It
// reports what the invocation confirmed — never readiness, never
// source values, never fabricated counts.
//
// Owner identity and desired-spec publication are distinct facts: a
// non-nil Owner with Publication unknown means a write may have
// landed, and a nil Owner means the identity is unconfirmed, never
// that no write occurred. ApplyCompleted reports that the single
// framework payload apply returned success and nothing more: a
// pending, absent, or nonconverged Deployment causes no failure and
// no monitoring call, and apply completion is never a readiness
// claim.
type Result struct {
	// Attempted is the last stage the run attempted, in Load →
	// Render → Deploy order. It is empty when the run rejected the
	// invocation before any stage ran.
	Attempted Stage

	// Completed is the last stage that finished successfully. It is
	// empty when no stage did.
	Completed Stage

	// Owner is the confirmed server-backed identity of the
	// definition owner, when the invocation confirmed one — from a
	// successful publication or from a failure that still carried
	// identity. A nil value asserts an unconfirmed identity only.
	Owner *v1alpha1.PlatformModuleDefinition

	// Publication classifies the desired-spec publication of the
	// definition owner: not attempted, confirmed, rejected, or
	// unknown. Only observed publication success or an
	// already-matching server state confirms the desired spec.
	Publication moduleowner.Outcome

	// ApplyCompleted reports whether the framework payload apply
	// finished without error. Writes that preceded the framework's
	// first error report stand as partial state; this flag never
	// claims workload convergence.
	ApplyCompleted bool
}
