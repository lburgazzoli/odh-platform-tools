package linter

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
)

// actionCalls records the service invocations of one action run.
type actionCalls struct {
	sequence    []string
	contexts    []context.Context
	paths       []string
	definitions []v1alpha1.PlatformModuleDefinition
	resources   [][]unstructured.Unstructured
}

// loader returns a fake definition loader returning definition and
// err.
func (r *actionCalls) loader(definition v1alpha1.PlatformModuleDefinition, err error) DefinitionLoad {
	return func(ctx context.Context, path string, _ ...loader.LoadOption) (v1alpha1.PlatformModuleDefinition, error) {
		r.sequence = append(r.sequence, "load")
		r.contexts = append(r.contexts, ctx)
		r.paths = append(r.paths, path)

		return definition, err
	}
}

// renderer returns a fake backend renderer named backend, returning
// resources and err.
func (r *actionCalls) renderer(
	backend string,
	resources []unstructured.Unstructured,
	err error,
) ManifestRender {
	return func(ctx context.Context, definition v1alpha1.PlatformModuleDefinition, definitionFile string) ([]unstructured.Unstructured, error) {
		r.sequence = append(r.sequence, "render:"+backend)
		r.contexts = append(r.contexts, ctx)
		r.definitions = append(r.definitions, definition)
		r.paths = append(r.paths, definitionFile)

		return resources, err
	}
}

// validator returns a fake validation function returning diagnostics.
func (r *actionCalls) validator(diagnostics ...Diagnostic) Validation {
	return func(definition v1alpha1.PlatformModuleDefinition, resources []unstructured.Unstructured) ([]Diagnostic, error) {
		r.sequence = append(r.sequence, "validate")
		r.definitions = append(r.definitions, definition)
		r.resources = append(r.resources, resources)

		return diagnostics, nil
	}
}

// failingValidator returns a fake validation function returning err.
func failingValidator(err error) Validation {
	return func(v1alpha1.PlatformModuleDefinition, []unstructured.Unstructured) ([]Diagnostic, error) {
		return nil, err
	}
}

// testDefinition returns a minimal loaded definition for the action
// tests.
func testDefinition() v1alpha1.PlatformModuleDefinition {
	return v1alpha1.PlatformModuleDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic"},
		Spec: v1alpha1.PlatformModuleDefinitionSpec{
			Instance: v1alpha1.Instance{Type: v1alpha1.ModuleType{
				Group:   "components.platform.opendatahub.io",
				Version: "v1alpha1",
				Kind:    "SyntheticModule",
			}},
		},
	}
}

// helmDefinition returns a loaded definition selecting the Helm
// backend.
func helmDefinition() v1alpha1.PlatformModuleDefinition {
	definition := testDefinition()
	definition.Spec.Manifests.Format = v1alpha1.FormatHelm

	return definition
}

// kustomizeDefinition returns a loaded definition selecting the
// Kustomize backend.
func kustomizeDefinition() v1alpha1.PlatformModuleDefinition {
	definition := testDefinition()
	definition.Spec.Manifests.Format = v1alpha1.FormatKustomize

	return definition
}

// diagnosticIDs collects the check IDs of a lint result.
func diagnosticIDs(result Result) []string {
	ids := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		ids = append(ids, diagnostic.ID)
	}

	return ids
}

func TestActionRunValidatesOptions(t *testing.T) {
	g := NewWithT(t)

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil), calls.renderer("helm", nil, nil),
		calls.renderer("kustomize", nil, nil), calls.validator())

	result, err := action.Run(t.Context(), Options{})

	g.Expect(errors.Is(err, ErrInvalidOptions)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("DefinitionFile"))
	g.Expect(result.Diagnostics).To(BeEmpty())
	g.Expect(calls.sequence).To(BeEmpty())
}

func TestActionRunCancellationPrecedesInvalidOptions(t *testing.T) {
	g := NewWithT(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil), calls.renderer("helm", nil, nil),
		calls.renderer("kustomize", nil, nil), calls.validator())

	_, err := action.Run(ctx, Options{})

	g.Expect(err).To(MatchError(context.Canceled))
}

func TestActionRunStopsOnLoadError(t *testing.T) {
	g := NewWithT(t)

	loadFailure := errors.New("load failure")

	calls := &actionCalls{}
	action := NewAction(calls.loader(v1alpha1.PlatformModuleDefinition{}, loadFailure),
		calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil), calls.validator())

	result, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

	g.Expect(err).To(MatchError(loadFailure))
	g.Expect(result.Diagnostics).To(BeEmpty())
	g.Expect(calls.sequence).To(ConsistOf("load"))
}

func TestActionRunStopsOnRenderError(t *testing.T) {
	g := NewWithT(t)

	renderFailure := errors.New("render failure")

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil),
		calls.renderer("helm", nil, renderFailure), calls.renderer("kustomize", nil, nil),
		calls.validator())

	result, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

	g.Expect(err).To(MatchError(renderFailure))
	g.Expect(result.Diagnostics).To(BeEmpty())
	// No validation ran on top of a failed render.
	g.Expect(calls.sequence).To(ConsistOf("load", "render:helm"))
}

func TestActionRunSelectsBackendFromFormat(t *testing.T) {
	t.Parallel()

	t.Run("helm", func(t *testing.T) {
		g := NewWithT(t)

		calls := &actionCalls{}
		action := NewAction(calls.loader(helmDefinition(), nil),
			calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil),
			calls.validator())

		_, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(calls.sequence).To(Equal([]string{"load", "render:helm", "validate"}))
	})

	t.Run("kustomize", func(t *testing.T) {
		g := NewWithT(t)

		calls := &actionCalls{}
		action := NewAction(calls.loader(kustomizeDefinition(), nil),
			calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil),
			calls.validator())

		_, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(calls.sequence).To(Equal([]string{"load", "render:kustomize", "validate"}))
	})

	t.Run("unexpected format is a defensive error", func(t *testing.T) {
		g := NewWithT(t)

		definition := testDefinition()
		definition.Spec.Manifests.Format = v1alpha1.Format("bogus")

		calls := &actionCalls{}
		action := NewAction(calls.loader(definition, nil),
			calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil),
			calls.validator())

		result, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("bogus"))
		g.Expect(result.Diagnostics).To(BeEmpty())
		g.Expect(calls.sequence).To(ConsistOf("load"))
	})
}

func TestActionRunPropagatesContextAndInputs(t *testing.T) {
	g := NewWithT(t)

	rendered := []unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "synthetic-controller"},
	}}}

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil),
		calls.renderer("helm", rendered, nil), calls.renderer("kustomize", nil, nil),
		calls.validator())

	ctx := t.Context()
	_, err := action.Run(ctx, Options{DefinitionFile: "some/definition.yaml"})
	g.Expect(err).NotTo(HaveOccurred())

	// The caller's context and explicit file reach every service.
	g.Expect(calls.contexts).To(HaveLen(2))
	g.Expect(calls.contexts[0]).To(Equal(ctx))
	g.Expect(calls.contexts[1]).To(Equal(ctx))
	g.Expect(calls.paths).To(Equal([]string{"some/definition.yaml", "some/definition.yaml"}))

	// The renderer and the validation function receive the loaded
	// definition unchanged, and validation receives the rendered
	// objects as returned by the backend.
	g.Expect(calls.definitions).To(HaveLen(2))
	g.Expect(calls.definitions).To(ConsistOf(helmDefinition(), helmDefinition()))
	g.Expect(calls.resources).To(Equal([][]unstructured.Unstructured{rendered}))
}

func TestActionRunReturnsFindingsAsResults(t *testing.T) {
	g := NewWithT(t)

	// Deliberately unsorted: the action aggregates deterministically.
	findings := []Diagnostic{
		{ID: "B_FINDING", Severity: SeverityError, Message: "second"},
		{ID: "A_FINDING", Severity: SeverityError, Message: "first"},
	}

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil),
		calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil),
		calls.validator(findings...))

	result, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

	// Manifest defects are structured findings, not execution errors.
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.HasErrors()).To(BeTrue())
	g.Expect(result.HasWarnings()).To(BeFalse())
	g.Expect(diagnosticIDs(result)).To(Equal([]string{"A_FINDING", "B_FINDING"}))
}

func TestActionRunWarningOnlyResultSucceeds(t *testing.T) {
	g := NewWithT(t)

	findings := []Diagnostic{{ID: "A_FINDING", Severity: SeverityWarning, Message: "unverified"}}

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil),
		calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil),
		calls.validator(findings...))

	result, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.HasErrors()).To(BeFalse())
	g.Expect(result.HasWarnings()).To(BeTrue())
	g.Expect(diagnosticIDs(result)).To(ConsistOf("A_FINDING"))
}

func TestActionRunStopsOnValidationError(t *testing.T) {
	g := NewWithT(t)

	validationFailure := errors.New("validation failure")

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil),
		calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil),
		failingValidator(validationFailure))

	result, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

	// A check that fails to evaluate is an execution error, not a
	// finding: no partial result is reported as success.
	g.Expect(err).To(MatchError(validationFailure))
	g.Expect(result.Diagnostics).To(BeEmpty())
	g.Expect(calls.sequence).To(Equal([]string{"load", "render:helm"}))
}

func TestActionRunForwardsErrorCategories(t *testing.T) {
	t.Parallel()

	t.Run("definition error identity", func(t *testing.T) {
		g := NewWithT(t)

		definitionFailure := moduleerrors.NewDefinitionError(errors.New("bad spec"))

		calls := &actionCalls{}
		action := NewAction(calls.loader(v1alpha1.PlatformModuleDefinition{}, definitionFailure),
			calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil),
			calls.validator())

		_, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

		g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	})

	t.Run("loader cancellation identity", func(t *testing.T) {
		g := NewWithT(t)

		calls := &actionCalls{}
		action := NewAction(calls.loader(v1alpha1.PlatformModuleDefinition{}, context.Canceled),
			calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil),
			calls.validator())

		_, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

		g.Expect(err).To(MatchError(context.Canceled))
	})
}
