// Package deploy provides the module deploy command and reports.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	deployer "github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/cluster"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// Process exit statuses.
const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// Default flag values: the overall invocation budget and the shared
// rollout-wait budget.
const (
	DefaultTimeout     = 5 * time.Minute
	DefaultWaitTimeout = 30 * time.Second
)

// Deploy is the command's deployment action.
type Deploy func(
	ctx context.Context,
	getter genericclioptions.RESTClientGetter,
	options deployer.Options,
) (deployer.Result, error)

// Action validates settings, connects using the standard getter, and runs a
// fresh deployer. The timeout includes connection setup.
func Action(
	ctx context.Context,
	getter genericclioptions.RESTClientGetter,
	options deployer.Options,
) (deployer.Result, error) {
	if err := ctx.Err(); err != nil {
		return deployer.Result{}, err
	}

	if err := options.ValidateSettings(); err != nil {
		return deployer.Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()

	connection, err := cluster.Connect(ctx, getter)
	if err != nil {
		return deployer.Result{}, err
	}

	return deployer.New(
		options,
		deployer.WithClient(connection.Client()),
	).Apply(ctx)
}

// ExitError carries a process exit code and preserves the execution error.
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

	return fmt.Sprintf("module deploy: exit status %d", e.Code)
}

// Unwrap preserves the underlying execution error identity.
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode reports the classified process exit status.
func (e *ExitError) ExitCode() int { return e.Code }

// Classified execution failure categories of one deploy invocation.
const (
	FailureCancelled      = "cancelled"
	FailureInvalidOptions = "invalid-options"
	FailureDefinition     = "definition"
	FailureImageFile      = "image-file"
	FailureRuntime        = "runtime"
)

// Cancellation takes precedence over usage and runtime failure categories.
func classify(err error) (string, int) {
	switch {
	case err == nil:
		return "", ExitSuccess
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return FailureCancelled, ExitFailure
	case errors.Is(err, moduleerrors.ErrInvalidDeployOptions):
		return FailureInvalidOptions, ExitUsage
	case errors.Is(err, moduleerrors.ErrInvalidDefinition):
		return FailureDefinition, ExitUsage
	case errors.Is(err, moduleerrors.ErrImageSource):
		return FailureImageFile, ExitUsage
	default:
		return FailureRuntime, ExitFailure
	}
}

// deployOptions holds the parsed `module deploy` flag values of one
// execution.
type deployOptions struct {
	config              string
	platformVersion     string
	relatedImagesFile   string
	monitoringNamespace string
	platformType        string
	fieldManager        string
	timeout             time.Duration
	wait                bool
	waitTimeout         time.Duration
	format              outputFormat
}

// NewDeployCommand binds flags to the supplied action and formats its result.
func NewDeployCommand(action Deploy, getter genericclioptions.RESTClientGetter) *cobra.Command {
	options := deployOptions{
		format:      FormatText,
		timeout:     DefaultTimeout,
		waitTimeout: DefaultWaitTimeout,
	}

	command := &cobra.Command{
		Use:   "deploy",
		Short: "Install one module definition and its controller package",
		Long: "Loads one PlatformModuleDefinition, renders its selected " +
			"manifest backend, publishes the definition owner, and applies " +
			"the rendered controller package through the framework deploy " +
			"action into the namespace resolved from the standard " +
			"--namespace flag with kubectl semantics: the explicit flag, " +
			"else the kubeconfig context's namespace, else default. " +
			"Exits 0 on success, 1 on runtime, connection, apply, wait, " +
			"timeout, or cancellation failures, and 2 on usage, local " +
			"definition, or image-file errors. The standard " +
			"kubeconfig/context/namespace/auth flags are inherited from the " +
			"root command.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			return options.execute(command, action, getter)
		},
	}

	flags := command.Flags()
	flags.StringVar(&options.config, "config", "",
		"module definition file (required)")
	flags.StringVar(&options.platformVersion, "platform-version", "",
		"platform version, parsed once and shared by the platform config and deploy (required)")
	flags.StringVar(&options.relatedImagesFile, "related-images-file", "",
		"dotenv file of related image sources; no file is discovered automatically")
	flags.StringVar(&options.monitoringNamespace, "monitoring-namespace", "",
		"monitoring namespace injected as MONITORING_NAMESPACE")
	flags.StringVar(&options.platformType, "platform-type", "",
		"platform identity injected as ODH_MODULE_OPERATOR_PLATFORM_TYPE (default: OpenDataHub)")
	flags.StringVar(&options.fieldManager, "field-manager", deployer.DefaultFieldManager,
		"server-side-apply field manager for the definition and ordinary payload writes; a nonempty Kubernetes label value of at most 63 characters")
	flags.DurationVar(&options.timeout, "timeout", DefaultTimeout,
		"overall invocation budget; a longer --wait-timeout is capped by the remaining overall time (first deadline wins)")
	flags.BoolVar(&options.wait, "wait", false,
		"wait for the module controller Deployment rollout after the apply")
	flags.DurationVar(&options.waitTimeout, "wait-timeout", DefaultWaitTimeout,
		"shared rollout wait budget when --wait is enabled; positive, and never extending the remaining --timeout")
	flags.Var(&options.format, "output",
		"report format: text or json (default text)")

	for _, required := range []string{"config", "platform-version"} {
		if err := command.MarkFlagRequired(required); err != nil {
			// Registration-time failure only: the flag was just added.
			panic(err)
		}
	}

	return command
}

func (o deployOptions) execute(
	command *cobra.Command,
	action Deploy,
	getter genericclioptions.RESTClientGetter,
) error {
	var result deployer.Result

	var err error

	// An empty flag is explicit invalid input; the Go API treats empty as default.
	if o.fieldManager == "" {
		err = moduleerrors.New(
			moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: FieldManager must not be empty",
		)
	} else {
		var namespace string

		namespace, err = cluster.ResolveNamespace(getter)
		if err == nil {
			result, err = action(command.Context(), getter, deployer.Options{
				DefinitionFile:      o.config,
				RelatedImagesFile:   o.relatedImagesFile,
				Namespace:           namespace,
				MonitoringNamespace: o.monitoringNamespace,
				PlatformType:        o.platformType,
				PlatformVersion:     o.platformVersion,
				FieldManager:        o.fieldManager,
				Timeout:             o.timeout,
				Wait:                o.wait,
				WaitTimeout:         o.waitTimeout,
			})
		}
	}

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

	// Execution failures are also summarized on stderr.
	if failure != nil {
		if _, writeErr := fmt.Fprintf(command.ErrOrStderr(), "module deploy %s: %s: %s\n",
			o.config, category, err); writeErr != nil {
			return &ExitError{
				Code: ExitFailure,
				Err:  errors.Join(err, fmt.Errorf("write failure summary: %w", writeErr)),
			}
		}
	}

	if code == ExitSuccess {
		return nil
	}

	return &ExitError{Code: code, Err: err}
}
