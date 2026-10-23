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

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// Options carries the runtime rendering inputs that offline lint
// rendering does not have.
type Options struct {
	// Namespace is the installation namespace a run renders into.
	//
	// For the Helm backend it becomes the release namespace —
	// .Release.Namespace for chart templates — without filling
	// metadata.namespace itself: charts template the namespace
	// explicitly or leave objects namespace-less, exactly as the
	// operator passes the applications namespace to module charts.
	//
	// For the Kustomize backend it is the namespace the engine's real
	// namespace transformer applies to namespaced objects, subjects,
	// and references at render time; cluster-scoped objects keep their
	// scope.
	//
	// The zero value leaves rendering untouched, matching offline
	// lint output exactly.
	Namespace string
}

// NewRequest builds the framework reconciliation request for
// rendering the module described by definition, whose file was loaded
// from definitionFile, with no runtime options.
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
func NewRequest(definition v1alpha1.PlatformModuleDefinition, definitionFile string) *types.ReconciliationRequest {
	return NewRequestWithOptions(definition, definitionFile, Options{})
}

// NewRequestWithOptions builds the framework reconciliation request
// carrying the runtime rendering options.
func NewRequestWithOptions(definition v1alpha1.PlatformModuleDefinition, definitionFile string, options Options) *types.ReconciliationRequest {
	request := &types.ReconciliationRequest{
		Instance: NewInstance(definition),
	}

	root := filepath.Dir(definitionFile)

	// Structural validation is deferred, so a decodable definition may
	// select a backend without its block: no entry is attached and the
	// render action reports the empty result downstream instead of
	// this adapter panicking on a nil block.
	switch definition.Spec.Manifests.Format {
	case v1alpha1.FormatHelm:
		if manifests := definition.Spec.Manifests.Helm; manifests != nil {
			request.HelmCharts = []types.HelmChartInfo{{
				Source: helm.Source{
					Chart:            resolveEntry(root, manifests.Path),
					ReleaseName:      definition.Name,
					ReleaseNamespace: options.Namespace,
				},
			}}
		}
	case v1alpha1.FormatKustomize:
		if manifests := definition.Spec.Manifests.Kustomize; manifests != nil {
			// The namespace is deliberately not carried on the
			// manifest entry: like the operator's Kustomize render
			// action, the backend applies it through the engine's
			// namespace transformer at render time.
			request.Manifests = []types.ManifestInfo{
				{Path: resolveEntry(root, manifests.Path)},
			}
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
