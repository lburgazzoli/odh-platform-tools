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

// DefinitionLoad loads and defaults a definition.
type DefinitionLoad func(
	ctx context.Context,
	path string,
	opts ...loader.LoadOption,
) (toolApi.PlatformModuleDefinition, error)

// ManifestRender renders a loaded definition.
type ManifestRender func(
	ctx context.Context,
	definition toolApi.PlatformModuleDefinition,
	definitionFile string,
	opts ...renderer.RenderOption,
) ([]unstructured.Unstructured, error)

// Action loads, renders, and validates a module. It holds no per-call state;
// concurrent use requires concurrency-safe dependencies.
type Action struct {
	load     DefinitionLoad
	render   ManifestRender
	validate Validation
}

// NewAction supplies the loader, renderer, and validation function.
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

// Run returns manifest defects as diagnostics with nil error; HasErrors
// indicates failure. Execution errors retain their original categories.
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

	slices.SortStableFunc(diagnostics, func(x Diagnostic, y Diagnostic) int {
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
