// Package module wires the lint, deploy, generate, mock, and validate subcommands to their actions.
package module

import (
	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/deploy"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/generate"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/mock"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/validate"
)

// NewModuleCommand wires the standard Kubernetes getter to deploy, mock, and validate.
func NewModuleCommand(
	lintRun lint.Run,
	deployRun deploy.Run,
	mockRun mock.Run,
	generateRun generate.Run,
	validateRun validate.Run,
	getter genericclioptions.RESTClientGetter,
) *cobra.Command {
	group := &cobra.Command{
		Use:   "module",
		Short: "Manage platform modules",
		Long: "Lint checks a module definition and its rendered manifests offline. " +
			"Deploy installs one module definition and its controller package. " +
			"Generate infers a module definition from local Helm or Kustomize " +
			"manifests. Mock runs a status-only controller for a compatible " +
			"module resource. Validate checks a running module against the " +
			"platform contract, read-only.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	group.AddCommand(lint.NewLintCommand(lintRun))
	group.AddCommand(deploy.NewDeployCommand(deployRun, getter))
	group.AddCommand(generate.NewGenerateCommand(generateRun))
	group.AddCommand(mock.NewMockCommand(mockRun, getter))
	group.AddCommand(validate.NewValidateCommand(validateRun, getter))

	return group
}
