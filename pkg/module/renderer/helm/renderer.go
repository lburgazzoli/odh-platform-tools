// Package helm builds the native manifest-kit Helm renderer for
// Helm-based module definitions. Construction is its whole job: the
// shared rendering service in the parent renderer package drives the
// returned renderer through the shared engine, ordering, and guard.
package helm

import (
	"context"
	"fmt"
	"path/filepath"

	jsonv2 "encoding/json/v2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"
	helm "github.com/k8s-manifest-kit/renderer-helm/pkg"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/source"
)

// New builds the native Helm renderer for the module described by
// definition, whose file was loaded from definitionFile: the chart
// entry resolves against the definition file's directory, the release
// name is the module's metadata.name, and namespace — the release
// namespace, exposed to chart templates as .Release.Namespace without
// filling metadata.namespace — is passed already resolved. Authored
// literal values decode lazily inside the source's own values
// function, so a malformed entry surfaces as a render failure, not a
// construction failure.
//
// The explicit flags preserve the old framework action's effective
// struct-valued options: strict templates, and no renderer-added
// content hash or source annotations, where the native renderer would
// default the content hash to true. The renderer carries no cache and
// no client, never resolves replacement strings, never sets values
// beyond the definition's authored literals, and never adds repository
// configuration, so descriptor images and environment entries cannot
// affect the output.
func New(
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	namespace string,
) (enginetypes.Renderer, error) {
	native, err := helm.New(
		[]helm.Source{helmSource(definition, definitionFile, namespace)},
		helm.WithStrict(true),
		helm.WithContentHash(false),
		helm.WithSourceAnnotations(false),
	)
	if err != nil {
		return nil, fmt.Errorf("helm render for module %s: %w", definition.Name, err)
	}

	return native, nil
}

// helmSource builds the native Helm chart source for a definition:
// the chart entry resolves against the definition file's directory,
// the release name is the module's metadata.name, and the release
// namespace carries the resolved namespace without filling
// metadata.namespace. Authored literal values decode lazily inside
// the source's own values function — the renderer's error channel —
// so a malformed entry surfaces as a render failure, not a new error
// seam here.
func helmSource(
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	namespace string,
) helm.Source {
	var path string
	var values map[string]apiextensionsv1.JSON

	if manifests := definition.Spec.Manifests.Helm; manifests != nil {
		path = manifests.Path
		values = manifests.Values
	}

	source := helm.Source{
		Chart:            source.ResolveEntry(filepath.Dir(definitionFile), path),
		ReleaseName:      definition.Name,
		ReleaseNamespace: namespace,
	}

	if values != nil {
		source.Values = func(context.Context) (enginetypes.Values, error) {
			return decodeHelmValues(values)
		}
	}

	return source
}

// decodeHelmValues decodes authored literal chart values to the map
// the Helm renderer consumes. Each entry is raw JSON exactly as
// authored — placeholder-looking text stays literal — and the round
// trip cannot fail for content that loaded successfully, but a
// malformed entry is reported rather than dropped.
//
// A null entry is represented by apiextensions JSON with empty Raw —
// its UnmarshalJSON leaves Raw untouched for null while MarshalJSON
// emits null again — so empty Raw decodes as JSON null, not as an EOF
// failure.
func decodeHelmValues(values map[string]apiextensionsv1.JSON) (enginetypes.Values, error) {
	result := make(enginetypes.Values, len(values))

	for key, value := range values {
		var decoded any

		if len(value.Raw) == 0 {
			decoded = nil
		} else if err := jsonv2.Unmarshal(value.Raw, &decoded); err != nil {
			return nil, fmt.Errorf("helm value %q: %w", key, err)
		}

		result[key] = decoded
	}

	return result, nil
}
