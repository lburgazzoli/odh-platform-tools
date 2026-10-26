// Package renderer holds the manifest source and engine plumbing the
// lint and run backend renderers share: entry-path resolution against
// the definition file, lazy decoding of authored Helm values, and the
// common engine construction that registers the library apply-order
// post-renderer ahead of any caller-supplied post-renderer.
//
// The backends in the helm and kustomize subpackages build their
// native manifest-kit sources from a loaded definition and drive them
// through engine.New/Engine.Render; this parent package never imports
// those backends back, so no cycle forms.
package renderer

import (
	"context"
	"fmt"
	"path/filepath"

	jsonv2 "encoding/json/v2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	engine "github.com/k8s-manifest-kit/engine/pkg"
	"github.com/k8s-manifest-kit/engine/pkg/postrenderer"
	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"
)

// NewEngine builds the shared rendering engine around a native
// manifest-kit renderer. The library postrenderer.ApplyOrder is
// registered first, followed by the options' caller-supplied
// post-renderers, so ordering precedes injection exactly once for
// both backends. Every registered post-renderer is guarded by a
// context check, so an expired context stops the chain between
// processors instead of publishing successful output; the framework
// deploy action sorts the enriched batch again before writes, so no
// additional final engine sort is added here.
func NewEngine(r enginetypes.Renderer, opts ...RenderOption) (*engine.Engine, error) {
	options := RenderOptions{}

	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&options)
		}
	}

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

// ResolveEntry resolves an authored manifest path against the
// definition file's directory. An empty path — an omitted block or an
// omitted path — resolves to the definition file's directory itself;
// the path is otherwise used exactly as configured, and an absolute
// path is honored as is. Existence and shape of the entry are the
// render engines' to validate, so a missing or unsuitable entry fails
// at render time.
func ResolveEntry(root string, authored string) string {
	if filepath.IsAbs(authored) {
		return filepath.Clean(authored)
	}

	return filepath.Join(root, authored)
}

// DecodeHelmValues decodes authored literal chart values to the map
// the Helm renderer consumes. Each entry is raw JSON exactly as
// authored — placeholder-looking text stays literal — and the round
// trip cannot fail for content that loaded successfully, but a
// malformed entry is reported rather than dropped.
//
// A null entry is represented by apiextensions JSON with empty Raw —
// its UnmarshalJSON leaves Raw untouched for null while MarshalJSON
// emits null again — so empty Raw decodes as JSON null, not as an EOF
// failure.
func DecodeHelmValues(values map[string]apiextensionsv1.JSON) (enginetypes.Values, error) {
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
