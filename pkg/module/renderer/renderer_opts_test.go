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
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// optsTestRenderer is a deterministic native renderer: it returns its
// fixed batch in a deliberately unsorted order, so engine-level tests
// observe only processor behavior.
type optsTestRenderer struct{}

// Name implements enginetypes.Renderer.
func (optsTestRenderer) Name() string {
	return "opts-test"
}

// Process implements enginetypes.Renderer.
func (optsTestRenderer) Process(_ context.Context, _ enginetypes.Values) ([]unstructured.Unstructured, error) {
	return []unstructured.Unstructured{
		optsTestObject("Deployment", "", "synthetic-controller"),
		optsTestObject("ConfigMap", "beta", "shared"),
		optsTestObject("ConfigMap", "alpha", "shared"),
	}, nil
}

// optsTestObject builds one minimal unstructured object.
func optsTestObject(kind string, namespace string, name string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}}
}

// recordingPostRenderer returns a post-renderer that records its name
// and passes the batch through unchanged.
func recordingPostRenderer(calls *[]string, name string) enginetypes.PostRenderer {
	return func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		*calls = append(*calls, name)

		return objects, nil
	}
}

// TestNewEngineResolvesRenderOptions proves the option machinery
// through the real shared engine: nil interface and typed nil
// functional options are ignored, a whole RenderOptions preset and
// WithPostRenderer options apply in order exactly once, and the
// library apply-order post-renderer runs first — every caller
// processor sees the whole batch already apply-order sorted.
func TestNewEngineResolvesRenderOptions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var calls []string
	var batches [][]string

	observing := func(name string) enginetypes.PostRenderer {
		return func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			calls = append(calls, name)

			seen := make([]string, 0, len(objects))
			for i := range objects {
				seen = append(seen, objects[i].GetNamespace()+"/"+objects[i].GetName())
			}
			batches = append(batches, seen)

			return objects, nil
		}
	}

	shared, err := NewEngine(
		optsTestRenderer{},
		nil,
		option.FunctionalOption[RenderOptions](nil),
		RenderOptions{PostRenderers: []enginetypes.PostRenderer{observing("preset")}},
		WithPostRenderer(observing("first")),
		WithPostRenderer(observing("second")),
	)
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := shared.Render(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	// Nil options were ignored; the preset and the WithXxx options
	// applied in application order, exactly once each.
	g.Expect(calls).To(Equal([]string{"preset", "first", "second"}))

	// Every processor saw the whole batch once, already sorted by the
	// library apply-order post-renderer ahead of the first caller
	// processor: the ConfigMaps arrive namespace-sorted ahead of the
	// namespace-less Deployment.
	sorted := []string{"alpha/shared", "beta/shared", "/synthetic-controller"}
	g.Expect(batches).To(Equal([][]string{sorted, sorted, sorted}))

	// The engine returned the processed batch with no further sort.
	g.Expect(len(resources)).To(Equal(3))
	g.Expect(resources[0].GetNamespace() + "/" + resources[0].GetName()).To(Equal("alpha/shared"))
}

// TestRenderOptionsApplyToPinsOrderingAndCloning proves option
// resolution semantics at the ApplyTo level: a preset replaces the
// collected settings and clones its processor slice to exact length,
// WithNamespace replaces, and WithPostRenderer appends.
func TestRenderOptionsApplyToPinsOrderingAndCloning(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var calls []string

	preset := RenderOptions{
		Namespace:     "preset-namespace",
		PostRenderers: []enginetypes.PostRenderer{recordingPostRenderer(&calls, "preset")},
	}

	resolved := RenderOptions{}
	preset.ApplyTo(&resolved)
	WithNamespace("modules").ApplyTo(&resolved)
	WithPostRenderer(recordingPostRenderer(&calls, "first")).ApplyTo(&resolved)
	WithPostRenderer(recordingPostRenderer(&calls, "second")).ApplyTo(&resolved)

	g.Expect(resolved.Namespace).To(Equal("modules"))
	g.Expect(resolved.PostRenderers).To(HaveLen(3))

	for _, postRenderer := range resolved.PostRenderers {
		_, _ = postRenderer(t.Context(), nil)
	}
	g.Expect(calls).To(Equal([]string{"preset", "first", "second"}))

	// The preset's own slice is unchanged by the resolution.
	g.Expect(preset.PostRenderers).To(HaveLen(1))
}

// TestRenderOptionsPresetDoesNotLeakAppendsIntoCallerStorage proves
// the clone in ApplyTo: a post-renderer appended during resolution
// lands in the resolved copy, never in the caller's spare-capacity
// backing array — appending to the caller's own slice afterwards can
// never observe or overwrite the option's processor.
func TestRenderOptionsPresetDoesNotLeakAppendsIntoCallerStorage(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var callerCalls []string
	var optionCalls []string

	// One processor with spare capacity behind it.
	callerSlice := make([]enginetypes.PostRenderer, 1, 2)
	callerSlice[0] = recordingPostRenderer(&callerCalls, "caller-first")

	resolved := RenderOptions{}
	(RenderOptions{PostRenderers: callerSlice}).ApplyTo(&resolved)
	WithPostRenderer(recordingPostRenderer(&optionCalls, "option")).ApplyTo(&resolved)

	g.Expect(resolved.PostRenderers).To(HaveLen(2))

	// The caller keeps filling its own spare capacity: the stored
	// processor is the caller's, not the option's.
	callerSlice = append(callerSlice, recordingPostRenderer(&callerCalls, "caller-second"))

	_, _ = callerSlice[1](t.Context(), nil)
	g.Expect(callerCalls).To(Equal([]string{"caller-second"}))
	g.Expect(optionCalls).To(BeEmpty())

	_, _ = resolved.PostRenderers[1](t.Context(), nil)
	g.Expect(optionCalls).To(Equal([]string{"option"}))
}
