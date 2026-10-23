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

// Package runner composes the reusable module services into the
// staged module run: explicit Load, Render, and Deploy stages holding
// per-invocation state. The services stay independently callable;
// this package only wires them, it does not embed or rename them.
//
// A Runner is fresh per invocation and not concurrency-safe. Stages
// run in order — Load, Render, Deploy — and every out-of-order,
// repeated, or post-failure call is rejected without input or output.
// A failed stage is terminal: later stages never run, and the state a
// failure still confirms — the server-backed owner identity and its
// publication outcome — is retained rather than discarded.
//
// Load and Render are client-free local preparation. Only Deploy
// connects: the lazy cluster connection, the definition owner
// publication, and the single framework payload apply all happen
// there, in that order. No stage prechecks namespaces — rendered
// Namespaces follow framework create/update behavior — and no stage
// lints, creates the module CR, or waits for workload convergence.
package runner

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/blang/semver/v4"
	"github.com/operator-framework/api/pkg/lib/version"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/adapt"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
	images "github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/images"
	moduleowner "github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/owner"
)

// DefinitionLoad loads and normalizes one definition file. The strict
// loader is the concrete implementation.
type DefinitionLoad func(ctx context.Context, path string) (v1alpha1.PlatformModuleDefinition, error)

// ManifestRender renders the manifest backend selected by a loaded
// definition with shared runtime options. The Helm and Kustomize
// backend wrappers are the concrete implementations.
type ManifestRender func(
	ctx context.Context,
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	options renderer.Options,
) ([]unstructured.Unstructured, error)

// Connect builds the invocation-scoped cluster connection. The
// cluster service is the concrete implementation.
type Connect func(ctx context.Context, getter genericclioptions.RESTClientGetter) (*cluster.Connection, error)

// EnsureOwner publishes the definition owner. The owner service is
// the concrete implementation.
type EnsureOwner func(
	ctx context.Context,
	connection *cluster.Connection,
	definition v1alpha1.PlatformModuleDefinition,
) (*v1alpha1.PlatformModuleDefinition, error)

// ApplyPayload applies the rendered payload through the framework
// deploy action. The deploy service is the concrete implementation.
type ApplyPayload func(
	ctx context.Context,
	connection *cluster.Connection,
	owner *v1alpha1.PlatformModuleDefinition,
	release api.Release,
	resources []unstructured.Unstructured,
) error

// stage is the runner's position in the Load → Render → Deploy
// sequence.
type stage int

const (
	stageNew stage = iota
	stageLoaded
	stageRendered
	stageDeployed
	stageFailed
)

// Runner holds one module run invocation's state across its explicit
// stages. It is constructed once per invocation; stages validate
// their sequence themselves, so an out-of-order or repeated call is
// rejected without input or output.
type Runner struct {
	getter  genericclioptions.RESTClientGetter
	options Options

	load            DefinitionLoad
	renderHelm      ManifestRender
	renderKustomize ManifestRender
	connect         Connect
	ensure          EnsureOwner
	apply           ApplyPayload

	stage stage

	definition            *v1alpha1.PlatformModuleDefinition
	images                map[string]string
	namespace             string
	applicationsNamespace string
	platformType          string
	release               api.Release

	resources  []unstructured.Unstructured
	connection *cluster.Connection

	owner       *v1alpha1.PlatformModuleDefinition
	publication moduleowner.Outcome
	applied     bool
}

// New wires the runner with its service boundaries: the standard
// getter the invocation resolves namespace and connection from, and
// the narrow I/O seams — loader, both backend renderers, connection,
// owner publication, and payload apply. The root module facade passes
// the concrete reusable services; tests inject fakes.
func New(
	options Options,
	getter genericclioptions.RESTClientGetter,
	load DefinitionLoad,
	renderHelm ManifestRender,
	renderKustomize ManifestRender,
	connect Connect,
	ensure EnsureOwner,
	apply ApplyPayload,
) *Runner {
	return &Runner{
		getter:          getter,
		options:         options,
		load:            load,
		renderHelm:      renderHelm,
		renderKustomize: renderKustomize,
		connect:         connect,
		ensure:          ensure,
		apply:           apply,
		stage:           stageNew,
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
func (r *Runner) Load(ctx context.Context) error {
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
		return fmt.Errorf("%w: parsing platform version: %w", ErrInvalidOptions, err)
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
	r.stage = stageLoaded

	return nil
}

// Render is the rendering stage: the selected backend renders the
// definition with the shared engine — the library apply-order
// post-renderer first, then the env injector, then the platform
// config injector, each exactly once — into the applications
// namespace. It performs no client or HTTP request and no owner or
// payload write.
//
// The renderer itself has already rejected reserved identities as
// render errors, and the backend guarantees that a render finishing
// after the context expired never becomes successful output, so no
// failed render result is published here.
func (r *Runner) Render(ctx context.Context) error {
	if err := r.begin(stageRendered); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		r.fail()
		return err
	}

	render, err := r.backendFor(r.definition)
	if err != nil {
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

	resources, err := render(ctx, *r.definition, r.options.DefinitionFile, renderer.Options{
		Namespace:     r.applicationsNamespace,
		PostRenderers: []enginetypes.PostRenderer{env.Run, config.Run},
	})
	if err != nil {
		r.fail()
		return err
	}

	r.resources = resources
	r.stage = stageRendered

	return nil
}

// Deploy is the deployment stage: connect lazily, publish the
// definition owner, then apply the whole rendered payload through the
// framework deploy action with the confirmed owner and the same
// release the injector used.
//
// Only a successful owner.Ensure permits the payload apply. An Ensure
// failure retains whatever identity and publication outcome it still
// confirmed — a nil owner does not prove that no write occurred — and
// stops before any payload write; an apply failure keeps the
// published owner, and writes that preceded the framework's first
// error report stand as partial state. No namespace is prechecked:
// rendered Namespaces follow framework create/update behavior, and
// other missing namespaces fail through the API.
func (r *Runner) Deploy(ctx context.Context) error {
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

	owner, err := r.ensure(ctx, connection, *r.definition)
	if err != nil {
		r.retainOwner(owner, err)
		r.fail()
		return err
	}
	r.owner = owner
	r.publication = moduleowner.OutcomeConfirmed

	if err := r.apply(ctx, connection, r.owner, r.release, r.resources); err != nil {
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
func (r *Runner) begin(next stage) error {
	if r.stage == stageFailed {
		return fmt.Errorf("%w: a failed invocation is terminal", ErrStageSequence)
	}

	if next != r.stage+1 {
		return fmt.Errorf("%w: stage %d cannot follow %d", ErrStageSequence, next, r.stage)
	}

	return nil
}

// fail marks the invocation terminal. Confirmed state — the owner
// identity and publication outcome — stays readable after it.
func (r *Runner) fail() {
	r.stage = stageFailed
}

// retainOwner records the owner state a failed Ensure still
// confirms: the best-known identity — the return value or the typed
// error's — and the publication outcome. A nil owner means the
// identity is unconfirmed, never that no write occurred.
func (r *Runner) retainOwner(owner *v1alpha1.PlatformModuleDefinition, err error) {
	var typed *moduleowner.Error
	if errors.As(err, &typed) {
		if owner == nil {
			owner = typed.Owner
		}
		r.publication = typed.Outcome
	}

	r.owner = owner
}

// backendFor selects the rendering service for the loaded
// definition's manifest format. Structural validation is deferred, so
// an unsupported value reaches this point as a definition failure
// rather than being caught at load time.
func (r *Runner) backendFor(definition *v1alpha1.PlatformModuleDefinition) (ManifestRender, error) {
	switch definition.Spec.Manifests.Format {
	case v1alpha1.FormatHelm:
		return r.renderHelm, nil
	case v1alpha1.FormatKustomize:
		return r.renderKustomize, nil
	default:
		return nil, moduleerrors.NewDefinitionErrorf("unsupported manifest format %q for module %s",
			definition.Spec.Manifests.Format,
			definition.Name)
	}
}

// snapshotImages snapshots the image sources of one definition: the
// declared related images plus the main container image's whole-token
// source key, when it is one, resolved from the selected file and the
// process environment.
func snapshotImages(path string, definition v1alpha1.PlatformModuleDefinition) (map[string]string, error) {
	keys := make([]string, 0, len(definition.Spec.RelatedImages)+1)
	keys = append(keys, definition.Spec.RelatedImages...)

	if container := definition.Spec.Controller.Deployment.Container; container != nil {
		if key, ok := images.ImageSourceKey(container.Image); ok {
			keys = append(keys, key)
		}
	}

	return images.Load(path, keys)
}
