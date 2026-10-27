// Package module wires the `module` command group of the binary. It
// owns the group command only: the `lint` and `deploy` subcommands live
// in their own packages, and every business execution goes through
// the injected action boundaries.
package module

import (
	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/deploy"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
)

// NewModuleCommand returns the `module` command group with its two
// registered subcommands, `lint` and `deploy`. The standard getter the
// root registered is forwarded to the deploy composition; lint is
// offline and never resolves it.
func NewModuleCommand(
	lintRun lint.Run,
	deployAction deploy.Deploy,
	getter genericclioptions.RESTClientGetter,
) *cobra.Command {
	group := &cobra.Command{
		Use:   "module",
		Short: "Offline platform module tooling",
		Long: "Offline tooling for platform module definitions: lint " +
			"checks a module definition and its rendered manifests " +
			"without a cluster, and deploy installs one module definition " +
			"and its controller package onto a cluster.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	group.AddCommand(lint.NewLintCommand(lintRun))
	group.AddCommand(deploy.NewDeployCommand(deployAction, getter))

	return group
}
