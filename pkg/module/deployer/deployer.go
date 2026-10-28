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

// Package deployer coordinates Load, Render, Deploy, and optional rollout waiting.
package deployer

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/blang/semver/v4"
	"github.com/operator-framework/api/pkg/lib/version"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/adapt"
	images "github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/images"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/retry"
)

// stage is the deployer's position in the Load → Render → Deploy
// sequence.
type stage int

const (
	stageNew stage = iota
	stageLoaded
	stageRendered
	stageDeployed
	stageFailed
)

// Deployer holds one invocation's state and is not safe for concurrent use.
// Stages run in order; failures are terminal.
type Deployer struct {
	options Options
	client  crclient.Client

	stage stage

	definition *toolApi.PlatformModuleDefinition
	images     map[string]string
	release    api.Release

	resources []unstructured.Unstructured

	owner   *toolApi.PlatformModuleDefinition
	applied bool

	payloadRetryOptions retry.Options

	waited        bool
	waitCompleted bool
	waitTargets   []types.NamespacedName
}

// New applies options in order and resolves defaults. The supplied client is
// used unchanged; cluster access begins in Deploy.
func New(opts ...DeployerOption) *Deployer {
	resolved := Options{}
	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&resolved)
		}
	}

	// Resolve defaults once; leave invalid values for validation.
	if resolved.PlatformType == "" {
		resolved.PlatformType = DefaultPlatformType
	}
	if resolved.FieldManager == "" {
		resolved.FieldManager = DefaultFieldManager
	}
	if resolved.WaitTimeout == 0 {
		resolved.WaitTimeout = DefaultWaitTimeout
	}
	if resolved.WaitPollInterval == 0 {
		resolved.WaitPollInterval = DefaultWaitPollInterval
	}

	return &Deployer{
		options:             resolved,
		client:              resolved.Client,
		payloadRetryOptions: defaultPayloadRetryOptions(),
		stage:               stageNew,
	}
}

// Load validates settings, reads the definition, and resolves image sources
// without accessing the cluster.
func (r *Deployer) Load(ctx context.Context) error {
	if err := r.begin(stageLoaded); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	if err := r.options.Validate(); err != nil {
		r.fail()
		return err
	}

	parsed, err := semver.ParseTolerant(r.options.PlatformVersion)
	if err != nil {
		r.fail()
		return moduleerrors.New(
			moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: parsing platform version: "+err.Error(),
			moduleerrors.WithCause(err),
		)
	}

	definition, err := loader.LoadFromPath(ctx, r.options.DefinitionFile)
	if err != nil {
		r.fail()
		return err
	}

	snapshot, err := images.Snapshot(r.options.RelatedImagesFile, definition)
	if err != nil {
		r.fail()
		return err
	}

	r.release = api.Release{
		Name:    api.ReleasePlatform,
		Version: version.OperatorVersion{Version: parsed},
	}
	r.definition = &definition
	r.images = snapshot
	r.stage = stageLoaded

	return nil
}

// Render builds and adapts the payload locally. Reserved resources, missing
// controller targets, and unresolved required images fail before deployment.
func (r *Deployer) Render(ctx context.Context) error {
	if err := r.begin(stageRendered); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	env := &adapt.EnvInjector{
		Definition:          r.definition,
		Images:              r.images,
		Namespace:           r.options.Namespace,
		MonitoringNamespace: r.options.MonitoringNamespace,
		PlatformType:        r.options.PlatformType,
	}
	config := &adapt.PlatformConfigInjector{
		Definition:      r.definition,
		Namespace:       r.options.Namespace,
		PlatformVersion: r.release.Version.String(),
	}

	resources, err := renderer.Render(ctx, *r.definition, r.options.DefinitionFile,
		renderer.WithNamespace(r.options.Namespace),
		renderer.WithPostRenderer(env.Run),
		renderer.WithPostRenderer(config.Run),
	)
	if err != nil {
		r.fail()
		return err
	}

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	r.resources = resources
	r.stage = stageRendered

	return nil
}

// Deploy publishes the definition owner, then applies the rendered resources.
// Failures can leave partial writes; the confirmed owner is retained.
func (r *Deployer) Deploy(ctx context.Context) error {
	if err := r.begin(stageDeployed); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	owner, err := r.publishDefinition(ctx)
	if err != nil {
		r.fail()
		return err
	}
	r.owner = owner

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	if err := r.applyResources(ctx); err != nil {
		r.fail()
		return err
	}
	r.applied = true
	r.stage = stageDeployed

	return nil
}

func (r *Deployer) begin(next stage) error {
	if r.stage == stageFailed {
		return moduleerrors.New(
			moduleerrors.ErrStageSequence,
			"invalid deployer stage sequence: a failed invocation is terminal",
		)
	}

	if next != r.stage+1 {
		return moduleerrors.New(
			moduleerrors.ErrStageSequence,
			fmt.Sprintf("invalid deployer stage sequence: stage %d cannot follow %d",
				next, r.stage),
		)
	}

	return nil
}

func (r *Deployer) fail() {
	r.stage = stageFailed
}
