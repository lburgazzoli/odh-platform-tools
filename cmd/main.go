// Command odh-platform-tools provides platform module tooling.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	climodule "github.com/lburgazzoli/odh-platform-tools/cmd/module"
	deploycli "github.com/lburgazzoli/odh-platform-tools/cmd/module/deploy"
	generatecli "github.com/lburgazzoli/odh-platform-tools/cmd/module/generate"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	mockcli "github.com/lburgazzoli/odh-platform-tools/cmd/module/mock"
	validatecli "github.com/lburgazzoli/odh-platform-tools/cmd/module/validate"
)

func main() {
	os.Exit(run())
}

type exitCoder interface {
	ExitCode() int
}

// run executes the command and returns its process exit status.
func run() int {
	// Process signals become context cancellation for all module actions.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := NewRootCommand(
		lint.Action,
		deploycli.Action,
		mockcli.Action,
		generatecli.Action,
		validatecli.Action)

	if err := command.ExecuteContext(ctx); err != nil {
		// The opt-in diagnostic view surfaces the raw chain, including
		// the causes classified summaries withhold. It is best effort:
		// the failure being reported is the command's, not the
		// diagnostic write's.
		if debugEnabled(command) {
			_ = writeErrorChain(os.Stderr, err)
		}

		// Classified outcomes already wrote their report; every
		// other command or flag failure is a usage failure on stderr.
		var coder exitCoder
		if errors.As(err, &coder) {
			return coder.ExitCode()
		}

		fmt.Fprintln(os.Stderr, "module:", err)

		return lint.ExitUsage
	}

	return lint.ExitSuccess
}

// NewRootCommand registers module commands and shared Kubernetes flags.
func NewRootCommand(
	lintRun lint.Run,
	deployRun deploycli.Run,
	mockRun mockcli.Run,
	generateRun generatecli.Run,
	validateRun validatecli.Run,
) *cobra.Command {
	flags := genericclioptions.NewConfigFlags(true)

	root := &cobra.Command{
		Use:   "odh-platform-tools",
		Short: "OpenDataHub platform module tooling",
		// Reports and failure lines are printed by the commands
		// themselves; usage and flag errors are printed by main.
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	flags.AddFlags(root.PersistentFlags())

	root.PersistentFlags().Bool("debug", false,
		"on failure, print the wrapped error chain with every cause to stderr (diagnostic output; exit codes are unaffected)")

	root.AddCommand(climodule.NewModuleCommand(lintRun, deployRun, mockRun, generateRun, validateRun, flags))

	return root
}

// ExitCode preserves classified exit codes; unclassified failures are usage errors.
func ExitCode(err error) int {
	if err == nil {
		return lint.ExitSuccess
	}

	var coder exitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}

	return lint.ExitUsage
}
