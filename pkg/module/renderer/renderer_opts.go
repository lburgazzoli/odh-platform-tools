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

// RenderOptions configures namespace handling and post-rendering.
type RenderOptions struct {
	// Namespace becomes Helm's release namespace or Kustomize's namespace
	// transform target. Empty leaves namespace handling to the source.
	Namespace string

	// PostRenderers run after apply ordering, in the supplied order.
	PostRenderers []enginetypes.PostRenderer
}

// ApplyTo replaces the options and clones the processor slice.
func (opts RenderOptions) ApplyTo(target *RenderOptions) {
	preset := RenderOptions{
		Namespace:     opts.Namespace,
		PostRenderers: make([]enginetypes.PostRenderer, len(opts.PostRenderers)),
	}
	copy(preset.PostRenderers, opts.PostRenderers)

	*target = preset
}

// RenderOption customizes rendering; nil options are ignored.
type RenderOption = option.Option[RenderOptions]

// WithNamespace sets the backend namespace.
func WithNamespace(namespace string) RenderOption {
	return option.FunctionalOption[RenderOptions](func(target *RenderOptions) {
		target.Namespace = namespace
	})
}

// WithPostRenderer appends a processor.
func WithPostRenderer(postRenderer enginetypes.PostRenderer) RenderOption {
	return option.FunctionalOption[RenderOptions](func(target *RenderOptions) {
		target.PostRenderers = append(target.PostRenderers, postRenderer)
	})
}
