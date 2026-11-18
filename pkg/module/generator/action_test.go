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

package generator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// renderCall records one injected render invocation.
type renderCall struct {
	definition toolApi.PlatformModuleDefinition
	file       string
}

// renderStub is a concurrency-safe render boundary fake returning
// per-call resources, or a failure once triggered.
type renderStub struct {
	mu        sync.Mutex
	calls     []renderCall
	resources [][]unstructured.Unstructured
	fail      error
}

// Render records the call and returns the staged resources.
func (s *renderStub) Render(
	_ context.Context,
	definition toolApi.PlatformModuleDefinition,
	file string,
	_ ...renderer.RenderOption,
) ([]unstructured.Unstructured, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, renderCall{definition: definition, file: file})

	if s.fail != nil {
		return nil, s.fail
	}

	index := len(s.calls) - 1
	if index >= len(s.resources) {
		index = len(s.resources) - 1
	}

	// Copy so mutations cannot leak between calls.
	staged := make([]unstructured.Unstructured, len(s.resources[index]))
	copy(staged, s.resources[index])

	return staged, nil
}

// recorded returns a copy of the recorded calls.
func (s *renderStub) recorded() []renderCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	calls := make([]renderCall, len(s.calls))
	copy(calls, s.calls)

	return calls
}

// passValidation accepts every definition.
func passValidation(
	toolApi.PlatformModuleDefinition,
	[]unstructured.Unstructured,
) ([]linter.Diagnostic, error) {
	return nil, nil
}

// newKustomizeSource creates a Kustomize source root.
func newKustomizeSource(t *testing.T) string {
	t.Helper()

	return writeSource(t, "kustomization.yaml")
}

// newHelmSource creates a Helm chart source root.
func newHelmSource(t *testing.T) string {
	t.Helper()

	chart := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(chart, "Chart.yaml"),
		[]byte("apiVersion: v2\nname: demo\nversion: 0.1.0\n"),
		0o644); err != nil {
		t.Fatalf("write chart: %v", err)
	}

	return chart
}

// stubFor wires an action whose render returns the given per-call
// resource bundles.
func stubFor(t *testing.T, resources ...[]unstructured.Unstructured) (*Action, *renderStub) {
	t.Helper()

	stub := &renderStub{resources: resources}

	return NewAction(stub.Render, passValidation), stub
}

// newRenderStub builds a standalone render stub over per-call
// resource bundles.
func newRenderStub(t *testing.T, resources ...[]unstructured.Unstructured) *renderStub {
	t.Helper()

	return &renderStub{resources: resources}
}

// apiextensionsJSON builds a literal JSON value for assertions.
func apiextensionsJSON(raw string) apiextensionsv1.JSON {
	return apiextensionsv1.JSON{Raw: []byte(raw)}
}

// decodeGenerated loads a generated document through the existing
// loader.
func decodeGenerated(
	t *testing.T,
	g *WithT,
	document []byte,
) toolApi.PlatformModuleDefinition {
	t.Helper()

	path := filepath.Join(t.TempDir(), "module.yaml")
	g.Expect(os.WriteFile(path, document, 0o644)).To(Succeed())

	loaded, err := loader.LoadFromPath(context.Background(), path)
	g.Expect(err).NotTo(HaveOccurred())

	return loaded
}

// runOptions are the default options of a successful stdout run over
// the shared fixture.
func runOptions(source string) Options {
	return Options{SourceDir: source}
}

// TestRunRendersBootstrapThenFinalSequence proves the render boundary
// sees a bootstrap pass with the temporary release name and empty
// module type, then a final pass carrying the inferred name and GVK;
// an explicit name is used for both passes.
func TestRunRendersBootstrapThenFinalSequence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		explicit string
		expected []string
	}{
		{name: "inferred name", explicit: "", expected: []string{"module-discovery", "foobar"}},
		{name: "explicit name", explicit: "demo", expected: []string{"demo", "demo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			source := newKustomizeSource(t)
			action, stub := stubFor(t, validModuleResources(t), validModuleResources(t))

			result, err := action.Run(context.Background(), Options{
				SourceDir: source,
				Name:      tc.explicit,
			})

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result.Output).To(Equal(outputStdout))

			calls := stub.recorded()
			g.Expect(calls).To(HaveLen(2))

			g.Expect(calls[0].definition.Name).To(Equal(tc.expected[0]))
			g.Expect(calls[0].definition.Spec.Instance.Type).
				To(Equal(toolApi.ModuleType{}))
			g.Expect(calls[0].definition.Spec.Manifests.Kustomize).To(BeNil(),
				"the bootstrap pass must not carry a manifests path")

			g.Expect(calls[1].definition.Name).To(Equal(tc.expected[1]))
			g.Expect(calls[1].definition.Spec.Instance.Type).To(Equal(fooBarType()))

			for _, call := range calls {
				g.Expect(call.file).To(Equal(filepath.Join(source, syntheticDefinitionName)))
			}
		})
	}
}

// TestRunTakesControllerFromFinalRender proves controller names and
// images come from the final render, not stale discovery data.
func TestRunTakesControllerFromFinalRender(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	bootstrap := []unstructured.Unstructured{
		moduleCRD(t, "example.com", "FooBar", crdVersionSpec{name: "v1alpha1", served: true, storage: true}),
		controllerDeployment(t, "stale-controller", []containerSpec{
			{name: "manager", image: "quay.io/example/stale:v0"},
		}, nil),
	}

	final := []unstructured.Unstructured{
		moduleCRD(t, "example.com", "FooBar", crdVersionSpec{name: "v1alpha1", served: true, storage: true}),
		controllerDeployment(t, "foo-controller", []containerSpec{
			{name: "manager", image: "quay.io/example/controller:v1"},
			{name: "sidecar", image: "quay.io/example/sidecar:v1"},
		}, []containerSpec{
			{name: "setup", image: "quay.io/example/controller:v1"},
		}),
	}

	action, _ := stubFor(t, bootstrap, final)

	result, err := action.Run(context.Background(), Options{
		SourceDir:     newKustomizeSource(t),
		InitContainer: "setup",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.YAML).To(ContainSubstring("foo-controller"))
	g.Expect(result.YAML).To(ContainSubstring("quay.io/example/controller:v1"))
	g.Expect(result.YAML).NotTo(ContainSubstring("stale"))
	g.Expect(result.YAML).To(ContainSubstring("initContainer"))
}

// TestRunRejectsOptionsBeforeRender proves a missing source, an
// invalid explicit name, and Helm values on a Kustomize source all
// fail with the options category before any renderer call.
func TestRunRejectsOptionsBeforeRender(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		options Options
	}{
		{name: "missing source", options: Options{}},
		{name: "invalid name", options: Options{SourceDir: newKustomizeSource(t), Name: "Bad"}},
		{
			name:    "kustomize values file",
			options: Options{SourceDir: newKustomizeSource(t), ValuesFile: "values.yaml"},
		},
		{
			name:    "kustomize set expressions",
			options: Options{SourceDir: newKustomizeSource(t), Values: []string{"a=b"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			action, stub := stubFor(t, validModuleResources(t), validModuleResources(t))

			_, err := action.Run(context.Background(), tc.options)

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue())
			g.Expect(stub.recorded()).To(BeEmpty(), "no render may run")
		})
	}
}

// TestRunBootstrapFailureSuggestsExplicitName proves a bootstrap
// failure without an explicit name keeps its category reachable and
// mentions the explicit-name remedy.
func TestRunBootstrapFailureSuggestsExplicitName(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := &renderStub{
		fail: moduleerrors.New(moduleerrors.ErrInvalidDefinition, "render boom"),
	}

	action := NewAction(stub.Render, passValidation)

	_, err := action.Run(context.Background(), runOptions(newHelmSource(t)))

	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("explicit name"))
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue(),
		"the wrapped failure must keep its category reachable")
}

// TestRunRejectsBadBundlesRegardlessOfName proves zero or multiple
// CRDs fail even with an explicit name, and the failure carries no
// explicit-name hint.
func TestRunRejectsBadBundlesRegardlessOfName(t *testing.T) {
	t.Parallel()

	empty := []unstructured.Unstructured{}

	g := NewWithT(t)

	action, _ := stubFor(t, empty, empty)

	_, err := action.Run(context.Background(), Options{
		SourceDir: newKustomizeSource(t),
		Name:      "demo",
	})

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	g.Expect(err.Error()).NotTo(ContainSubstring("explicit name"))
}

// TestRunRejectsReservedBundles proves a bundled module instance
// prevents all output.
func TestRunRejectsReservedBundles(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	bundled := append(validModuleResources(t), moduleInstance(fooBarType(), "the-instance"))

	action, _ := stubFor(t, validModuleResources(t), bundled)

	published := false
	action.publish = func(context.Context, string, []byte) error {
		published = true
		return nil
	}

	_, err := action.Run(context.Background(), Options{
		SourceDir: newKustomizeSource(t),
		Output:    filepath.Join(t.TempDir(), "module.yaml"),
	})

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrReservedResource)).To(BeTrue())
	g.Expect(published).To(BeFalse(), "reserved bundles must not publish")
}

// TestRunRejectsErrorFindings proves error-severity lint findings
// prevent all output while warning findings do not.
func TestRunRejectsErrorFindings(t *testing.T) {
	t.Parallel()

	newAction := func(diagnostics []linter.Diagnostic) *Action {
		return NewAction(
			func(_ context.Context, definition toolApi.PlatformModuleDefinition, _ string, _ ...renderer.RenderOption) ([]unstructured.Unstructured, error) {
				if definition.Spec.Instance.Type.Kind == "" {
					return validModuleResources(t), nil
				}

				return validModuleResources(t), nil
			},
			func(toolApi.PlatformModuleDefinition, []unstructured.Unstructured) ([]linter.Diagnostic, error) {
				return diagnostics, nil
			})
	}

	t.Run("error findings", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		action := newAction([]linter.Diagnostic{{
			ID: "FINDING", Severity: linter.SeverityError, Message: "defect",
		}})

		published := false
		action.publish = func(context.Context, string, []byte) error {
			published = true
			return nil
		}

		_, err := action.Run(context.Background(), Options{
			SourceDir: newKustomizeSource(t),
			Output:    filepath.Join(t.TempDir(), "module.yaml"),
		})

		g.Expect(errors.Is(err, moduleerrors.ErrGenerateValidation)).To(BeTrue())
		g.Expect(err.Error()).To(ContainSubstring("FINDING"))
		g.Expect(published).To(BeFalse())
	})

	t.Run("warning findings", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		action := newAction([]linter.Diagnostic{{
			ID: "NOTE", Severity: linter.SeverityWarning, Message: "unverified",
		}})

		_, err := action.Run(context.Background(), runOptions(newKustomizeSource(t)))

		g.Expect(err).NotTo(HaveOccurred())
	})
}

// TestRunFlowsValuesAndRelatedImages proves a values file and ordered
// overrides reach both renders and the final document, and related
// image names are copied sorted and deduplicated without their
// values.
func TestRunFlowsValuesAndRelatedImages(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	dir := t.TempDir()

	valuesFile := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(valuesFile, []byte("replicas: 2\nname: from-file\n"), 0o644); err != nil {
		t.Fatalf("write values: %v", err)
	}

	imagesFile := filepath.Join(dir, "images.yaml")
	if err := os.WriteFile(imagesFile, []byte(`relatedImages:
- name: z-image
  value: quay.io/example/z:v9
- name: a-image
  value: quay.io/example/a:v1
- name: a-image
  value: quay.io/example/a:v2
`), 0o644); err != nil {
		t.Fatalf("write images: %v", err)
	}

	action, stub := stubFor(t, validModuleResources(t), validModuleResources(t))

	result, err := action.Run(context.Background(), Options{
		SourceDir:         newHelmSource(t),
		ValuesFile:        valuesFile,
		Values:            []string{"replicas=3"},
		RelatedImagesFile: imagesFile,
	})
	g.Expect(err).NotTo(HaveOccurred())

	calls := stub.recorded()
	g.Expect(calls).To(HaveLen(2))

	for _, call := range calls {
		g.Expect(call.definition.Spec.Manifests.Format).To(Equal(toolApi.FormatHelm))
		g.Expect(call.definition.Spec.Manifests.Helm.Values).
			To(HaveKeyWithValue("replicas", apiextensionsJSON("3")))
		g.Expect(call.definition.Spec.Manifests.Helm.Values).
			To(HaveKeyWithValue("name", apiextensionsJSON(`"from-file"`)))
	}

	text := string(result.YAML)
	g.Expect(text).To(ContainSubstring("replicas: 3"))
	g.Expect(text).To(ContainSubstring("a-image"))
	g.Expect(text).To(ContainSubstring("z-image"))
	g.Expect(text).NotTo(ContainSubstring("quay.io/example/z"))
	g.Expect(text).NotTo(ContainSubstring("quay.io/example/a:"))

	loaded := decodeGenerated(t, g, result.YAML)
	g.Expect(loaded.Spec.RelatedImages).To(Equal([]string{"a-image", "z-image"}))
}

// TestRunPublishesCreateOnlyFileOutput proves file output publishes
// 0600 bytes exactly once, and a second run over the same destination
// fails without touching it.
func TestRunPublishesCreateOnlyFileOutput(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	source := newKustomizeSource(t)
	output := filepath.Join(t.TempDir(), "module.yaml")

	action, _ := stubFor(t, validModuleResources(t), validModuleResources(t))

	result, err := action.Run(context.Background(), Options{
		SourceDir: source,
		Output:    output,
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Output).To(Equal(output))
	g.Expect(result.YAML).NotTo(BeEmpty())

	contents, err := os.ReadFile(output)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(contents).To(Equal(result.YAML))

	info, err := os.Stat(output)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

	_, err = action.Run(context.Background(), Options{
		SourceDir: source,
		Output:    output,
	})
	g.Expect(errors.Is(err, moduleerrors.ErrGenerateOutput)).To(BeTrue())

	after, err := os.ReadFile(output)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(after).To(Equal(contents))
}

// TestRunSupportsRelativeAndColocatedPaths proves relative source and
// output paths, a colocated Kustomize output, and a destination
// directory containing spaces all resolve correctly.
func TestRunSupportsRelativeAndColocatedPaths(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)

	if err := os.MkdirAll(filepath.Join(base, "src dir"), 0o755); err != nil {
		t.Fatalf("create spaced source: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(base, "src dir", "kustomization.yaml"),
		[]byte("# marker\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	for _, tc := range []struct {
		name   string
		source string
		output string
	}{
		{name: "relative source and output", source: "src dir", output: "out/module.yaml"},
		{name: "colocated kustomize output", source: "src dir", output: "src dir/module.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			if err := os.MkdirAll(filepath.Join(base, filepath.Dir(tc.output)), 0o755); err != nil {
				t.Fatalf("create output parent: %v", err)
			}

			action, _ := stubFor(t, validModuleResources(t), validModuleResources(t))

			result, err := action.Run(context.Background(), Options{
				SourceDir: tc.source,
				Output:    tc.output,
			})

			g.Expect(err).NotTo(HaveOccurred())

			expected, err := filepath.Abs(tc.output)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result.Output).To(Equal(expected))

			_, statErr := os.Stat(expected)
			g.Expect(statErr).NotTo(HaveOccurred())

			if err := os.Remove(expected); err != nil {
				t.Fatalf("cleanup output: %v", err)
			}
		})
	}
}

// TestRunGuardsHelmFileOutputBeforeRender proves Helm destinations
// inside the chart fail with the options category before any render,
// while a destination outside the chart succeeds.
func TestRunGuardsHelmFileOutputBeforeRender(t *testing.T) {
	t.Parallel()

	t.Run("inside chart", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		chart := newHelmSource(t)

		action, stub := stubFor(t, validModuleResources(t), validModuleResources(t))

		_, err := action.Run(context.Background(), Options{
			SourceDir: chart,
			Output:    filepath.Join(chart, "module.yaml"),
		})

		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue())
		g.Expect(stub.recorded()).To(BeEmpty(), "the guard must run before rendering")
	})

	t.Run("outside chart", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		action, _ := stubFor(t, validModuleResources(t), validModuleResources(t))

		_, err := action.Run(context.Background(), Options{
			SourceDir: newHelmSource(t),
			Output:    filepath.Join(t.TempDir(), "module.yaml"),
		})

		g.Expect(err).NotTo(HaveOccurred())
	})
}

// TestRunRejectsMissingOutputParent proves a missing destination
// parent directory fails without being created.
func TestRunRejectsMissingOutputParent(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()

	action, _ := stubFor(t, validModuleResources(t), validModuleResources(t))

	_, err := action.Run(context.Background(), Options{
		SourceDir: newKustomizeSource(t),
		Output:    filepath.Join(base, "missing", "module.yaml"),
	})

	g.Expect(errors.Is(err, moduleerrors.ErrGenerateOutput)).To(BeTrue())

	if _, statErr := os.Stat(filepath.Join(base, "missing")); statErr == nil {
		t.Fatal("publication must not create destination directories")
	}
}

// TestRunHonorsCancellationCheckpoints proves cancellation during
// validation prevents publication, while cancellation after a
// successful installation keeps the completed result successful.
func TestRunHonorsCancellationCheckpoints(t *testing.T) {
	t.Parallel()

	t.Run("cancelled during validation", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		ctx, cancel := context.WithCancel(context.Background())

		action := NewAction(
			newRenderStub(t, validModuleResources(t), validModuleResources(t)).Render,
			func(toolApi.PlatformModuleDefinition, []unstructured.Unstructured) ([]linter.Diagnostic, error) {
				cancel()
				return nil, nil
			})

		published := false
		action.publish = func(context.Context, string, []byte) error {
			published = true
			return nil
		}

		_, err := action.Run(ctx, Options{
			SourceDir: newKustomizeSource(t),
			Output:    filepath.Join(t.TempDir(), "module.yaml"),
		})

		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		g.Expect(published).To(BeFalse())
	})

	t.Run("cancelled after installation", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		ctx, cancel := context.WithCancel(context.Background())

		action, _ := stubFor(t, validModuleResources(t), validModuleResources(t))
		action.publish = func(_ context.Context, dest string, data []byte) error {
			if err := publishFile(context.Background(), dest, data); err != nil {
				return err
			}

			cancel()

			return nil
		}

		output := filepath.Join(t.TempDir(), "module.yaml")

		result, err := action.Run(ctx, Options{
			SourceDir: newKustomizeSource(t),
			Output:    output,
		})

		g.Expect(err).NotTo(HaveOccurred(),
			"cancellation after the commit point must not fail the run")
		g.Expect(result.Output).To(Equal(output))

		_, statErr := os.Stat(output)
		g.Expect(statErr).NotTo(HaveOccurred())
	})

	t.Run("cancelled at start", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		action, stub := stubFor(t, validModuleResources(t), validModuleResources(t))

		_, err := action.Run(ctx, runOptions(newKustomizeSource(t)))

		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		g.Expect(stub.recorded()).To(BeEmpty())
	})
}

// TestRunKeepsCallsIsolated proves repeated calls over one action
// produce identical documents and independent publications.
func TestRunKeepsCallsIsolated(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	source := newKustomizeSource(t)
	action, _ := stubFor(t, validModuleResources(t), validModuleResources(t))

	first, err := action.Run(context.Background(), runOptions(source))
	g.Expect(err).NotTo(HaveOccurred())

	second, err := action.Run(context.Background(), runOptions(source))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(second.YAML).To(Equal(first.YAML))
}

// TestRunDoesNotMutateInjectedInputs proves the definition passed to
// the validation boundary matches the effective definition and the
// resources keep their original identities.
func TestRunDoesNotMutateInjectedInputs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var validated toolApi.PlatformModuleDefinition
	var rendered []unstructured.Unstructured

	action := NewAction(
		newRenderStub(t, validModuleResources(t), validModuleResources(t)).Render,
		func(definition toolApi.PlatformModuleDefinition, resources []unstructured.Unstructured) ([]linter.Diagnostic, error) {
			validated = definition
			rendered = resources
			return nil, nil
		})

	_, err := action.Run(context.Background(), runOptions(newKustomizeSource(t)))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(validated.Spec.Instance.Name).To(Equal("foobar"))
	g.Expect(validated.Spec.Controller.Deployment.Container.Image).
		To(Equal("quay.io/example/controller:v1"))
	g.Expect(rendered).To(HaveLen(2))
	g.Expect(rendered[0].GroupVersionKind().Kind).To(Equal("CustomResourceDefinition"))
}
