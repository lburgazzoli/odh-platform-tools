// Package kustomize renders Kustomize-based module definitions through
// the actual framework Kustomize action over the configured local
// entry.
package kustomize

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	fwkustomize "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/render/kustomize"
	mk "github.com/opendatahub-io/odh-platform-utilities/framework/render/kustomize"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// Render renders the module described by document through the actual
// framework Kustomize action and returns the rendered objects.
//
// The action renders the configured entry directory directly, with
// caching disabled and no client attached. The declared path maps
// directly to the manifest entry with no namespace transformation and
// no fallback to a default/ overlay: the entry kustomization is always
// the one rendered, and the engine's own loader selects the
// kustomization file name. Generated metadata and output ordering come
// from the framework action.
//
// Engine behavior is left at its defaults, so rendering behaves like a
// plain kustomize build over the same tree, including the engine's
// load-restriction policy: any input kustomize build rejects fails
// here with the engine's own error.
//
// The returned list contains every object the framework action
// rendered; callers filtering it must not assume they hold the raw
// kustomization output.
func Render(ctx context.Context, document config.Document) ([]unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	request := renderer.NewRequest(document)

	action := fwkustomize.NewAction(
		fwkustomize.WithCache(false),
		fwkustomize.WithManifestsOptions(
			// The framework redirects an entry without
			// kustomization.yaml to a default/ subdirectory; an
			// empty overlay path neutralizes that redirect so the
			// engine's loader resolves kustomization.yaml, .yml,
			// and .json entries itself.
			mk.WithEngineRenderOpts(
				mk.WithKustomizationOverlayPath(""),
			),
		),
	)

	if err := action(ctx, request); err != nil {
		return nil, fmt.Errorf("kustomize render for module %s: %w", document.Definition.Metadata.Name, err)
	}

	return request.Resources, nil
}
