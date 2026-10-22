package linter

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// actionCalls records the service invocations of one action run.
type actionCalls struct {
	sequence    []string
	contexts    []context.Context
	paths       []string
	definitions []config.PlatformModuleDefinition
}

// loader returns a fake definition loader returning definition and
// err.
func (r *actionCalls) loader(definition config.PlatformModuleDefinition, err error) DefinitionLoad {
	return func(ctx context.Context, path string) (config.PlatformModuleDefinition, error) {
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
	return func(ctx context.Context, definition config.PlatformModuleDefinition, definitionFile string) ([]unstructured.Unstructured, error) {
		r.sequence = append(r.sequence, "render:"+backend)
		r.contexts = append(r.contexts, ctx)
		r.definitions = append(r.definitions, definition)
		r.paths = append(r.paths, definitionFile)

		return resources, err
	}
}

// helmDefinition returns a loaded definition selecting the Helm
// backend.
func helmDefinition() config.PlatformModuleDefinition {
	definition := testDefinition()
	definition.Spec.Manifests.Format = config.FormatHelm

	return definition
}

// kustomizeDefinition returns a loaded definition selecting the
// Kustomize backend.
func kustomizeDefinition() config.PlatformModuleDefinition {
	definition := testDefinition()
	definition.Spec.Manifests.Format = config.FormatKustomize

	return definition
}

func TestActionRunValidatesOptions(t *testing.T) {
	g := NewWithT(t)

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil), calls.renderer("helm", nil, nil),
		calls.renderer("kustomize", nil, nil))

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
		calls.renderer("kustomize", nil, nil))

	_, err := action.Run(ctx, Options{})

	g.Expect(err).To(MatchError(context.Canceled))
}

func TestActionRunStopsOnLoadError(t *testing.T) {
	g := NewWithT(t)

	loadFailure := errors.New("load failure")

	calls := &actionCalls{}
	action := NewAction(calls.loader(config.PlatformModuleDefinition{}, loadFailure),
		calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil))

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
		calls.renderer("helm", nil, renderFailure), calls.renderer("kustomize", nil, nil))

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
			calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil))

		_, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(calls.sequence).To(Equal([]string{"load", "render:helm"}))
	})

	t.Run("kustomize", func(t *testing.T) {
		g := NewWithT(t)

		calls := &actionCalls{}
		action := NewAction(calls.loader(kustomizeDefinition(), nil),
			calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil))

		_, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(calls.sequence).To(Equal([]string{"load", "render:kustomize"}))
	})

	t.Run("unexpected format is a defensive error", func(t *testing.T) {
		g := NewWithT(t)

		definition := testDefinition()
		definition.Spec.Manifests.Format = config.Format("bogus")

		calls := &actionCalls{}
		action := NewAction(calls.loader(definition, nil),
			calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil))

		result, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("bogus"))
		g.Expect(result.Diagnostics).To(BeEmpty())
		g.Expect(calls.sequence).To(ConsistOf("load"))
	})
}

func TestActionRunPropagatesContextAndInputs(t *testing.T) {
	g := NewWithT(t)

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil),
		calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil))

	ctx := t.Context()
	_, err := action.Run(ctx, Options{DefinitionFile: "some/definition.yaml"})
	g.Expect(err).NotTo(HaveOccurred())

	// The caller's context and explicit file reach every service.
	g.Expect(calls.contexts).To(HaveLen(2))
	g.Expect(calls.contexts[0]).To(Equal(ctx))
	g.Expect(calls.contexts[1]).To(Equal(ctx))
	g.Expect(calls.paths).To(Equal([]string{"some/definition.yaml", "some/definition.yaml"}))

	// The renderer receives the loaded definition unchanged, and the
	// definition file exactly as the caller passed it.
	g.Expect(calls.definitions).To(HaveLen(1))
	g.Expect(calls.definitions[0]).To(Equal(helmDefinition()))
	g.Expect(calls.paths).To(Equal([]string{"some/definition.yaml", "some/definition.yaml"}))
}

func TestActionRunReturnsFindingsAsResults(t *testing.T) {
	g := NewWithT(t)

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil),
		calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil))

	result, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

	// Manifest defects are structured findings, not execution errors.
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.HasErrors()).To(BeTrue())
	g.Expect(diagnosticIDs(result)).To(ConsistOf(
		CheckDeploymentMissing,
		CheckModuleCRDMissing,
		CheckSingletonEvidenceMissing,
	))
}

func TestActionRunWarningOnlyResultSucceeds(t *testing.T) {
	g := NewWithT(t)

	resources := append(singletonWebhook(nil), []unstructured.Unstructured{
		controllerDeployment(testDeployment, testContainer),
		serviceAccountObject(testNamespace),
		moduleCRDObject(t, crdWithRootRule("self.spec.replicas >= 0")),
	}...)

	calls := &actionCalls{}
	action := NewAction(calls.loader(helmDefinition(), nil),
		calls.renderer("helm", resources, nil), calls.renderer("kustomize", nil, nil))

	result, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.HasErrors()).To(BeFalse())
	g.Expect(result.HasWarnings()).To(BeTrue())
	g.Expect(diagnosticIDs(result)).To(ConsistOf(CheckSingletonWebhookUnverified))
}

func TestActionRunForwardsErrorCategories(t *testing.T) {
	t.Parallel()

	t.Run("definition error identity", func(t *testing.T) {
		g := NewWithT(t)

		definitionFailure := config.NewDefinitionError(errors.New("bad spec"))

		calls := &actionCalls{}
		action := NewAction(calls.loader(config.PlatformModuleDefinition{}, definitionFailure),
			calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil))

		_, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

		g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeTrue())
	})

	t.Run("loader cancellation identity", func(t *testing.T) {
		g := NewWithT(t)

		calls := &actionCalls{}
		action := NewAction(calls.loader(config.PlatformModuleDefinition{}, context.Canceled),
			calls.renderer("helm", nil, nil), calls.renderer("kustomize", nil, nil))

		_, err := action.Run(t.Context(), Options{DefinitionFile: "definition.yaml"})

		g.Expect(err).To(MatchError(context.Canceled))
	})
}
