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
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/generator/values"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
	utilconfig "github.com/lburgazzoli/odh-platform-tools/pkg/util/config"
)

// publishFunc installs the encoded document at the destination. The
// production implementation stages and links; tests may substitute.
type publishFunc func(
	ctx context.Context,
	dest string,
	data []byte,
) error

// Action renders, infers, validates, and emits module definitions. It
// holds no per-call state; concurrent use requires concurrency-safe
// dependencies.
type Action struct {
	render   linter.ManifestRender
	validate linter.Validation
	publish  publishFunc
}

// NewAction supplies the rendering and validation function
// boundaries, following linter.NewAction.
func NewAction(
	render linter.ManifestRender,
	validate linter.Validation,
) *Action {
	return &Action{
		render:   render,
		validate: validate,
		publish:  publishFile,
	}
}

// Result is the outcome of one successful generation run.
type Result struct {
	// YAML is the complete generated document.
	YAML []byte

	// Output is outputStdout or the absolute destination path. A file
	// result means the document is already published.
	Output string
}

// Run infers a module definition from the local source and either
// returns its YAML for stdout or publishes it create-only. A
// successful file result is final: cancellation after the commit
// point does not turn it into a failure.
func (a *Action) Run(ctx context.Context, options Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if err := options.validate(); err != nil {
		return Result{}, err
	}

	sourceDir, err := resolveSourceDir(options.SourceDir)
	if err != nil {
		return Result{}, err
	}

	format, err := detectBackend(sourceDir)
	if err != nil {
		return Result{}, err
	}

	if err := rejectKustomizeValues(format, options); err != nil {
		return Result{}, err
	}

	if options.Name != "" {
		if err := validateExplicitName(options.Name, format); err != nil {
			return Result{}, err
		}
	}

	if options.InstanceName != "" {
		if err := validateInstanceName(options.InstanceName); err != nil {
			return Result{}, err
		}
	}

	output, err := resolveOutput(options.Output)
	if err != nil {
		return Result{}, err
	}

	definitionDir, err := output.definitionDir()
	if err != nil {
		return Result{}, moduleerrors.New(
			moduleerrors.ErrGenerateOutput,
			"resolve the generated definition directory",
			moduleerrors.WithCause(err),
		)
	}

	if output.file {
		if err := ensureDestinationParent(output.path); err != nil {
			return Result{}, err
		}

		if format == toolApi.FormatHelm {
			if err := guardChartDestination(sourceDir, output.path); err != nil {
				return Result{}, err
			}
		}
	}

	helmValues, err := values.LoadFromPath(options.ValuesFile, options.Values)
	if err != nil {
		return Result{}, err
	}

	relatedImages, err := loadRelatedImageNames(options.RelatedImagesFile)
	if err != nil {
		return Result{}, err
	}

	discovery, err := a.bootstrap(ctx, sourceDir, format, helmValues, options.Name)
	if err != nil {
		if options.Name == "" {
			err = fmt.Errorf("generation bootstrap failed, an explicit name might help: %w", err)
		}

		return Result{}, err
	}

	name := options.Name
	if name == "" {
		name = inferredName(discovery.moduleType.Kind)

		if err := validateInferredName(name, format); err != nil {
			return Result{}, err
		}
	}

	// The instance name defaults to the module name, mirroring the
	// loader's default for the published document.
	instanceName := options.InstanceName
	if instanceName == "" {
		instanceName = name
	}

	resources, deployment, err := a.renderFinal(ctx, sourceDir, format, helmValues, name, discovery)
	if err != nil {
		return Result{}, err
	}

	selection, err := discoverController(deployment, options.InitContainer)
	if err != nil {
		return Result{}, err
	}

	manifestsPath := manifestPath(sourceDir, definitionDir)

	definition := effectiveDefinition(
		name, instanceName, discovery.moduleType, format, manifestsPath, helmValues, relatedImages, selection)

	if err := renderer.CheckReserved(definition, resources); err != nil {
		return Result{}, err
	}

	diagnostics, err := a.validate(definition, resources)
	if err != nil {
		return Result{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if findings := errorFindings(diagnostics); len(findings) > 0 {
		return Result{}, moduleerrors.New(
			moduleerrors.ErrGenerateValidation,
			"generated definition failed validation: "+findings,
		)
	}

	document, err := encodeDocument(minimalDocument(
		name, instanceName, discovery.moduleType, format, manifestsPath, helmValues, relatedImages, selection))
	if err != nil {
		return Result{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if !output.file {
		return Result{YAML: document, Output: outputStdout}, nil
	}

	if err := a.publish(ctx, output.path, document); err != nil {
		return Result{}, err
	}

	return Result{YAML: document, Output: output.path}, nil
}

// bootstrap renders the discovery pass with the explicit or temporary
// release name and infers the module identity from it.
func (a *Action) bootstrap(
	ctx context.Context,
	sourceDir string,
	format toolApi.Format,
	helmValues map[string]apiextensionsv1.JSON,
	name string,
) (moduleDiscovery, error) {
	bootstrapName := name
	if bootstrapName == "" {
		bootstrapName = bootstrapReleaseName
	}

	definition := renderDefinition(bootstrapName, toolApi.ModuleType{}, format, helmValues)

	resources, err := a.render(ctx, definition, filepath.Join(sourceDir, syntheticDefinitionName))
	if err != nil {
		return moduleDiscovery{}, renderFailure("bootstrap", sourceDir, err)
	}

	if err := ctx.Err(); err != nil {
		return moduleDiscovery{}, err
	}

	return discoverModule(resources)
}

// renderFinal renders with the final name and discovered type,
// rechecks cardinalities and CRD identity, and returns the final
// resources with their decoded controller Deployment.
func (a *Action) renderFinal(
	ctx context.Context,
	sourceDir string,
	format toolApi.Format,
	helmValues map[string]apiextensionsv1.JSON,
	name string,
	discovery moduleDiscovery,
) ([]unstructured.Unstructured, *appsv1.Deployment, error) {
	definition := renderDefinition(name, discovery.moduleType, format, helmValues)

	resources, err := a.render(ctx, definition, filepath.Join(sourceDir, syntheticDefinitionName))
	if err != nil {
		return nil, nil, renderFailure("final", sourceDir, err)
	}

	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	deployment, err := confirmModule(resources, discovery)
	if err != nil {
		return nil, nil, err
	}

	return resources, deployment, nil
}

// renderFailure rewraps a native render error with a sanitized
// stage-and-path message: native renderer messages may quote chart
// templates or supplied values, so the raw cause stays wrapped for
// category and cancellation identity without its text reaching
// wrapper diagnostics. Errors that already carry a public module
// message, such as reserved-resource findings, pass through
// unchanged.
func renderFailure(stage string, sourceDir string, err error) error {
	var moduleErr *moduleerrors.ModuleError
	if errors.As(err, &moduleErr) {
		return err
	}

	return moduleerrors.New(
		moduleerrors.ErrGenerateRender,
		"the "+stage+" render of "+sourceDir+" failed; the raw renderer "+
			"error is wrapped, not printed, because it may quote chart or "+
			"values content",
		moduleerrors.WithCause(err),
	)
}

// rejectKustomizeValues fails Helm-only value options on Kustomize
// sources before any render.
func rejectKustomizeValues(format toolApi.Format, options Options) error {
	if format != toolApi.FormatKustomize {
		return nil
	}

	if options.ValuesFile != "" || len(options.Values) > 0 {
		return moduleerrors.New(
			moduleerrors.ErrInvalidGenerateOptions,
			"Helm values options require a Helm chart source",
		)
	}

	return nil
}

// loadRelatedImageNames copies the related-image names through the
// existing permissive decoder, sorted and deduplicated. Values are
// never resolved against the environment.
func loadRelatedImageNames(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}

	var config api.RelatedImagesConfig
	if err := utilconfig.LoadFromPath(path, &config); err != nil {
		return nil, moduleerrors.New(
			moduleerrors.ErrImageSource,
			"read related images file "+path,
			moduleerrors.WithCause(err),
		)
	}

	names := make([]string, 0, len(config.RelatedImages))
	for _, image := range config.RelatedImages {
		names = append(names, image.Name)
	}

	slices.Sort(names)

	return slices.Compact(names), nil
}

// errorFindings joins the error-severity diagnostic strings.
func errorFindings(diagnostics []linter.Diagnostic) string {
	messages := make([]string, 0, len(diagnostics))

	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == linter.SeverityError {
			messages = append(messages, diagnostic.String())
		}
	}

	return strings.Join(messages, "; ")
}
