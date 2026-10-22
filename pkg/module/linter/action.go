package linter

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// DefinitionLoad loads and validates one module definition file. The
// strict loader is the concrete implementation.
type DefinitionLoad func(ctx context.Context, path string) (config.PlatformModuleDefinition, error)

// ManifestRender renders the manifest backend selected by a loaded
// definition and returns the rendered objects. The Helm and Kustomize
// backend wrappers are the concrete implementations.
type ManifestRender func(ctx context.Context, definition config.PlatformModuleDefinition, definitionFile string) ([]unstructured.Unstructured, error)

// Action composes loading, backend rendering, and pure validation
// into one lint invocation. It is Cobra-free, performs no environment
// resolution or injection, never prints, and holds no per-call state:
// the same action is safe for concurrent runs.
type Action struct {
	load            DefinitionLoad
	renderHelm      ManifestRender
	renderKustomize ManifestRender
}

// NewAction wires the action with its service boundaries. All three
// services are required; the root module facade passes the concrete
// reusable loader and backend wrappers, and tests inject fakes.
func NewAction(
	load DefinitionLoad,
	renderHelm ManifestRender,
	renderKustomize ManifestRender,
) *Action {
	return &Action{
		load:            load,
		renderHelm:      renderHelm,
		renderKustomize: renderKustomize,
	}
}

// Run lints the module defined by options.DefinitionFile:
// validate options, load, select the backend from the loaded
// definition's manifest format, render, then run pure validation over
// the returned objects.
//
// Execution errors follow the error-category contract: a cancelled
// context keeps its cancellation identity and takes precedence over
// invalid options; invalid options match ErrInvalidOptions; loader,
// source, and renderer errors are forwarded without recategorizing
// (a failed phase never yields a partial object set as success).
// Manifest defects are not execution errors: they are returned as
// findings in the Result with a nil error, and Result.HasErrors
// derives the failure.
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

	return Validate(ctx, definition, resources)
}

// backendFor selects the rendering service for the loaded definition's
// manifest format. The strict loader already restricts the format, so
// an unexpected value is a defensive internal error, not a user
// category.
func (a *Action) backendFor(definition config.PlatformModuleDefinition) (ManifestRender, error) {
	switch definition.Spec.Manifests.Format {
	case config.FormatHelm:
		return a.renderHelm, nil
	case config.FormatKustomize:
		return a.renderKustomize, nil
	default:
		return nil, fmt.Errorf("unsupported manifest format %q for module %s",
			definition.Spec.Manifests.Format,
			definition.Metadata.Name)
	}
}
