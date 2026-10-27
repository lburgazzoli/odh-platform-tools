package linter

import (
	"cmp"
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// DefinitionLoad loads and normalizes one module definition file. The
// loader is the concrete implementation; its options flow through
// untouched.
type DefinitionLoad func(
	ctx context.Context,
	path string,
	opts ...loader.LoadOption,
) (toolApi.PlatformModuleDefinition, error)

// ManifestRender renders the manifest backend selected by a loaded
// definition and returns the rendered objects. The centralized
// rendering service is the concrete implementation.
type ManifestRender func(
	ctx context.Context,
	definition toolApi.PlatformModuleDefinition,
	definitionFile string,
	opts ...renderer.RenderOption,
) ([]unstructured.Unstructured, error)

// Action composes loading, backend rendering, and validation into one
// lint invocation: it loads the definition, renders its manifest
// backend, runs every registered validator over the rendered objects,
// and reports the aggregated findings. It is Cobra-free, performs no
// environment resolution or injection, never prints, and holds no
// per-call state: the same action is safe for concurrent runs.
type Action struct {
	load     DefinitionLoad
	render   ManifestRender
	validate Validation
}

// NewAction wires the action with its service boundaries: the loader,
// the centralized renderer, and the validation function. The root
// module facade passes the concrete reusable services and the default
// checks; tests inject fakes.
func NewAction(
	load DefinitionLoad,
	render ManifestRender,
	validate Validation,
) *Action {
	return &Action{
		load:     load,
		render:   render,
		validate: validate,
	}
}

// Run lints the module defined by options.DefinitionFile:
// validate options, load, render through the centralized rendering
// service with no runtime options, then run the validation function
// over the returned objects.
//
// Execution errors follow the error-category contract: a cancelled
// context keeps its cancellation identity and takes precedence over
// invalid options; invalid options match
// moduleerrors.ErrInvalidLintOptions; loader, source, and renderer
// errors are forwarded without recategorizing
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

	resources, err := a.render(ctx, definition, options.DefinitionFile)
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
