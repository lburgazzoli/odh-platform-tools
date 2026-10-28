// Package helm constructs native Helm renderers.
package helm

import (
	"context"
	"fmt"
	"path/filepath"

	jsonv2 "encoding/json/v2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"
	helm "github.com/k8s-manifest-kit/renderer-helm/pkg"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/source"
)

// New resolves the chart relative to definitionFile and uses the module name
// as the release name. Namespace sets .Release.Namespace, not object metadata.
// Only authored literal chart values are used.
func New(
	definition toolApi.PlatformModuleDefinition,
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

func helmSource(
	definition toolApi.PlatformModuleDefinition,
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

// A JSON null is stored as empty Raw by apiextensions.JSON.
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
