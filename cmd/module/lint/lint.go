// Package lint provides the module lint command and reports.
package lint

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// Process exit statuses.
const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// Run is the command's lint action.
type Run func(ctx context.Context, options linter.Options) (linter.Result, error)

// ExitError carries the exit code and optional execution error.
// Findings can fail lint without an execution error.
type ExitError struct {
	Code int
	Err  error
}

// Error renders the classified outcome; the authoritative report has
// already been written to stdout.
func (e *ExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}

	return fmt.Sprintf("module lint: %d error-severity finding(s)", e.Code)
}

// Unwrap preserves the underlying execution error identity.
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode implements the root command's exit contract.
func (e *ExitError) ExitCode() int { return e.Code }

// Classified execution failure categories of one lint invocation.
const (
	FailureCancelled      = "cancelled"
	FailureInvalidOptions = "invalid-options"
	FailureDefinition     = "definition"
	FailureRender         = "render"
)

// classify handles execution errors; findings use Result.HasErrors.
func classify(err error) (string, int) {
	switch {
	case err == nil:
		return "", ExitSuccess
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return FailureCancelled, ExitFailure
	case errors.Is(err, moduleerrors.ErrInvalidLintOptions):
		return FailureInvalidOptions, ExitUsage
	case errors.Is(err, moduleerrors.ErrInvalidDefinition):
		return FailureDefinition, ExitUsage
	default:
		return FailureRender, ExitFailure
	}
}

// lintOptions holds the parsed `module lint` flag values of one
// execution.
type lintOptions struct {
	config string
	format outputFormat
}

// NewLintCommand binds flags to the supplied action and formats its result.
func NewLintCommand(run Run) *cobra.Command {
	options := lintOptions{format: FormatText}

	command := &cobra.Command{
		Use:   "lint",
		Short: "Lint one module definition and its rendered manifests",
		Long: "Loads one PlatformModuleDefinition, renders its selected " +
			"manifest backend offline, and validates the rendered " +
			"objects. Exits 0 on success, 1 on findings, cancellation, " +
			"or execution failure, and 2 on usage or definition errors.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			return options.execute(command, run)
		},
	}

	command.Flags().StringVar(&options.config, "config", "",
		"module definition file (required)")
	command.Flags().Var(&options.format, "output",
		"report format: text or json (default text)")

	if err := command.MarkFlagRequired("config"); err != nil {
		// Registration-time failure only: the flag was just added.
		panic(err)
	}

	return command
}

func (o lintOptions) execute(command *cobra.Command, run Run) error {
	result, err := run(command.Context(), linter.Options{DefinitionFile: o.config})

	category, code := classify(err)

	var failure *Failure
	if err != nil {
		failure = &Failure{Category: category, Message: err.Error()}
	}

	report := newReport(o.config, result, failure)

	// Preserve the action error if writing its report also fails.
	if writeErr := writeReport(command.OutOrStdout(), o.format.String(), report); writeErr != nil {
		return &ExitError{
			Code: ExitFailure,
			Err:  errors.Join(err, fmt.Errorf("write report: %w", writeErr)),
		}
	}

	// Execution failures are also summarized on stderr; findings are
	// report content only.
	if failure != nil {
		if _, writeErr := fmt.Fprintf(command.ErrOrStderr(), "module lint %s: %s: %s\n",
			o.config, category, err); writeErr != nil {
			return &ExitError{
				Code: ExitFailure,
				Err:  errors.Join(err, fmt.Errorf("write failure summary: %w", writeErr)),
			}
		}
	}

	if code == ExitSuccess && result.HasErrors() {
		code = ExitFailure
	}

	if code == ExitSuccess {
		return nil
	}

	return &ExitError{Code: code, Err: err}
}
