package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
)

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

	root.AddCommand(NewModuleCommand(run))

	return root
}

// NewModuleCommand returns the `module` command group with exactly
// one registered subcommand, `lint`.
func NewModuleCommand(run lint.Run) *cobra.Command {
	group := &cobra.Command{
		Use:   "module",
		Short: "Offline platform module tooling",
		Long: "Offline tooling for platform module definitions: lint " +
			"checks a module definition and its rendered manifests " +
			"without a cluster.",
		// Deferred subcommands such as `run` are rejected as unknown
		// commands; a bare `module` shows this help.
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	group.AddCommand(lint.NewLintCommand(run))

	return group
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
