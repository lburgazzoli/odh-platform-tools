// Package cluster builds Kubernetes clients from standard cli-runtime configuration.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// errNilGetter marks a missing standard configuration getter.
var errNilGetter = errors.New("kubernetes configuration getter is nil")

// Connection holds an uncached client with a lazy REST mapper.
type Connection struct {
	client crclient.Client
}

// Connect creates a lazy, uncached client without changing the getter's configuration.
// When no request timeout is configured, it uses the remaining invocation budget
// as a per-request limit. Background discovery can outlive the invocation deadline.
func Connect(ctx context.Context, getter genericclioptions.RESTClientGetter) (*Connection, error) {
	if getter == nil {
		return nil, fmt.Errorf("resolving Kubernetes configuration: %w", errNilGetter)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	restConfig, err := getter.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("resolving Kubernetes configuration: %w", err)
	}

	config := rest.CopyConfig(restConfig)
	if config.Timeout == 0 {
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining > 0 {
				config.Timeout = remaining
			}
		}
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering standard scheme types: %w", err)
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering apiextensions scheme types: %w", err)
	}
	if err := toolApi.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering module definition scheme types: %w", err)
	}

	client, err := crclient.New(config, crclient.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating controller-runtime client: %w", err)
	}

	return &Connection{client: client}, nil
}

// NewWithClient wraps an existing client unchanged.
func NewWithClient(client crclient.Client) *Connection {
	return &Connection{client: client}
}

// ResolveNamespace uses the standard local kubeconfig resolution:
// explicit override, context namespace, then default.
func ResolveNamespace(getter genericclioptions.RESTClientGetter) (string, error) {
	if getter == nil {
		return "", fmt.Errorf("resolving namespace: %w", errNilGetter)
	}

	namespace, _, err := getter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return "", fmt.Errorf("resolving namespace: %w", err)
	}

	return namespace, nil
}

// Client returns the uncached controller-runtime client.
func (c *Connection) Client() crclient.Client {
	return c.client
}

// NamespaceExists returns false for NotFound and propagates other errors.
func (c *Connection) NamespaceExists(ctx context.Context, name string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	var namespace corev1.Namespace
	if err := c.client.Get(ctx, crclient.ObjectKey{Name: name}, &namespace); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}
