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

// Package renderer owns guarded module rendering: it selects the
// native manifest-kit backend a loaded definition asks for, drives it
// through one shared engine, and applies the payload guard to the
// final output. The backends in the helm and kustomize subpackages
// only construct their native renderers; this parent package is the
// single place engine execution, ordering, cooperative cancellation,
// and the reserved-resource policy live.
//
// Render is the one public entrypoint. Lint calls it with no options,
// so its output matches offline rendering exactly; run calls it with
// the installation namespace and its adaptation injectors as caller
// post-renderers.
package renderer

import (
	"context"
	"fmt"

	engine "github.com/k8s-manifest-kit/engine/pkg"
	"github.com/k8s-manifest-kit/engine/pkg/postrenderer"
	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/helm"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/kustomize"
)

// Render renders the module described by definition, whose file was
// loaded from definitionFile, through the backend its manifest format
// selects, and returns the rendered objects.
//
// The options resolve once per invocation: the namespace becomes the
// Helm release namespace or the Kustomize namespace transformer's
// target — passed to the native constructor before anything renders —
// and the caller post-renderers run after the engine's apply-order
// post-renderer, in the order supplied. The zero options leave
// rendering untouched, matching offline lint output exactly.
//
// Rendering is the shared engine's: the library apply-order
// post-renderer first, then the caller post-renderers, each guarded
// by a context check, then the payload guard over the final batch.
// Instances of the definition's own module type, any
// PlatformModuleDefinition, and the persisted definition CRD fail the
// render, so no caller can receive them; a render that finished after
// the context expired never becomes successful output either. Every
// failure returns a nil object set with the cause reachable through
// errors.Is/As and the format and module named in the error text.
func Render(
	ctx context.Context,
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	opts ...RenderOption,
) ([]unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	options := resolveOptions(opts...)

	native, err := backendFor(definition, definitionFile, options.Namespace)
	if err != nil {
		return nil, err
	}

	return execute(ctx, definition, native, options)
}

// resolveOptions applies the options in order over the defaults once
// per public invocation; a nil option — nil interface or typed nil
// functional option — is ignored.
func resolveOptions(opts ...RenderOption) RenderOptions {
	options := RenderOptions{}

	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&options)
		}
	}

	return options
}

// backendFor selects and constructs the native renderer for the
// loaded definition's manifest format. Structural validation is
// deferred, so an unsupported value reaches this point as a
// definition failure rather than being caught at load time.
func backendFor(
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	namespace string,
) (enginetypes.Renderer, error) {
	switch definition.Spec.Manifests.Format {
	case v1alpha1.FormatHelm:
		return helm.New(definition, definitionFile, namespace)
	case v1alpha1.FormatKustomize:
		return kustomize.New(definition, definitionFile, namespace)
	default:
		return nil, moduleerrors.NewDefinitionErrorf("unsupported manifest format %q for module %s",
			definition.Spec.Manifests.Format,
			definition.Name)
	}
}

// execute runs the shared guarded pipeline over an
// already-constructed native renderer: engine construction, render,
// the payload guard on the final batch, and the final context check.
// It is the one execution path every render — public or in-memory
// test — goes through.
func execute(
	ctx context.Context,
	definition v1alpha1.PlatformModuleDefinition,
	native enginetypes.Renderer,
	options RenderOptions,
) ([]unstructured.Unstructured, error) {
	format := definition.Spec.Manifests.Format

	shared, err := newEngine(native, options)
	if err != nil {
		return nil, fmt.Errorf("%s render for module %s: %w", format, definition.Name, err)
	}

	resources, err := shared.Render(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s render for module %s: %w", format, definition.Name, err)
	}

	// The shared payload guard fails the render itself on reserved
	// objects, after every caller post-renderer enriched the batch;
	// they are never silently dropped.
	if err := CheckReserved(definition, resources); err != nil {
		return nil, fmt.Errorf("%s render for module %s: %w", format, definition.Name, err)
	}

	// A render that finished after the context expired never becomes
	// successful output.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return resources, nil
}

// newEngine builds the shared rendering engine around a native
// manifest-kit renderer from already-resolved options. The library
// postrenderer.ApplyOrder is registered first, followed by the
// caller-supplied post-renderers, so ordering precedes injection
// exactly once for both backends. Every registered post-renderer is
// guarded by a context check, so an expired context stops the chain
// between processors instead of publishing successful output; the
// framework deploy action sorts the enriched batch again before
// writes, so no additional final engine sort is added here.
func newEngine(r enginetypes.Renderer, options RenderOptions) (*engine.Engine, error) {
	postRenderers := make([]enginetypes.PostRenderer, 0, len(options.PostRenderers)+1)
	postRenderers = append(postRenderers, cancellable(postrenderer.ApplyOrder()))

	for _, postRenderer := range options.PostRenderers {
		postRenderers = append(postRenderers, cancellable(postRenderer))
	}

	return engine.New(
		engine.WithRenderer(r),
		engine.Options{PostRenderers: postRenderers},
	)
}

// cancellable guards one post-renderer with a context check: an
// already-expired context returns its own error and the wrapped
// processor never runs. Synchronous native rendering cannot be
// interrupted this way — only the chain between processors and the
// boundaries around rendering are cooperative.
func cancellable(postRenderer enginetypes.PostRenderer) enginetypes.PostRenderer {
	return func(ctx context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		return postRenderer(ctx, objects)
	}
}
