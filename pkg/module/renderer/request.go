// Package renderer adapts module definitions to the framework
// rendering interfaces of odh-platform-utilities.
//
// The request adapter builds the minimal framework reconciliation
// request that the framework render actions consume. It is non-owning
// and offline: it attaches no client, controller, conditions manager,
// or cluster identity, and it never mutates the definition it was
// built from.
package renderer

import (
	"path/filepath"

	helm "github.com/k8s-manifest-kit/renderer-helm/pkg"

	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// NewRequest builds the framework reconciliation request for
// rendering the module described by definition, whose file was loaded
// from definitionFile.
//
// The request carries only authored module identity and the manifest
// entry, resolved against the definition file's location: one Helm
// chart source for the Helm backend, or one manifest entry for the
// Kustomize backend. No client, controller, conditions manager, or
// release is attached, and the instance carries no cluster identity.
//
// Actions consuming the request must be built with caching disabled:
// the framework cache key requires the instance UID and generation,
// which offline rendering does not have. With caching disabled the
// cacher still reads the instance kind for metrics, so the request
// instance always carries the module's custom resource type.
func NewRequest(definition config.PlatformModuleDefinition, definitionFile string) *types.ReconciliationRequest {
	request := &types.ReconciliationRequest{
		Instance: NewInstance(definition),
	}

	root := filepath.Dir(definitionFile)

	switch definition.Spec.Manifests.Format {
	case config.FormatHelm:
		request.HelmCharts = []types.HelmChartInfo{{
			Source: helm.Source{
				Chart:       resolveEntry(root, definition.Spec.Manifests.Helm.Path),
				ReleaseName: definition.Metadata.Name,
			},
		}}
	case config.FormatKustomize:
		request.Manifests = []types.ManifestInfo{
			{Path: resolveEntry(root, definition.Spec.Manifests.Kustomize.Path)},
		}
	}

	return request
}

// resolveEntry resolves an authored manifest path against the
// definition file's directory. The path is used exactly as configured,
// and an absolute path is honored as is. Existence and shape of the
// entry are the render engines' to validate, so a missing or
// unsuitable entry fails at render time.
func resolveEntry(root string, authored string) string {
	if filepath.IsAbs(authored) {
		return filepath.Clean(authored)
	}

	return filepath.Join(root, authored)
}
