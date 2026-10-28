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

// Package renderer runs Helm or Kustomize through a shared post-rendering pipeline.
package renderer

import (
	"context"
	"fmt"

	engine "github.com/k8s-manifest-kit/engine/pkg"
	"github.com/k8s-manifest-kit/engine/pkg/postrenderer"
	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/helm"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/kustomize"
)

// Render uses the backend selected by definition. Apply ordering precedes caller
// post-renderers, then CheckReserved validates the final output. Failures return
// nil resources. Cancellation is cooperative; native rendering is not interruptible.
func Render(
	ctx context.Context,
	definition toolApi.PlatformModuleDefinition,
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

func resolveOptions(opts ...RenderOption) RenderOptions {
	options := RenderOptions{}

	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&options)
		}
	}

	return options
}

func backendFor(
	definition toolApi.PlatformModuleDefinition,
	definitionFile string,
	namespace string,
) (enginetypes.Renderer, error) {
	switch definition.Spec.Manifests.Format {
	case toolApi.FormatHelm:
		return helm.New(definition, definitionFile, namespace)
	case toolApi.FormatKustomize:
		return kustomize.New(definition, definitionFile, namespace)
	default:
		detail := fmt.Errorf("unsupported manifest format %q for module %s",
			definition.Spec.Manifests.Format,
			definition.Name)
		return nil, moduleerrors.New(moduleerrors.ErrInvalidDefinition,
			"invalid module definition: "+detail.Error(), moduleerrors.WithCause(detail))
	}
}

func execute(
	ctx context.Context,
	definition toolApi.PlatformModuleDefinition,
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

	if err := CheckReserved(definition, resources); err != nil {
		return nil, fmt.Errorf("%s render for module %s: %w", format, definition.Name, err)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return resources, nil
}

// The deploy action sorts again after injection, so no final sort is needed here.
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

func cancellable(postRenderer enginetypes.PostRenderer) enginetypes.PostRenderer {
	return func(ctx context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		return postRenderer(ctx, objects)
	}
}
