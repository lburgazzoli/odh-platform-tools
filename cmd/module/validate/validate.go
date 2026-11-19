// Package validate provides the module validate command and reports.
package validate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/cluster"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/validator"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/exit"
)

// Process exit statuses.
const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// DefaultTimeout is the overall validation budget unless overridden.
const DefaultTimeout = validator.DefaultTimeout

// Run is the command's validation action.
type Run func(
	ctx context.Context,
	getter genericclioptions.RESTClientGetter,
	options validator.Options,
) (validator.Result, error)

// Action validates options, starts the overall deadline, resolves the
// namespace, loads the definition without rendering, validates selectors,
// composes context-aware standard Kubernetes clients, and runs the
// validator service. An earlier caller deadline shortens the budget; the
// inherited request timeout bounds each single request independently.
func Action(
	ctx context.Context,
	getter genericclioptions.RESTClientGetter,
	options validator.Options,
) (validator.Result, error) {
	if err := ctx.Err(); err != nil {
		return validator.Result{}, err
	}

	if err := options.Validate(); err != nil {
		return validator.Result{}, err
	}

	// One overall deadline governs namespace/file/config work, discovery,
	// every read, and polling. An earlier caller deadline wins.
	budget := options.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}

	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// Namespace resolution belongs inside the timed action: explicit
	// flag, else the kubeconfig context's namespace, else default.
	namespace := options.Namespace

	if namespace == "" {
		resolved, err := cluster.ResolveNamespace(getter)
		if err != nil {
			return validator.Result{}, moduleerrors.WrapStage("resolve the namespace", err)
		}

		namespace = resolved
	}

	// The resolved namespace is retained on every later failure.
	result := validator.Result{Namespace: namespace}

	if err := validator.ValidateNamespace(namespace); err != nil {
		return result, err
	}

	definition, err := loader.LoadFromPath(ctx, options.DefinitionFile)
	if err != nil {
		return result, moduleerrors.WrapStage("load the module definition", err)
	}

	// Local input is validated before any network access; rendering
	// sources and image tokens are not required.
	if err := validator.ValidateSelectors(validator.SelectorsFromDefinition(definition), namespace); err != nil {
		return result, err
	}

	restConfig, err := getter.ToRESTConfig()
	if err != nil {
		return result, moduleerrors.WrapStage("resolve the Kubernetes configuration", err)
	}

	// The caller's configured request timeout bounds each single request
	// independently and never extends the budget; every operation is also
	// context-bounded.
	config := rest.CopyConfig(restConfig)

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return result, moduleerrors.WrapStage("create the discovery client", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return result, moduleerrors.WrapStage("create the dynamic client", err)
	}

	service := validator.NewService(
		validator.NewDiscoveryFunc(discoveryClient),
		validator.NewObjectReader(dynamicClient),
	)

	validated, err := service.Validate(ctx, definition, namespace)
	if err != nil {
		return validated, moduleerrors.WrapStage("validate the running module", err)
	}

	return validated, nil
}

// Classified execution failure categories of one validate invocation.
const (
	FailureTimeout        = "timeout"
	FailureCancelled      = "cancelled"
	FailureInvalidOptions = "invalid-options"
	FailureDefinition     = "definition"
	FailureRuntime        = "runtime"
)

// Cancellation and deadline identity takes precedence over the option,
// definition, and runtime failure categories.
func classify(err error) (string, int) {
	switch {
	case err == nil:
		return "", ExitSuccess
	case errors.Is(err, context.DeadlineExceeded):
		return FailureTimeout, ExitFailure
	case errors.Is(err, context.Canceled):
		return FailureCancelled, ExitFailure
	case errors.Is(err, moduleerrors.ErrInvalidValidateOptions):
		return FailureInvalidOptions, ExitUsage
	case errors.Is(err, moduleerrors.ErrInvalidDefinition):
		return FailureDefinition, ExitUsage
	default:
		return FailureRuntime, ExitFailure
	}
}

// validateOptions holds the parsed `module validate` flag values of one
// execution.
type validateOptions struct {
	config  string
	timeout time.Duration
	format  outputFormat
}

// NewValidateCommand binds flags to the supplied action and formats its result.
func NewValidateCommand(action Run, getter genericclioptions.RESTClientGetter) *cobra.Command {
	options := validateOptions{
		format:  FormatText,
		timeout: DefaultTimeout,
	}

	command := &cobra.Command{
		Use:   "validate",
		Short: "Check a running module against the platform contract",
		Long: "Loads one PlatformModuleDefinition and validates the running " +
			"module it describes, read-only, in the namespace resolved from " +
			"the standard --namespace flag with kubectl semantics: the " +
			"explicit flag, else the kubeconfig context's namespace, else " +
			"default. It checks that the module custom resource API is " +
			"discoverable, cluster-scoped, and serves /status; that the " +
			"instance exists and its status conforms to the platform " +
			"contract and is ready; that the controller Deployment is " +
			"rolled out; and that the module consumed the platform version " +
			"from its odh-<module>-config ConfigMap. Waiting is automatic " +
			"under one overall --timeout budget; there is no wait toggle. " +
			"A matching platform release is read-only consistency " +
			"evidence, not proof that the module watches the ConfigMap. " +
			"Exits 0 only when every check passes, 1 for findings, " +
			"runtime, timeout, or cancellation failures, and 2 on usage " +
			"or definition input errors. The standard " +
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
	flags.DurationVar(&options.timeout, "timeout", DefaultTimeout,
		"overall validation budget, including connection, discovery, and waiting; positive, and shortened by any earlier caller deadline")
	flags.Var(&options.format, "output",
		"report format: text or json (default text)")

	if err := command.MarkFlagRequired("config"); err != nil {
		// Registration-time failure only: the flag was just added.
		panic(err)
	}

	return command
}

func (o validateOptions) execute(
	command *cobra.Command,
	action Run,
	getter genericclioptions.RESTClientGetter,
) error {
	var result validator.Result

	var err error

	// Flag-level input is validated before any action or cluster access.
	switch {
	case strings.TrimSpace(o.config) == "":
		err = moduleerrors.New(
			moduleerrors.ErrInvalidValidateOptions,
			"invalid validate options: --config must not be blank",
		)
	case o.timeout <= 0:
		err = moduleerrors.New(
			moduleerrors.ErrInvalidValidateOptions,
			fmt.Sprintf("invalid validate options: --timeout must be positive, got %s", o.timeout),
		)
	default:
		result, err = action(command.Context(), getter, validator.Options{
			DefinitionFile: o.config,
			Timeout:        o.timeout,
		})
	}

	category, code := classify(err)

	var failure *Failure
	if err != nil {
		failure = &Failure{Category: category, Message: err.Error()}
	}

	passed := err == nil && result.Passed()

	report := newReport(o.config, result, failure, passed)

	// Preserve the action error if writing its report also fails.
	if writeErr := writeReport(command.OutOrStdout(), o.format.String(), report); writeErr != nil {
		return &exit.Error{
			Code: ExitFailure,
			Err:  errors.Join(err, fmt.Errorf("write report: %w", writeErr)),
		}
	}

	// Execution failures are also summarized on stderr.
	if failure != nil {
		if _, writeErr := fmt.Fprintf(command.ErrOrStderr(), "module validate %s: %s: %s\n",
			o.config, category, err); writeErr != nil {
			return &exit.Error{
				Code: ExitFailure,
				Err:  errors.Join(err, fmt.Errorf("write failure summary: %w", writeErr)),
			}
		}
	}

	switch {
	case code != ExitSuccess:
		return &exit.Error{Code: code, Err: err}
	case !passed:
		// Findings without an execution failure still fail the
		// invocation; the report carries the details.
		return &exit.Error{Code: ExitFailure}
	default:
		return nil
	}
}
