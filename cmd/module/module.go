// Package module wires the lint, deploy, and mock subcommands to their actions.
package module

import (
	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/deploy"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/mock"
)

// NewModuleCommand wires the standard Kubernetes getter to deploy and mock.
func NewModuleCommand(
	lintRun lint.Run,
	deployAction deploy.Deploy,
	mockRun mock.Run,
	getter genericclioptions.RESTClientGetter,
) *cobra.Command {
	group := &cobra.Command{
		Use:   "module",
		Short: "Manage platform modules",
		Long: "Lint checks a module definition and its rendered manifests offline. " +
			"Deploy installs one module definition and its controller package. " +
			"Mock runs a status-only controller for a compatible module resource.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	group.AddCommand(lint.NewLintCommand(lintRun))
	group.AddCommand(deploy.NewDeployCommand(deployAction, getter))
	group.AddCommand(mock.NewMockCommand(mockRun, getter))

	return group
}
