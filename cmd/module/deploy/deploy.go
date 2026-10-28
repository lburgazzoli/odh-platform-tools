// Package deploy adapts the staged module deploy action to the
// `module deploy` command surface. It owns flag parsing, report
// formatting, and exit-code classification only: no loading,
// rendering, connecting, or applying logic lives here, and every
// business execution goes through the injected action boundary.
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

// Deploy runs one composed module deploy invocation. The concrete
// implementation is Action; command tests inject fakes. It is the
// only business boundary the command touches: it receives the
// standard getter the root registered, so namespace and REST
// resolution share one configuration source.
type Deploy func(
	ctx context.Context,
	getter genericclioptions.RESTClientGetter,
	options deployer.Options,
) (deployer.Result, error)

// Action is the concrete Deploy boundary: it validates the
// invocation settings, derives the invocation context from the caller's
// context and the options' timeout, builds the standard cluster
// connection from the getter — so REST and authentication inputs
// share one configuration source — and drives one fresh staged
// Deployer over the connected client through its convenience Apply.
// The settings check precedes the client, the invocation timer, and
// every stage, so an invalid setting is a usage failure before any
// client construction; the client itself cannot be checked before the
// connection that supplies it, so its requirement is enforced by the
// deployer's first stage once construction has injected it, and a
// broken configuration surfaces as a runtime failure. The deployer
// holds no state across invocations.
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

// ExitError carries the process exit status of one executed deploy
// invocation, so main can exit with the classified status without
// re-inspecting reports or matching error strings. Its error identity
// is the underlying execution error, if any. ExitCode implements the
// root's command-neutral exit contract.
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

// classify maps one deploy execution error to its failure category and
// exit code. Cancellation and deadline always win; usage, local
// definition, and image-file failures are usage failures; every
// other execution error — render, connection, owner preparation, apply,
// and wait failures included — is a runtime failure.
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

// NewDeployCommand returns the `module deploy` command. It turns the
// required --config and --platform-version flags and the runtime,
// image, wait, and output flags into a plain action Options struct,
// forwards the command context and the standard getter, calls the
// injected action exactly once, and formats one report. The standard
// kubeconfig/context/namespace/auth flags come from the root's
// persistent ConfigFlags, never duplicated here.
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

// execute runs one module deploy invocation: resolve the effective
// namespace from the standard getter — kubectl semantics: the explicit
// --namespace flag, else the kubeconfig context's namespace, else
// default — call the action with the parsed options, the command
// context, and the same getter, classify the outcome, write exactly
// one report to stdout, echo execution failures to stderr, and return
// the classified exit outcome for main.
func (o deployOptions) execute(
	command *cobra.Command,
	action Deploy,
	getter genericclioptions.RESTClientGetter,
) error {
	var result deployer.Result

	var err error

	// The flag's default is the canonical manager, so an empty value
	// can only be explicit input. It is rejected here, before the
	// injected action runs, through the same classification and
	// report path as every other options failure: the Go option
	// itself treats empty as the default request.
	if o.fieldManager == "" {
		err = moduleerrors.New(
			moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: FieldManager must not be empty",
		)
	} else {
		var namespace string

		// The namespace resolution is a local configuration read
		// through the same getter the connection uses; its failure
		// is a runtime one, classified with every other execution
		// error below.
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

	// A report or summary write failure is an internal execution
	// failure (exit 1), never a usage error, and keeps the action's
	// outcome identity alongside it.
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
