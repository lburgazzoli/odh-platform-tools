/*
Copyright 2026 Luca Burgazzoli

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package mock exposes the foreground module mock command.
package mock

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	modmock "github.com/lburgazzoli/odh-platform-tools/pkg/module/mock"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/exit"
)

// Run is the command's foreground controller action.
type Run func(ctx context.Context, getter genericclioptions.RESTClientGetter, options modmock.Options) error

// Action resolves the inherited Kubernetes connection and runs the mock.
func Action(ctx context.Context, getter genericclioptions.RESTClientGetter, options modmock.Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	config, err := getter.ToRESTConfig()
	if err != nil {
		return moduleerrors.New(moduleerrors.ErrMockRuntime, "could not resolve Kubernetes REST configuration", moduleerrors.WithCause(err))
	}

	controller := modmock.New(config, options)

	if err := controller.Run(ctx); err != nil {
		return moduleerrors.WrapStage("run the mock controller", err)
	}

	return nil
}

// NewMockCommand binds the required flags to a foreground controller action.
func NewMockCommand(action Run, getter genericclioptions.RESTClientGetter) *cobra.Command {
	var input flags
	command := &cobra.Command{
		Use:   "mock",
		Short: "Run a status-only mock module controller",
		Long: "Watch a compatible cluster-scoped singleton custom resource and report " +
			"condition controls and the platform version from a ConfigMap. In-cluster " +
			"runs use the Pod namespace by default. The inherited --namespace flag can override it.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			options, err := input.options(getter)
			if err == nil {
				err = action(command.Context(), getter, options)
			}

			return reportError(command, err)
		},
	}
	flags := command.Flags()
	flags.StringVar(&input.apiVersion, "api-version", "", "custom resource group/version (required)")
	flags.StringVar(&input.kind, "kind", "", "custom resource kind (required)")
	flags.StringVar(&input.platformConfigMap, "platform-configmap", "", "platform ConfigMap name (required)")

	for _, flag := range []string{"api-version", "kind", "platform-configmap"} {
		if err := command.MarkFlagRequired(flag); err != nil {
			panic(err)
		}
	}

	return command
}

func reportError(command *cobra.Command, err error) error {
	if err == nil {
		return nil
	}

	code := 1
	if errors.Is(err, moduleerrors.ErrInvalidMockOptions) {
		code = 2
	}

	if _, writeErr := fmt.Fprintln(command.ErrOrStderr(), "module mock:", err); writeErr != nil {
		return &exit.Error{
			Code: 1,
			Err:  errors.Join(err, fmt.Errorf("write mock failure summary: %w", writeErr)),
		}
	}

	return &exit.Error{Code: code, Err: err}
}
