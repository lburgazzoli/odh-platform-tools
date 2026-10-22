// Package module wires the `module` command group of the binary. It
// owns the group command only: the `lint` subcommand lives in
// cmd/module/lint, and every business execution goes through the
// injected action boundary.
package module

import (
	"github.com/spf13/cobra"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
)

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
