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

	climodule "github.com/lburgazzoli/odh-platform-tools/cmd/module"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module"
)

func main() {
	os.Exit(run())
}

// run executes the command and returns its process exit status.
func run() int {
	// Process signals become context cancellation so an interrupted
	// lint is classified as cancellation, not a crash.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := NewRootCommand(module.NewLinter().Run)

	if err := command.ExecuteContext(ctx); err != nil {
		// Classified lint outcomes already wrote their report; every
		// other command or flag failure is a usage failure on stderr.
		var exitErr *lint.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.Code
		}

		fmt.Fprintln(os.Stderr, "module:", err)

		return lint.ExitUsage
	}

	return lint.ExitSuccess
}

// NewRootCommand returns the binary root command with exactly one
// registered command tree, `module lint`. Deferred commands such as
// `module run` are not stubbed: Cobra rejects them as unknown
// commands, and legacy descriptor override flags are rejected as
// unknown flags.
func NewRootCommand(run lint.Run) *cobra.Command {
	root := &cobra.Command{
		Use:   "odh-platform-tools",
		Short: "Offline platform module tooling",
		// Reports and failure lines are printed by the lint command
		// itself; usage and flag errors are printed by main.
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(climodule.NewModuleCommand(run))

	return root
}

// ExitCode maps one command execution error to its process exit
// status: classified lint outcomes keep their own code, and every
// other command or flag failure is a usage failure.
func ExitCode(err error) int {
	if err == nil {
		return lint.ExitSuccess
	}

	var exitErr *lint.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}

	return lint.ExitUsage
}
