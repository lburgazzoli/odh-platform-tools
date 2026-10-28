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

// Package deployer composes the reusable module services into the
// staged module deploy: explicit Load, Render, and Deploy stages
// holding per-invocation state. The services stay independently
// callable; this package only wires them, it does not embed or
// rename them.
//
// A Deployer is fresh per invocation and not concurrency-safe. Stages
// run in order — Load, Render, Deploy — and every out-of-order,
// repeated, or post-failure call is rejected without input or output.
// A failed stage is terminal: later stages never run, and the state a
// failure still confirms — the server-backed owner identity — is
// retained rather than discarded.
//
// Load and Render are client-free local preparation. The deployer
// never connects: its Kubernetes client arrives fully built through
// the options, and the composition layer owns construction from the
// standard cli-runtime configuration. Deploy runs the definition
// owner preparation and the single framework payload apply, in that
// order, over that client. No stage prechecks namespaces — rendered
// Namespaces follow framework create/update behavior — and no stage
// lints, creates the module CR, or waits for workload convergence
// unless the opt-in Wait is requested.
package deployer

import (
	"context"
	"fmt"
	"time"

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

// Deployer holds one module deploy invocation's state across its explicit
// stages. It is constructed once per invocation; stages validate
// their sequence themselves, so an out-of-order or repeated call is
// rejected without input or output.
type Deployer struct {
	options Options
	client  crclient.Client

	stage stage

	definition   *toolApi.PlatformModuleDefinition
	images       map[string]string
	namespace    string
	platformType string
	release      api.Release

	resources []unstructured.Unstructured

	owner   *toolApi.PlatformModuleDefinition
	applied bool

	fieldManager string

	// payloadRetryBackoff is the pause between the payload action's
	// attempts; nil means defaultPayloadRetryBackoff.
	payloadRetryBackoff []time.Duration

	waited        bool
	waitCompleted bool
	waitTargets   []types.NamespacedName
}

// New resolves the constructor options in order — an Options preset
// applies wholesale, later WithXxx options override individual
// fields — and returns a fresh deployer holding the resolved
// configuration and the supplied Kubernetes client. The client is a
// required option used exactly as supplied: no copy, timeout
// adjustment, or scheme change is made, and no client call happens
// before the Deploy stage.
func New(opts ...DeployerOption) *Deployer {
	resolved := Options{}
	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&resolved)
		}
	}

	return &Deployer{
		options: resolved,
		client:  resolved.Client,
		stage:   stageNew,
	}
}

// Load is the local preparation stage: validate the invocation
// settings, parse the platform version once into the framework
// release, load and default the definition, and snapshot the declared
// image sources. It performs no client call and injects no image
// environment.
//
// The image snapshot covers exactly the declared related images plus
// the main container image's whole-token source key, when it is one;
// absent and masked sources are simply omitted, preserving the
// permissive missing-image behavior. The namespace is
// the mandatory options value, applied verbatim.
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
	r.namespace = r.options.Namespace
	r.platformType = r.options.PlatformType
	if r.platformType == "" {
		r.platformType = DefaultPlatformType
	}
	r.fieldManager = r.options.FieldManager
	if r.fieldManager == "" {
		r.fieldManager = DefaultFieldManager
	}
	r.stage = stageLoaded

	return nil
}

// Render is the rendering stage: the centralized rendering service
// renders the definition with the shared engine — the library
// apply-order post-renderer first, then the env injector, then the
// platform config injector, each exactly once — into the target
// namespace. It performs no client call and no owner or payload
// write.
//
// The rendering service has already rejected reserved identities as
// render errors and guarantees that a render finishing after the
// context expired never becomes successful output. The stage holds
// every seam to the same contract, rejecting a late successful return
// after the context expired, so no failed render result is published
// here.
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
		Namespace:           r.namespace,
		MonitoringNamespace: r.options.MonitoringNamespace,
		PlatformType:        r.platformType,
	}
	config := &adapt.PlatformConfigInjector{
		Definition:      r.definition,
		Namespace:       r.namespace,
		PlatformVersion: r.release.Version.String(),
	}

	resources, err := renderer.Render(ctx, *r.definition, r.options.DefinitionFile,
		renderer.WithNamespace(r.namespace),
		renderer.WithPostRenderer(env.Run),
		renderer.WithPostRenderer(config.Run),
	)
	if err != nil {
		r.fail()
		return err
	}

	// A render that returned after the context expired never becomes
	// successful output, even through a seam that does not check it
	// itself: the backend enforces this for the real renderers, and
	// the stage holds every seam to the same contract.
	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	r.resources = resources
	r.stage = stageRendered

	return nil
}

// Deploy is the deployment stage: prepare the definition owner —
// apply the embedded definition CRD, await its establishment, apply
// the definition itself — then apply the whole rendered payload
// through the framework deploy action with the confirmed owner and
// the same release the injector used, over the invocation's client.
//
// Only a successful preparation permits the payload apply. A
// preparation failure stops before any payload write and reports no
// owner — a nil owner does not prove that no write occurred; an apply
// failure keeps the confirmed owner, and writes that preceded the
// framework's first error report stand as partial state. No API write
// happens after the invocation expired: the context is checked after
// the preparation, ahead of the services' own entry checks. No
// namespace is prechecked: rendered Namespaces follow framework
// create/update behavior, and other missing namespaces fail through
// the API.
func (r *Deployer) Deploy(ctx context.Context) error {
	if err := r.begin(stageDeployed); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	owner, err := r.prepareOwner(ctx)
	if err != nil {
		r.fail()
		return err
	}
	r.owner = owner

	// No API write happens after the invocation expired: the payload
	// apply is checked here, ahead of the services' own entry checks.
	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	if err := r.deployPayload(ctx); err != nil {
		r.fail()
		return err
	}
	r.applied = true
	r.stage = stageDeployed

	return nil
}

// begin guards the transition into the next stage: out-of-order,
// repeated, and post-failure calls are rejected before any input or
// output happens.
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

// fail marks the invocation terminal. Confirmed state — the owner
// identity — stays readable after it.
func (r *Deployer) fail() {
	r.stage = stageFailed
}

// waitPollInterval is the effective observation pause: the explicit
// option when positive, the shared default otherwise.
func (r *Deployer) waitPollInterval() time.Duration {
	if r.options.WaitPollInterval > 0 {
		return r.options.WaitPollInterval
	}

	return DefaultWaitPollInterval
}
