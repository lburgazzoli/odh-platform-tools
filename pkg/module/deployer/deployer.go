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
// Load and Render are client-free local preparation. Only Deploy
// connects: the lazy cluster connection, the definition owner
// preparation, and the single framework payload apply all happen
// there, in that order. No stage prechecks namespaces — rendered
// Namespaces follow framework create/update behavior — and no stage
// lints, creates the module CR, or waits for workload convergence.
package deployer

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/blang/semver/v4"
	"github.com/operator-framework/api/pkg/lib/version"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/adapt"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/cluster"
	images "github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/images"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// DefinitionLoad loads and normalizes one definition file. The
// loader is the concrete implementation; its options flow through
// untouched.
type DefinitionLoad func(
	ctx context.Context,
	path string,
	opts ...loader.LoadOption,
) (toolApi.PlatformModuleDefinition, error)

// ManifestRender renders the manifest backend selected by a loaded
// definition with shared runtime options. The centralized rendering
// service is the concrete implementation.
type ManifestRender func(
	ctx context.Context,
	definition toolApi.PlatformModuleDefinition,
	definitionFile string,
	opts ...renderer.RenderOption,
) ([]unstructured.Unstructured, error)

// Connect builds the invocation-scoped cluster connection. The
// cluster service is the concrete implementation.
type Connect func(ctx context.Context, getter genericclioptions.RESTClientGetter) (*cluster.Connection, error)

// ApplyPayload applies the rendered payload through the framework
// deploy action under the invocation's effective field manager,
// already resolved and validated by the caller. The deploy service
// is the concrete implementation.
type ApplyPayload func(
	ctx context.Context,
	connection *cluster.Connection,
	owner *toolApi.PlatformModuleDefinition,
	release api.Release,
	resources []unstructured.Unstructured,
	fieldManager string,
) error

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
	getter  genericclioptions.RESTClientGetter
	options Options

	load    DefinitionLoad
	render  ManifestRender
	connect Connect
	apply   ApplyPayload

	stage stage

	definition            *toolApi.PlatformModuleDefinition
	images                map[string]string
	namespace             string
	applicationsNamespace string
	platformType          string
	release               api.Release

	resources  []unstructured.Unstructured
	connection *cluster.Connection

	owner   *toolApi.PlatformModuleDefinition
	applied bool

	fieldManager string

	waited           bool
	waitCompleted    bool
	waitTargets      []types.NamespacedName
	waitPollInterval time.Duration
}

// New wires the deployer with its service boundaries: the standard
// getter the invocation resolves namespace and connection from, and
// the narrow I/O seams — loader, the centralized renderer, connection,
// and payload apply. The owner preparation is the deployer's own step
// over the connection. The root module facade passes the concrete
// reusable services; tests inject fakes.
func New(
	options Options,
	getter genericclioptions.RESTClientGetter,
	load DefinitionLoad,
	render ManifestRender,
	connect Connect,
	apply ApplyPayload,
) *Deployer {
	return &Deployer{
		getter:  getter,
		options: options,
		load:    load,
		render:  render,
		connect: connect,
		apply:   apply,
		stage:   stageNew,

		waitPollInterval: defaultWaitPollInterval,
	}
}

// Load is the local preparation stage: validate the invocation
// settings, parse the platform version once into the framework
// release, load and default the definition, snapshot the declared
// image sources, and resolve the effective namespace from the
// standard getter. It performs no client or HTTP request and injects
// no image environment.
//
// The image snapshot covers exactly the declared related images plus
// the main container image's whole-token source key, when it is one;
// absent and masked sources are simply omitted, preserving the
// permissive missing-image behavior. The applications namespace
// defaults to the effective installation namespace here, so a
// rendered module lands where the invocation targets unless
// explicitly redirected.
func (r *Deployer) Load(ctx context.Context) error {
	if err := r.begin(stageLoaded); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	if err := r.options.validate(); err != nil {
		r.fail()
		return err
	}

	parsed, err := semver.ParseTolerant(r.options.PlatformVersion)
	if err != nil {
		r.fail()
		return moduleerrors.New(moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: parsing platform version: "+err.Error(),
			moduleerrors.WithCause(err))
	}

	definition, err := r.load(ctx, r.options.DefinitionFile)
	if err != nil {
		r.fail()
		return err
	}

	snapshot, err := snapshotImages(r.options.RelatedImagesFile, definition)
	if err != nil {
		r.fail()
		return err
	}

	namespace, err := cluster.ResolveNamespace(r.getter)
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
	r.namespace = namespace
	r.applicationsNamespace = r.options.ApplicationsNamespace
	if r.applicationsNamespace == "" {
		r.applicationsNamespace = namespace
	}
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
// platform config injector, each exactly once — into the applications
// namespace. It performs no client or HTTP request and no owner or
// payload write.
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
		Definition:            r.definition,
		Images:                r.images,
		ApplicationsNamespace: r.applicationsNamespace,
		MonitoringNamespace:   r.options.MonitoringNamespace,
		PlatformType:          r.platformType,
	}
	config := &adapt.PlatformConfigInjector{
		Definition:            r.definition,
		ApplicationsNamespace: r.applicationsNamespace,
		PlatformVersion:       r.release.Version.String(),
	}

	resources, err := r.render(ctx, *r.definition, r.options.DefinitionFile,
		renderer.WithNamespace(r.applicationsNamespace),
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

// Deploy is the deployment stage: connect lazily, prepare the
// definition owner — apply the embedded definition CRD, await its
// establishment, apply the definition itself — then apply the whole
// rendered payload through the framework deploy action with the
// confirmed owner and the same release the injector used.
//
// Only a successful preparation permits the payload apply. A
// preparation failure stops before any payload write and reports no
// owner — a nil owner does not prove that no write occurred; an apply
// failure keeps the confirmed owner, and writes that preceded the
// framework's first error report stand as partial state. No API write
// happens after the invocation expired: the context is checked after
// connecting and after the preparation, ahead of the services' own
// entry checks. No namespace is prechecked: rendered Namespaces
// follow framework create/update behavior, and other missing
// namespaces fail through the API.
func (r *Deployer) Deploy(ctx context.Context) error {
	if err := r.begin(stageDeployed); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	connection, err := r.connect(ctx, r.getter)
	if err != nil {
		r.fail()
		return err
	}
	r.connection = connection

	// No API write happens after the invocation expired: the owner
	// preparation and the payload apply are both checked here, ahead
	// of the services' own entry checks.
	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	owner, err := prepareOwner(ctx, connection, *r.definition, r.fieldManager)
	if err != nil {
		r.fail()
		return err
	}
	r.owner = owner

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	if err := r.apply(ctx, connection, r.owner, r.release, r.resources, r.fieldManager); err != nil {
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
		return moduleerrors.New(moduleerrors.ErrStageSequence,
			"invalid deployer stage sequence: a failed invocation is terminal")
	}

	if next != r.stage+1 {
		return moduleerrors.New(moduleerrors.ErrStageSequence,
			fmt.Sprintf("invalid deployer stage sequence: stage %d cannot follow %d",
				next, r.stage))
	}

	return nil
}

// fail marks the invocation terminal. Confirmed state — the owner
// identity — stays readable after it.
func (r *Deployer) fail() {
	r.stage = stageFailed
}

// snapshotImages snapshots the image sources of one definition: the
// declared related images plus the main container image's whole-token
// source key, when it is one, resolved from the selected file and the
// process environment.
func snapshotImages(path string, definition toolApi.PlatformModuleDefinition) (map[string]string, error) {
	keys := make([]string, 0, len(definition.Spec.RelatedImages)+1)
	keys = append(keys, definition.Spec.RelatedImages...)

	if container := definition.Spec.Controller.Deployment.Container; container != nil {
		if key, ok := images.ImageSourceKey(container.Image); ok {
			keys = append(keys, key)
		}
	}

	return images.Load(path, keys)
}
