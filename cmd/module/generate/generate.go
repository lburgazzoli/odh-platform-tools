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

// Package generate provides the module generate command.
package generate

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/generator"
)

// Process exit statuses.
const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// stdoutOutput is the normalized Result.Output of stream results.
const stdoutOutput = "stdout"

// Run is the command's generation action.
type Run func(ctx context.Context, options generator.Options) (generator.Result, error)

// ExitError carries the exit code and execution error.
type ExitError struct {
	Code int
	Err  error
}

// Error renders the classified outcome; the authoritative YAML has
// already been written or published.
func (e *ExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}

	return fmt.Sprintf("module generate: exit status %d", e.Code)
}

// Unwrap preserves the underlying execution error identity.
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode implements the root command's exit contract.
func (e *ExitError) ExitCode() int { return e.Code }

// Classified execution failure categories of one generate invocation.
const (
	FailureCancelled      = "cancelled"
	FailureInvalidOptions = "invalid-options"
	FailureDefinition     = "definition"
	FailureImageFile      = "image-file"
	FailureReserved       = "reserved"
	FailureValidation     = "validation"
	FailureOutput         = "output"
	FailureRender         = "render"
)

// Cancellation identity takes precedence over input categories.
func classify(err error) (string, int) {
	switch {
	case err == nil:
		return "", ExitSuccess
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return FailureCancelled, ExitFailure
	case errors.Is(err, moduleerrors.ErrInvalidGenerateOptions):
		return FailureInvalidOptions, ExitUsage
	case errors.Is(err, moduleerrors.ErrInvalidDefinition):
		return FailureDefinition, ExitUsage
	case errors.Is(err, moduleerrors.ErrImageSource):
		return FailureImageFile, ExitUsage
	case errors.Is(err, moduleerrors.ErrReservedResource):
		return FailureReserved, ExitFailure
	case errors.Is(err, moduleerrors.ErrGenerateValidation):
		return FailureValidation, ExitFailure
	case errors.Is(err, moduleerrors.ErrGenerateOutput):
		return FailureOutput, ExitFailure
	case errors.Is(err, moduleerrors.ErrGenerateRender):
		return FailureRender, ExitFailure
	default:
		return FailureRender, ExitFailure
	}
}

// generateOptions holds the parsed `module generate` flag values of
// one execution.
type generateOptions struct {
	name              nonEmptyValue
	initContainer     nonEmptyValue
	relatedImagesFile string
	valuesFile        singleValue
	values            []string
	output            nonEmptyValue
}

// NewGenerateCommand binds flags to the supplied action and writes
// its result.
func NewGenerateCommand(run Run) *cobra.Command {
	options := generateOptions{}

	command := &cobra.Command{
		Use:   "generate SOURCE_DIR",
		Short: "Infer one module definition from local manifests",
		Long: "Renders the local Helm chart or Kustomize tree at " +
			"SOURCE_DIR offline and infers a minimal " +
			"PlatformModuleDefinition from the rendered objects. " +
			"Discovery is strict: the source must render exactly one " +
			"CustomResourceDefinition with a single served storage " +
			"version and exactly one Deployment; the module name " +
			"defaults to the lowercase CRD kind. The controller main " +
			"container is the unique manager, or the only container; " +
			"an explicit --init-container is mandatory whenever the " +
			"Deployment has init containers. The generated definition " +
			"is validated with the same checks as module lint before " +
			"any output. Output defaults to stdout; a file path is " +
			"created once with 0600 permissions and never " +
			"overwritten. Exits 0 on success, 1 on render, " +
			"validation, output, or cancellation failures, and 2 on " +
			"usage, definition, or image-file errors.",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, args []string) error {
			return options.execute(command, run, args[0])
		},
	}

	flags := command.Flags()
	flags.Var(&options.name, "name",
		"module name; defaults to the lowercase CRD kind")
	flags.Var(&options.initContainer, "init-container",
		"controller init container name; required when the rendered Deployment has init containers")
	flags.StringVar(&options.relatedImagesFile, "related-images-file", "",
		"YAML file of related image sources; only the names are copied (no automatic discovery)")
	flags.Var(&options.valuesFile, "values-file",
		"Helm values file; a single nonempty occurrence (Helm sources only)")
	flags.StringArrayVar(&options.values, "values", nil,
		"ordered Helm set expression, each preserved verbatim including commas; repeatable (Helm sources only)")
	flags.Var(&options.output, "output",
		"destination: stdout (default) or a file path created once with 0600 permissions")

	return command
}

func (o generateOptions) execute(command *cobra.Command, run Run, source string) error {
	options := generator.Options{
		SourceDir:         source,
		Name:              o.name.value,
		InitContainer:     o.initContainer.value,
		RelatedImagesFile: o.relatedImagesFile,
		ValuesFile:        o.valuesFile.value,
		Values:            o.values,
		Output:            o.output.value,
	}

	result, err := run(command.Context(), options)
	if err != nil {
		category, code := classify(err)

		if _, writeErr := fmt.Fprintf(command.ErrOrStderr(), "module generate %s: %s: %s\n",
			source, category, err); writeErr != nil {
			return &ExitError{
				Code: ExitFailure,
				Err:  errors.Join(err, fmt.Errorf("write failure summary: %w", writeErr)),
			}
		}

		return &ExitError{Code: code, Err: err}
	}

	// File results are already published; only stdout results write
	// here, and a file success produces no stdout.
	if result.Output == stdoutOutput {
		// Cancellation between the service return and the first
		// stdout write still prevents output.
		if err := command.Context().Err(); err != nil {
			return &ExitError{Code: ExitFailure, Err: err}
		}

		written, err := command.OutOrStdout().Write(result.YAML)
		if err != nil {
			return &ExitError{
				Code: ExitFailure,
				Err:  fmt.Errorf("write generated definition: %w", err),
			}
		}

		if written != len(result.YAML) {
			return &ExitError{
				Code: ExitFailure,
				Err: fmt.Errorf("write generated definition: short write of %d of %d bytes",
					written, len(result.YAML)),
			}
		}
	}

	return nil
}
