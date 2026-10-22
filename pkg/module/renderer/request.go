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
	helm "github.com/k8s-manifest-kit/renderer-helm/pkg"

	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// NewRequest builds the framework reconciliation request for rendering
// the module described by document.
//
// The request carries only authored module identity and the
// loader-resolved local renderer paths from the document: one Helm
// chart source for the Helm backend, or one manifest entry for the
// Kustomize backend. No client, controller, conditions manager, or
// release is attached, and the instance carries no cluster identity.
//
// Actions consuming the request must be built with caching disabled:
// the framework cache key requires the instance UID and generation,
// which offline rendering does not have. With caching disabled the
// cacher still reads the instance kind for metrics, so the request
// instance always carries the module's custom resource type.
func NewRequest(document config.Document) *types.ReconciliationRequest {
	request := &types.ReconciliationRequest{
		Instance: NewInstance(document.Definition),
	}

	switch document.Definition.Spec.Manifests.Format {
	case config.FormatHelm:
		request.HelmCharts = []types.HelmChartInfo{{
			Source: helm.Source{
				Chart:       document.RendererEntry,
				ReleaseName: document.Definition.Metadata.Name,
			},
		}}
	case config.FormatKustomize:
		request.Manifests = []types.ManifestInfo{
			{Path: document.RendererEntry},
		}
	}

	return request
}
