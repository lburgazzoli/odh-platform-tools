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

package cluster

import (
	"context"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// External is the confirmed external-cluster engine: one explicit
// kubeconfig file and one explicit context, both required, with no
// current-context fallback. It never deletes anything — the cluster
// stays exactly as the suite found it — and its Stop is a no-op.
type External struct {
	kubeconfigPath string
	contextName    string

	config *rest.Config
	client crclient.Client
}

// NewExternal builds the engine over the caller's options.
func NewExternal(ctx context.Context, opts ...Option) (*External, error) {
	return newExternal(ctx, newOptions(opts...))
}

// newExternal builds the engine from an explicitly confirmed
// kubeconfig and context. Both must name existing entries in the
// file; there is no ambient fallback and no default context.
func newExternal(_ context.Context, options options) (*External, error) {
	if options.Type != "" && options.Type != TypeExternal {
		return nil, &InvalidOptionsError{Reason: "engine type " + options.Type + " is not " + TypeExternal}
	}

	if options.Kubeconfig == "" {
		return nil, &InvalidOptionsError{
			Reason: "the external engine requires an explicit kubeconfig path " +
				"(no ambient kubeconfig or current-context fallback)",
		}
	}

	if options.Context == "" {
		return nil, &InvalidOptionsError{
			Reason: "the external engine requires an explicit context name " +
				"(no current-context fallback)",
		}
	}

	// The configuration loads offline: existence of the file and the
	// named context is proven before anything connects.
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.Kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: options.Context},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading the external kubeconfig %q with context %q: %w",
			options.Kubeconfig, options.Context, err)
	}

	return &External{
		kubeconfigPath: options.Kubeconfig,
		contextName:    options.Context,
		config:         config,
	}, nil
}

// Name is the confirmed context name.
func (e *External) Name() string {
	return e.contextName
}

// KubeconfigPath is the confirmed kubeconfig file path.
func (e *External) KubeconfigPath() string {
	return e.kubeconfigPath
}

// ContextName is the confirmed context name.
func (e *External) ContextName() string {
	return e.contextName
}

// Config is the connected REST configuration.
func (e *External) Config() *rest.Config {
	return e.config
}

// Client is the connected controller-runtime client, available after
// Setup.
func (e *External) Client() crclient.Client {
	return e.client
}

// Setup connects the client over the engine's scheme and proves the
// confirmed cluster's API answers through a real discovery request.
func (e *External) Setup(_ context.Context) error {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return fmt.Errorf("registering the client-go scheme: %w", err)
	}

	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("registering the apiextensions scheme: %w", err)
	}

	if err := toolApi.AddToScheme(scheme); err != nil {
		return fmt.Errorf("registering the module scheme: %w", err)
	}

	client, err := crclient.New(e.config, crclient.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("connecting the client of external context %q: %w", e.contextName, err)
	}

	e.client = client

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(e.config)
	if err != nil {
		return fmt.Errorf("connecting discovery of external context %q: %w", e.contextName, err)
	}

	if _, err := discoveryClient.ServerVersion(); err != nil {
		return fmt.Errorf("external context %q is not answering: %w", e.contextName, err)
	}

	return nil
}

// Stop never deletes the confirmed cluster. The external engine owns
// nothing: the cluster stays exactly as the suite found it.
func (e *External) Stop(_ context.Context) error {
	return nil
}
