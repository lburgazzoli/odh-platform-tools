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

package renderer

import (
	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// RenderOptions holds the runtime rendering inputs that offline lint
// rendering does not have.
//
// Namespace is the installation namespace a deploy renders into: for the
// Helm backend it becomes the release namespace — .Release.Namespace
// for chart templates — without filling metadata.namespace itself, and
// for the Kustomize backend the engine's real namespace transformer
// applies it to namespaced objects, subjects, and references at render
// time while cluster-scoped objects keep their scope. The zero value
// leaves rendering untouched, matching offline lint output exactly.
//
// PostRenderers are caller-supplied post-renderers the shared engine
// applies after the library apply-order post-renderer. Lint supplies
// none; deploy registers the adaptation injectors here so ordering is
// enforced once, at engine level, instead of per backend.
type RenderOptions struct {
	// Namespace is the installation namespace a deploy renders into;
	// the zero value leaves rendering untouched.
	Namespace string

	// PostRenderers run after the shared engine's apply-order
	// post-renderer, in the order supplied.
	PostRenderers []enginetypes.PostRenderer
}

// ApplyTo implements RenderOption, so a whole RenderOptions can be
// passed as a bundled preset. The processor slice is cloned to exact
// length, so later appends never reach the preset's own backing array.
func (opts RenderOptions) ApplyTo(target *RenderOptions) {
	preset := RenderOptions{
		Namespace:     opts.Namespace,
		PostRenderers: make([]enginetypes.PostRenderer, len(opts.PostRenderers)),
	}
	copy(preset.PostRenderers, opts.PostRenderers)

	*target = preset
}

// RenderOption customizes one render. Options apply in order over the
// defaults; a nil option is ignored.
type RenderOption = option.Option[RenderOptions]

// WithNamespace sets the installation namespace a deploy renders into,
// replacing any earlier namespace option. An empty namespace leaves
// rendering untouched, matching offline lint output exactly.
func WithNamespace(namespace string) RenderOption {
	return option.FunctionalOption[RenderOptions](func(target *RenderOptions) {
		target.Namespace = namespace
	})
}

// WithPostRenderer appends one caller post-renderer after the
// post-renderers already collected; ordering is the application order.
func WithPostRenderer(postRenderer enginetypes.PostRenderer) RenderOption {
	return option.FunctionalOption[RenderOptions](func(target *RenderOptions) {
		target.PostRenderers = append(target.PostRenderers, postRenderer)
	})
}
