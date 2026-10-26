package linter

import (
	"cmp"
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
)

// DefinitionLoad loads and normalizes one module definition file. The
// loader is the concrete implementation; its options flow through
// untouched.
type DefinitionLoad func(
	ctx context.Context,
	path string,
	opts ...loader.LoadOption,
) (v1alpha1.PlatformModuleDefinition, error)

// ManifestRender renders the manifest backend selected by a loaded
// definition and returns the rendered objects. The Helm and Kustomize
// backend wrappers are the concrete implementations.
type ManifestRender func(ctx context.Context, definition v1alpha1.PlatformModuleDefinition, definitionFile string) ([]unstructured.Unstructured, error)

// Action composes loading, backend rendering, and validation into one
// lint invocation: it loads the definition, renders its manifest
// backend, runs every registered validator over the rendered objects,
// and reports the aggregated findings. It is Cobra-free, performs no
// environment resolution or injection, never prints, and holds no
// per-call state: the same action is safe for concurrent runs.
type Action struct {
	load            DefinitionLoad
	renderHelm      ManifestRender
	renderKustomize ManifestRender
	validate        Validation
}

// NewAction wires the action with its service boundaries: the loader,
// both backend renderers, and the validation function. The root
// module facade passes the concrete reusable services and the default
// checks; tests inject fakes.
func NewAction(
	load DefinitionLoad,
	renderHelm ManifestRender,
	renderKustomize ManifestRender,
	validate Validation,
) *Action {
	return &Action{
		load:            load,
		renderHelm:      renderHelm,
		renderKustomize: renderKustomize,
		validate:        validate,
	}
}

// Run lints the module defined by options.DefinitionFile:
// validate options, load, select the backend from the loaded
// definition's manifest format, render, then run the validation
// function over the returned objects.
//
// Execution errors follow the error-category contract: a cancelled
// context keeps its cancellation identity and takes precedence over
// invalid options; invalid options match ErrInvalidOptions; loader,
// source, and renderer errors are forwarded without recategorizing
// (a failed phase never yields a partial object set as success).
// A validation function that fails to evaluate is an execution error
// like the others. Manifest defects are not execution errors: they
// are returned as findings in the Result with a nil error, and
// Result.HasErrors derives the failure.
func (a *Action) Run(ctx context.Context, options Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if err := options.validate(); err != nil {
		return Result{}, err
	}

	definition, err := a.load(ctx, options.DefinitionFile)
	if err != nil {
		return Result{}, err
	}

	render, err := a.backendFor(definition)
	if err != nil {
		return Result{}, err
	}

	resources, err := render(ctx, definition, options.DefinitionFile)
	if err != nil {
		return Result{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	diagnostics, err := a.validate(definition, resources)
	if err != nil {
		return Result{}, err
	}

	slices.SortStableFunc(diagnostics, func(x, y Diagnostic) int {
		return cmp.Or(
			cmp.Compare(x.ID, y.ID),
			cmp.Compare(x.GVK.String(), y.GVK.String()),
			cmp.Compare(x.Namespace, y.Namespace),
			cmp.Compare(x.Name, y.Name),
			cmp.Compare(x.Field, y.Field),
			cmp.Compare(x.Message, y.Message),
		)
	})

	return Result{Diagnostics: diagnostics}, nil
}

// backendFor selects the rendering service for the loaded definition's
// manifest format. Structural validation is deferred, so an
// unsupported value reaches this point as a definition failure rather
// than being caught at load time.
func (a *Action) backendFor(definition v1alpha1.PlatformModuleDefinition) (ManifestRender, error) {
	switch definition.Spec.Manifests.Format {
	case v1alpha1.FormatHelm:
		return a.renderHelm, nil
	case v1alpha1.FormatKustomize:
		return a.renderKustomize, nil
	default:
		return nil, moduleerrors.NewDefinitionErrorf("unsupported manifest format %q for module %s",
			definition.Spec.Manifests.Format,
			definition.Name)
	}
}
