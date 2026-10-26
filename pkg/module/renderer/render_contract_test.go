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
	"errors"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// contractResource builds one minimal unstructured object.
func contractResource(kind string, namespace string, name string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}}
}

// writeContractChartFixture writes a Helm module rendering one
// ordinary ConfigMap, the enrichment target of the contract tests.
func writeContractChartFixture(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", helmChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", helmValuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/configmap.yaml", guardOrdinaryConfigMapTemplateYAML)
}

// TestRenderGuardRejectsCallerEnrichedReservedIdentities proves the
// guard runs after caller enrichment: a post-renderer that appends a
// reserved identity — or rewrites an ordinary rendered object into
// one — fails the whole render with the sentinel and a nil batch.
// Reserved objects are never filtered out of successful output.
func TestRenderGuardRejectsCallerEnrichedReservedIdentities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
	}{
		{"module instance at another version"},
		{"platform module definition"},
		{"reserved definition CRD"},
	}

	documents := map[string]string{
		"module instance at another version": guardModuleInstanceTemplateYAML,
		"platform module definition":         guardDefinitionTemplateYAML,
		"reserved definition CRD":            guardReservedCRDTemplateYAML,
	}

	modes := []struct {
		name string
		// enrich returns the batch a caller post-renderer produces
		// from the rendered objects and the case's reserved document.
		enrich func(t *testing.T, document unstructured.Unstructured, objects []unstructured.Unstructured) []unstructured.Unstructured
	}{
		{
			name: "appended by the caller",
			enrich: func(t *testing.T, document unstructured.Unstructured, objects []unstructured.Unstructured) []unstructured.Unstructured {
				t.Helper()

				return append(objects, document)
			},
		},
		{
			name: "rewritten from an ordinary rendered object",
			enrich: func(t *testing.T, document unstructured.Unstructured, _ []unstructured.Unstructured) []unstructured.Unstructured {
				t.Helper()

				// The whole batch is replaced: the ordinary rendered
				// objects are gone and only the reserved document —
				// whose own name and identity are kept — remains.
				return []unstructured.Unstructured{document}
			},
		},
	}

	for _, mode := range modes {
		for _, testCase := range cases {
			t.Run(mode.name+" / "+testCase.name, func(t *testing.T) {
				t.Parallel()

				g := NewWithT(t)

				root := t.TempDir()
				writeContractChartFixture(t, root)

				definition := loadDefinition(t, root)
				definitionFile := filepath.Join(root, "definition.yaml")

				document := guardResource(t, documents[testCase.name])

				enricher := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
					return mode.enrich(t, document, objects), nil
				}

				resources, err := Render(t.Context(), definition, definitionFile, WithPostRenderer(enricher))

				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.Is(err, ErrReservedResource)).To(BeTrue())
				g.Expect(resources).To(BeNil())
			})
		}
	}
}

// TestRenderGuardAllowsCallerAppendedOrdinaryResources proves caller
// enrichment with ordinary resources still succeeds and returns the
// enriched batch: the guard rejects only reserved identities.
func TestRenderGuardAllowsCallerAppendedOrdinaryResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	enricher := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		return append(objects, contractResource("ConfigMap", "modules", "caller-added")), nil
	}

	resources, err := Render(t.Context(), definition, definitionFile, WithPostRenderer(enricher))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(findResource(t, resources, "ConfigMap", "module-config")).NotTo(BeZero())
	g.Expect(findResource(t, resources, "ConfigMap", "caller-added")).NotTo(BeZero())
}

// TestRenderExpiredDeadlinePreventsEveryCallback proves an
// already-expired deadline fails the render before any backend or
// caller processor runs, keeping the DeadlineExceeded identity and a
// nil batch.
func TestRenderExpiredDeadlinePreventsEveryCallback(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)

	ran := false
	observe := func(context.Context, []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		ran = true

		return nil, nil
	}

	resources, err := Render(ctx, definition, definitionFile, WithPostRenderer(observe))

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(resources).To(BeNil())
	g.Expect(ran).To(BeFalse())
}

// TestRenderCancellationAtFinalProcessorNeverPublishesOutput proves
// the final context check: a last caller post-renderer that cancels
// the context lets the processor chain complete, but the render never
// becomes successful output — the failure keeps the cancellation
// identity and returns a nil batch after every processor ran once.
func TestRenderCancellationAtFinalProcessorNeverPublishesOutput(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	calls := 0

	count := func(context.Context, []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		calls++

		return nil, nil
	}

	cancelNow := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		cancel()

		return objects, nil
	}

	resources, err := Render(ctx, definition, definitionFile,
		WithPostRenderer(count),
		WithPostRenderer(count),
		WithPostRenderer(cancelNow),
	)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	g.Expect(resources).To(BeNil())
	g.Expect(calls).To(Equal(2))
}

// TestRenderProcessorErrorWithPartialOutput proves a caller processor
// that both enriches the batch and fails aborts the render with its
// own error identity: the partial output is never returned, and no
// later callback runs.
func TestRenderProcessorErrorWithPartialOutput(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	processorFailure := errors.New("processor failure")

	laterRan := false

	failing := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		return append(objects, contractResource("ConfigMap", "modules", "partial")), processorFailure
	}

	never := func(context.Context, []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		laterRan = true

		return nil, nil
	}

	resources, err := Render(t.Context(), definition, definitionFile,
		WithPostRenderer(failing),
		WithPostRenderer(never),
	)

	g.Expect(err).To(MatchError(processorFailure))
	g.Expect(resources).To(BeNil())
	g.Expect(laterRan).To(BeFalse())
}

// TestExecuteDoesNotResortAfterCallerEnrichment proves no extra final
// sort exists behind the engine: a caller processor appends an
// ordinary object that ApplyOrder would sort ahead of the existing
// output, and the returned batch keeps it appended — the framework
// deploy action, not the render, owns the final ordering.
func TestExecuteDoesNotResortAfterCallerEnrichment(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	// optsTestRenderer's batch sorts, under ApplyOrder, to
	// alpha/shared, beta/shared, then the namespace-less Deployment.
	appendSortAhead := func(_ context.Context, objects []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
		// alpha/aaa sorts ahead of every existing object under
		// ApplyOrder: a final sort would move it to the front.
		return append(objects, optsTestObject("ConfigMap", "alpha", "aaa")), nil
	}

	resources, err := execute(t.Context(), guardDefinition(), optsTestRenderer{}, RenderOptions{
		PostRenderers: []enginetypes.PostRenderer{appendSortAhead},
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resources).To(HaveLen(4))

	// The appended object stays appended: the sorted prefix is
	// untouched and the enrichment remains at the end.
	g.Expect(resources[0].GetNamespace() + "/" + resources[0].GetName()).To(Equal("alpha/shared"))
	g.Expect(resources[1].GetNamespace() + "/" + resources[1].GetName()).To(Equal("beta/shared"))
	g.Expect(resources[2].GetNamespace() + "/" + resources[2].GetName()).To(Equal("/synthetic-controller"))
	g.Expect(resources[3].GetNamespace() + "/" + resources[3].GetName()).To(Equal("alpha/aaa"))
}

// TestRenderIgnoresNilOptionsThroughRealBackend proves nil interface
// and typed nil functional options are ignored through the public
// service over a real backend: the render output is identical to the
// no-option call.
func TestRenderIgnoresNilOptionsThroughRealBackend(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	definition := loadDefinition(t, root)
	definitionFile := filepath.Join(root, "definition.yaml")

	plain, err := Render(t.Context(), definition, definitionFile)
	g.Expect(err).NotTo(HaveOccurred())

	withNils, err := Render(t.Context(), definition, definitionFile,
		nil,
		option.FunctionalOption[RenderOptions](nil),
	)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(withNils).To(Equal(plain))
}

// TestRenderNamespaceOptionLastWins proves the namespace option
// replaces, not accumulates: through the real Helm backend the last
// namespace option becomes the release namespace a template renders
// into.
func TestRenderNamespaceOptionLastWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", helmNamespacedTemplateYAML)

	resources, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"),
		WithNamespace("first"),
		WithNamespace("second"),
	)
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	namespace, found, err := unstructured.NestedString(deployment.Object, "metadata", "namespace")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(namespace).To(Equal("second"))
}

// TestRenderPostRendererOptionsAppendInOrder proves caller
// post-renderers apply in option order through the public service:
// the first registered processor observes the batch before the
// second, exactly once each, after the engine's apply-order
// post-renderer.
func TestRenderPostRendererOptionsAppendInOrder(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	var calls []string

	named := func(name string) enginetypes.PostRenderer {
		return func(context.Context, []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			calls = append(calls, name)

			return nil, nil
		}
	}

	_, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"),
		WithPostRenderer(named("first")),
		WithPostRenderer(named("second")),
	)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(calls).To(Equal([]string{"first", "second"}))
}

// TestRenderPresetDoesNotLeakCallerAppends proves a preset passed
// through the public service clones its processor slice: the caller
// filling its own spare capacity after the render can never observe
// or overwrite the option's processor.
func TestRenderPresetDoesNotLeakCallerAppends(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeContractChartFixture(t, root)

	var renderCalls []string
	var callerCalls []string

	named := func(calls *[]string, name string) enginetypes.PostRenderer {
		return func(context.Context, []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			*calls = append(*calls, name)

			return nil, nil
		}
	}

	// One processor with spare capacity behind it, applied as a
	// whole-preset option.
	callerSlice := make([]enginetypes.PostRenderer, 1, 2)
	callerSlice[0] = named(&renderCalls, "preset")

	_, err := Render(t.Context(), loadDefinition(t, root), filepath.Join(root, "definition.yaml"),
		RenderOptions{PostRenderers: callerSlice},
		WithPostRenderer(named(&renderCalls, "option")),
	)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(renderCalls).To(Equal([]string{"preset", "option"}))

	// The caller keeps filling its own spare capacity after the
	// render: the stored processor is the caller's, and the render's
	// recorded calls are untouched.
	callerSlice = append(callerSlice, named(&callerCalls, "caller-second"))

	_, _ = callerSlice[1](t.Context(), nil)
	g.Expect(callerCalls).To(Equal([]string{"caller-second"}))
	g.Expect(renderCalls).To(Equal([]string{"preset", "option"}))
}
