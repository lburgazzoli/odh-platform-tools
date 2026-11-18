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

	return modmock.New(config, options).Run(ctx)
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

type exitError struct {
	code  int
	cause error
}

func (e *exitError) Error() string { return e.cause.Error() }
func (e *exitError) Unwrap() error { return e.cause }
func (e *exitError) ExitCode() int { return e.code }

func reportError(command *cobra.Command, err error) error {
	if err == nil {
		return nil
	}

	code := 1
	if errors.Is(err, moduleerrors.ErrInvalidMockOptions) {
		code = 2
	}

	if _, writeErr := fmt.Fprintln(command.ErrOrStderr(), "module mock:", err); writeErr != nil {
		return &exitError{code: 1, cause: errors.Join(err, fmt.Errorf("write mock failure summary: %w", writeErr))}
	}

	return &exitError{code: code, cause: err}
}
