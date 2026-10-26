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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	kindcluster "sigs.k8s.io/kind/pkg/cluster"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// provider is the supervised Kind provider seam: exactly the
// operations the engine uses, with no listing, so no engine code can
// enumerate or purge clusters it does not own. The production
// implementation is the real kind provider.
type provider interface {
	Create(name string, options ...kindcluster.CreateOption) error
	Delete(name, explicitKubeconfigPath string) error
	KubeConfig(name string, internal bool) (string, error)
	CollectLogs(name, dir string) error
}

// The real kind provider satisfies the seam.
var _ provider = (*kindcluster.Provider)(nil)

// Kind is the managed Kind engine: one isolated cluster with a
// unique name, a private kubeconfig, and supervised lifecycle
// operations. It deletes exactly the cluster it created — nothing
// else, ever — and a failed deletion reports the exact retained
// cluster with its private artifacts.
type Kind struct {
	opts     options
	provider provider
	name     string
	dir      string

	config *rest.Config
	client crclient.Client
}

// NewKind creates the engine's isolated cluster over the caller's
// options.
func NewKind(ctx context.Context, opts ...Option) (*Kind, error) {
	return newKind(ctx, newOptions(opts...))
}

// newKind creates the engine's isolated cluster: a unique name under
// the configured prefix, a private working directory for the
// kubeconfig and failure logs, and a supervised create with the
// recorded node image pin, readiness wait, and private kubeconfig
// path — kind never touches an ambient kubeconfig. A failed create
// collects logs and attempts an exact-cluster cleanup, and reports
// the cluster name either way.
func newKind(ctx context.Context, options options) (*Kind, error) {
	if options.Type != "" && options.Type != TypeKind {
		return nil, &InvalidOptionsError{Reason: "engine type " + options.Type + " is not " + TypeKind}
	}

	if err := options.validate(); err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "odh-platform-tools-kind-")
	if err != nil {
		return nil, fmt.Errorf("creating the engine's private directory: %w", err)
	}

	engine := &Kind{
		opts:     options,
		provider: options.provider,
		name:     options.NamePrefix + uniqueID(),
		dir:      dir,
	}

	if engine.provider == nil {
		engine.provider = kindcluster.NewProvider(options.ProviderOptions...)
	}

	logsDir := options.LogsDir
	if logsDir == "" {
		logsDir = filepath.Join(dir, "logs")
	}

	kubeconfigPath := engine.KubeconfigPath()

	createOptions := []kindcluster.CreateOption{
		kindcluster.CreateWithNodeImage(options.NodeImage),
		kindcluster.CreateWithWaitForReady(options.WaitForReady),
		kindcluster.CreateWithKubeconfigPath(kubeconfigPath),
	}
	createOptions = append(createOptions, options.CreateOptions...)

	if err := supervise(ctx, "creating kind cluster "+engine.name,
		options.CreateBudget, options.SuperviseGrace, options.LogFn,
		func() error {
			return engine.provider.Create(engine.name, createOptions...)
		}); err != nil {
		return nil, errors.Join(
			fmt.Errorf("creating kind cluster %q: %w", engine.name, err),
			engine.collectLogs(ctx, logsDir),
			engine.delete(ctx),
		)
	}

	kubeconfig, err := engine.readKubeConfig(ctx)
	if err != nil {
		return nil, errors.Join(
			err,
			engine.collectLogs(ctx, logsDir),
			engine.delete(ctx),
		)
	}

	// The private kubeconfig file is the subprocess-facing identity:
	// kind wrote it during the create, and the engine guarantees its
	// presence and permissions from the content it just read.
	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0o600); err != nil {
		return nil, errors.Join(
			fmt.Errorf("writing the private kubeconfig of kind cluster %q: %w", engine.name, err),
			engine.collectLogs(ctx, logsDir),
			engine.delete(ctx),
		)
	}

	config, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("building the rest config of kind cluster %q: %w", engine.name, err),
			engine.collectLogs(ctx, logsDir),
			engine.delete(ctx),
		)
	}

	engine.config = config

	return engine, nil
}

// Name is the exact created cluster name.
func (e *Kind) Name() string {
	return e.name
}

// KubeconfigPath is the private kubeconfig kind wrote for this
// cluster — never an ambient path.
func (e *Kind) KubeconfigPath() string {
	return filepath.Join(e.dir, "kubeconfig")
}

// ContextName is kind's fixed context name for the cluster.
func (e *Kind) ContextName() string {
	return "kind-" + e.name
}

// Config is the connected REST configuration.
func (e *Kind) Config() *rest.Config {
	return e.config
}

// Client is the connected controller-runtime client, available after
// Setup.
func (e *Kind) Client() crclient.Client {
	return e.client
}

// Setup connects the client over the engine's scheme and proves the
// cluster's API answers through a real discovery request.
func (e *Kind) Setup(_ context.Context) error {
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
		return fmt.Errorf("connecting the client of kind cluster %q: %w", e.name, err)
	}

	e.client = client

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(e.config)
	if err != nil {
		return fmt.Errorf("connecting discovery of kind cluster %q: %w", e.name, err)
	}

	if _, err := discoveryClient.ServerVersion(); err != nil {
		return fmt.Errorf("kind cluster %q is not answering: %w", e.name, err)
	}

	return nil
}

// Stop deletes exactly the cluster the engine created and removes
// its private directory. A failed deletion reports the exact
// retained cluster and the private artifacts kept for inspection.
func (e *Kind) Stop(ctx context.Context) error {
	if e == nil || e.provider == nil || e.name == "" {
		return nil
	}

	if err := e.delete(ctx); err != nil {
		return fmt.Errorf("kind cluster %q was NOT deleted and must be removed manually; "+
			"its private artifacts are kept under %s: %w", e.name, e.dir, err)
	}

	if err := os.RemoveAll(e.dir); err != nil {
		return fmt.Errorf("kind cluster %q was deleted but its private directory %s was not: %w",
			e.name, e.dir, err)
	}

	return nil
}

// CollectLogs collects the cluster's diagnostics into the engine's
// logs directory under its own bounded supervision. It never deletes
// anything and is safe to call on failure paths.
func (e *Kind) CollectLogs(ctx context.Context) error {
	if e == nil || e.provider == nil || e.name == "" {
		return nil
	}

	logsDir := e.opts.LogsDir
	if logsDir == "" {
		logsDir = filepath.Join(e.dir, "logs")
	}

	return e.collectLogs(ctx, logsDir)
}

// delete deletes exactly the engine's cluster under a bounded
// supervision.
func (e *Kind) delete(ctx context.Context) error {
	return supervise(ctx, "deleting kind cluster "+e.name,
		e.opts.DeleteBudget, e.opts.SuperviseGrace, e.opts.LogFn,
		func() error {
			return e.provider.Delete(e.name, e.KubeconfigPath())
		})
}

// collectLogs collects the engine cluster's diagnostics under a
// bounded supervision separate from every other operation.
func (e *Kind) collectLogs(ctx context.Context, logsDir string) error {
	return supervise(ctx, "collecting logs of kind cluster "+e.name,
		e.opts.LogsBudget, e.opts.SuperviseGrace, e.opts.LogFn,
		func() error {
			if err := e.provider.CollectLogs(e.name, logsDir); err != nil {
				return fmt.Errorf("collecting logs of kind cluster %q into %s: %w",
					e.name, logsDir, err)
			}

			e.opts.LogFn("kind cluster %s logs written to %s", e.name, logsDir)

			return nil
		})
}

// readKubeConfig reads the cluster's private kubeconfig content
// under a bounded supervision.
func (e *Kind) readKubeConfig(ctx context.Context) (string, error) {
	var kubeconfig string

	if err := supervise(ctx, "reading the kubeconfig of kind cluster "+e.name,
		e.opts.LogsBudget, e.opts.SuperviseGrace, e.opts.LogFn,
		func() error {
			content, err := e.provider.KubeConfig(e.name, false)
			if err != nil {
				return fmt.Errorf("reading the kubeconfig of kind cluster %q: %w", e.name, err)
			}

			kubeconfig = content

			return nil
		}); err != nil {
		return "", err
	}

	return kubeconfig, nil
}

// uniqueID renders one collision-free cluster name suffix.
func uniqueID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		// Fall back to a time-based identity: uniqueness within one
		// test run is what the name needs.
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}

	return hex.EncodeToString(buffer)
}
