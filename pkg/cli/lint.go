package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// LintRun runs the composed module lint action once. The concrete
// implementation is module.NewLinter().Run; command tests inject
// fakes. It is the only business boundary the command touches.
type LintRun func(ctx context.Context, options linter.Options) (linter.Result, error)

// ExitError carries the process exit status of one executed lint
// invocation, so main can exit with the classified status without
// re-inspecting reports or matching error strings. Its error identity
// is the underlying execution error, if any; findings-only failures
// carry no execution error.
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

// Classified execution failure categories of one lint invocation.
const (
	FailureCancelled      = "cancelled"
	FailureInvalidOptions = "invalid-options"
	FailureDefinition     = "definition"
	FailureRender         = "render"
)

// classify maps one lint execution error to its failure category and
// exit code. Cancellation and deadline always win, then the
// identifiable invalid-options and definition categories are usage
// failures, then any other execution error is a render/internal
// failure. Findings are never classified here: they are report
// content, and their exit code derives from Result.HasErrors.
func classify(err error) (string, int) {
	switch {
	case err == nil:
		return "", ExitSuccess
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return FailureCancelled, ExitFailure
	case errors.Is(err, linter.ErrInvalidOptions):
		return FailureInvalidOptions, ExitUsage
	case errors.Is(err, config.ErrInvalidDefinition):
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

// NewLintCommand returns the `module lint` command. It turns the
// required --config flag and the optional --output flag into a plain
// action Options struct, forwards the command context, calls the
// injected action exactly once, and formats one report.
func NewLintCommand(run LintRun) *cobra.Command {
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

// execute runs one lint invocation: call the action with the parsed
// options and command context, classify the outcome, write exactly
// one report to stdout, echo execution failures to stderr, and return
// the classified exit outcome for main.
func (o lintOptions) execute(command *cobra.Command, run LintRun) error {
	result, err := run(command.Context(), linter.Options{DefinitionFile: o.config})

	category, code := classify(err)

	var failure *Failure
	if err != nil {
		failure = &Failure{Category: category, Message: err.Error()}
	}

	report := newReport(o.config, result, failure)

	if writeErr := writeReport(command.OutOrStdout(), o.format.String(), report); writeErr != nil {
		return writeErr
	}

	// Execution failures are also summarized on stderr; findings are
	// report content only.
	if failure != nil {
		if _, err := fmt.Fprintf(command.ErrOrStderr(), "module lint %s: %s: %s\n",
			o.config, category, err); err != nil {
			return fmt.Errorf("write failure summary: %w", err)
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
