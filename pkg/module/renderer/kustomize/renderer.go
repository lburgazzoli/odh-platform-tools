// Package kustomize renders Kustomize-based module definitions through
// the actual framework Kustomize action over captured, contained local
// inputs.
package kustomize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kztypes "sigs.k8s.io/kustomize/api/types"

	fwkustomize "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/render/kustomize"
	mk "github.com/opendatahub-io/odh-platform-utilities/framework/render/kustomize"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// Render renders the module described by document through the actual
// framework Kustomize action and returns the rendered objects.
//
// The kustomization tree is first captured into a private, validated
// snapshot; the action renders the snapshot, never the original tree,
// with caching disabled and no client attached. The declared path maps
// directly to the manifest entry with no namespace transformation and
// no fallback to a default/ overlay: the entry kustomization is always
// rendered as captured. Generated metadata and output ordering come
// from the framework action.
//
// Engine load restrictions are relaxed to LoadRestrictionsNone so
// sibling bases, components, patches, and generators inside the module
// root load. That relaxation is safe only because rendering runs from
// the contained snapshot: the snapshot holds validated bytes at their
// module-root-relative positions and no reference to anything outside
// itself, so nothing outside the module root is reachable.
//
// The returned list contains every object the framework action
// rendered; callers filtering it must not assume they hold the raw
// kustomization output.
func Render(ctx context.Context, document config.Document) ([]unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	snapshot, request, err := renderer.CaptureRequest(ctx, document)
	if err != nil {
		return nil, fmt.Errorf("kustomize backend for module %s: %w", document.Definition.Metadata.Name, err)
	}
	defer snapshot.Close() //nolint:errcheck // read-only snapshot

	action := fwkustomize.NewAction(
		fwkustomize.WithCache(false),
		fwkustomize.WithManifestsOptions(
			mk.WithLoadRestrictions(kztypes.LoadRestrictionsNone),
			mk.WithEngineRenderOpts(
				mk.WithKustomizationFileName(kustomizationFileName(snapshot.Entry())),
			),
		),
	)

	if err := action(ctx, request); err != nil {
		return nil, fmt.Errorf("kustomize render for module %s: %w", document.Definition.Metadata.Name, err)
	}

	return request.Resources, nil
}

// kustomizationFileName returns the kustomization file name the
// snapshot entry uses, so .yml entries render without falling back to
// the engine's default overlay lookup.
func kustomizationFileName(entry string) string {
	if _, err := os.Stat(filepath.Join(entry, "kustomization.yaml")); err == nil {
		return "kustomization.yaml"
	}

	return "kustomization.yml"
}
