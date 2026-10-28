// Command module provides offline platform module tooling.
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
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module"
)

func main() {
	os.Exit(run())
}

type exitCoder interface {
	ExitCode() int
}

// run executes the command and returns its process exit status.
func run() int {
	// Process signals become context cancellation so an interrupted
	// lint or deploy is classified as cancellation, not a crash.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := NewRootCommand(module.NewLinter().Run, deploycli.Action)

	if err := command.ExecuteContext(ctx); err != nil {
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
func NewRootCommand(lintRun lint.Run, deployAction deploycli.Deploy) *cobra.Command {
	flags := genericclioptions.NewConfigFlags(true)

	root := &cobra.Command{
		Use:   "odh-platform-tools",
		Short: "Offline platform module tooling",
		// Reports and failure lines are printed by the commands
		// themselves; usage and flag errors are printed by main.
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	flags.AddFlags(root.PersistentFlags())

	root.AddCommand(climodule.NewModuleCommand(lintRun, deployAction, flags))

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
