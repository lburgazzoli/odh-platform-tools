// Command module provides offline platform module tooling.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lburgazzoli/odh-platform-tools/pkg/cli"
	"github.com/lburgazzoli/odh-platform-tools/pkg/cli/lint"
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

	command := cli.NewRootCommand(module.NewLinter().Run)

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
