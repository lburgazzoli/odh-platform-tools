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
	"k8s.io/apimachinery/pkg/types"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// Stage identifies a deployment phase. The empty value means none was attempted.
type Stage string

const (
	// StageLoad is the local preparation stage.
	StageLoad Stage = "load"

	// StageRender is the rendering stage.
	StageRender Stage = "render"

	// StageDeploy is the deployment stage.
	StageDeploy Stage = "deploy"
)

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

// Result reports confirmed progress, including state retained after failure.
// Apply completion alone does not imply workload readiness.
type Result struct {
	// Attempted is the last attempted stage, or empty if rejected before execution.
	Attempted Stage

	// Completed is the last successful stage, or empty if none succeeded.
	Completed Stage

	// Owner is the server-confirmed definition, retained after payload or wait
	// failure. Nil means unconfirmed; it does not prove that no write occurred.
	Owner *toolApi.PlatformModuleDefinition

	// ApplyCompleted records a successful payload apply. Failed applies may
	// still leave partial writes.
	ApplyCompleted bool

	// WaitRequested records whether rollout waiting was requested.
	WaitRequested bool

	// WaitCompleted records whether all selected Deployments rolled out.
	WaitCompleted bool

	// WaitTargets contains the observed Deployment identities.
	WaitTargets []types.NamespacedName
}
