// Package helm renders Helm-based module definitions through the
// direct manifest-kit Helm renderer driven by the shared engine.
package helm

import (
	"context"
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"
	helm "github.com/k8s-manifest-kit/renderer-helm/pkg"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// Render renders the module described by definition, whose file was
// loaded from definitionFile, through the direct Helm renderer with
// no runtime options, and returns the rendered objects.
//
// The renderer renders the configured chart directory directly with
// no cache and no client attached. Chart strictness, values schema
// validation, bundled CRDs, dependency handling, and output ordering
// all come from the native renderer and the shared engine: this
// wrapper only maps the chart path, the module name as the Helm
// release name, and the effective options the old framework action
// produced — strict templates, and no renderer-added content hash or
// source annotations. It never resolves replacement strings, never
// sets values beyond the definition's authored literals, and never
// adds repository configuration, so descriptor images and environment
// entries cannot affect the output.
//
// The returned list contains every object the engine rendered for the
// chart; callers filtering it must not assume they hold the raw chart
// documents. The shared payload guard runs on the output: instances
// of the definition's own module type, any PlatformModuleDefinition,
// and the persisted definition CRD fail the render, so no caller can
// receive them.
func Render(ctx context.Context, definition v1alpha1.PlatformModuleDefinition, definitionFile string) ([]unstructured.Unstructured, error) {
	return RenderWithOptions(ctx, definition, definitionFile, renderer.Options{})
}

// RenderWithOptions renders the module with runtime options: the
// options' namespace becomes the Helm release namespace, exposing
// .Release.Namespace to chart templates, and the options'
// post-renderers run after the shared engine's apply-order
// post-renderer. The renderer never fills metadata.namespace itself —
// a template that does not use .Release.Namespace renders
// namespace-less output, exactly as with no options.
func RenderWithOptions(
	ctx context.Context,
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	options renderer.Options,
) ([]unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	source := helmSource(definition, definitionFile, options)

	// The explicit flags preserve the old framework action's
	// effective struct-valued options: strict templates, and no
	// renderer-added content hash or source annotations, where the
	// native renderer would default the content hash to true.
	native, err := helm.New(
		[]helm.Source{source},
		helm.WithStrict(true),
		helm.WithContentHash(false),
		helm.WithSourceAnnotations(false),
	)
	if err != nil {
		return nil, fmt.Errorf("helm render for module %s: %w", definition.Name, err)
	}

	shared, err := renderer.NewEngine(native, options)
	if err != nil {
		return nil, fmt.Errorf("helm render for module %s: %w", definition.Name, err)
	}

	resources, err := shared.Render(ctx)
	if err != nil {
		return nil, fmt.Errorf("helm render for module %s: %w", definition.Name, err)
	}

	// The shared payload guard fails the render itself on reserved
	// objects; they are never silently dropped.
	if err := renderer.CheckReserved(definition, resources); err != nil {
		return nil, fmt.Errorf("helm render for module %s: %w", definition.Name, err)
	}

	// A render that finished after the context expired never becomes
	// successful output.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return resources, nil
}

// helmSource builds the native Helm chart source for a definition:
// the chart entry resolves against the definition file's directory,
// the release name is the module's metadata.name, and the release
// namespace carries the options' namespace without filling
// metadata.namespace. Authored literal values decode lazily inside
// the source's own values function — the renderer's error channel —
// so a malformed entry surfaces as a render failure, not a new error
// seam here.
func helmSource(
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	options renderer.Options,
) helm.Source {
	var path string
	var values map[string]apiextensionsv1.JSON

	if manifests := definition.Spec.Manifests.Helm; manifests != nil {
		path = manifests.Path
		values = manifests.Values
	}

	source := helm.Source{
		Chart:            renderer.ResolveEntry(filepath.Dir(definitionFile), path),
		ReleaseName:      definition.Name,
		ReleaseNamespace: options.Namespace,
	}

	if values != nil {
		source.Values = func(context.Context) (enginetypes.Values, error) {
			return renderer.DecodeHelmValues(values)
		}
	}

	return source
}
