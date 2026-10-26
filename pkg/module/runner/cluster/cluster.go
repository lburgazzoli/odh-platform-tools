// Package cluster connects the run command to a Kubernetes cluster
// through the standard cli-runtime configuration: the same
// RESTClientGetter the root command's inherited ConfigFlags carry.
//
// The connection is invocation-scoped and lazy. Connect builds one
// controller-runtime client from a copied standard ToRESTConfig
// result — preserving credentials, TLS, impersonation, exec auth,
// request-timeout, and loader behavior — without contacting the
// server: discovery and the REST mapper resolve on first use. When
// the standard configuration carries no request timeout, Connect
// bounds every request to the invocation's remaining deadline, so
// client-internal discovery requests issued with background contexts
// of their own are still bounded; a user-provided request timeout is
// never extended. Calls that carry a context keep it.
//
// Required caller RBAC for a full run: discovery access
// (system:discovery or aggregated equivalent), get on namespaces, get
// on apiextensions.k8s.io customresourcedefinitions, and the
// create/get/update/patch permissions the framework deploy action
// needs for the rendered resources plus the persisted
// platformmoduledefinitions (including finalizers update for
// blockOwnerDeletion).
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

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// errNilGetter marks a missing standard configuration getter.
var errNilGetter = errors.New("kubernetes configuration getter is nil")

// Connection holds the single invocation-scoped controller-runtime
// client a run uses: uncached, built with default options, and
// carrying its own lazy REST mapper that reloads missing groups on
// demand — including groups served by CRDs established after the
// first mapping. It is created by Connect and never reused across
// invocations.
type Connection struct {
	client crclient.Client
}

// Connect builds a connection from the standard cli-runtime getter.
// It performs no bootstrap, payload, or cleanup writes and contacts
// no server: the client and its mapper are lazy. The standard REST
// configuration is copied so the connection never mutates the
// caller's getter state; credentials stay inside the standard client
// objects. When the copied configuration carries no request timeout,
// the invocation context's remaining deadline becomes the per-request
// bound — client-internal discovery requests issued with background
// contexts stay bounded, and a user --request-timeout is preserved
// exactly.
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
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering module definition scheme types: %w", err)
	}

	client, err := crclient.New(config, crclient.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating controller-runtime client: %w", err)
	}

	return &Connection{client: client}, nil
}

// NewWithClient wraps an existing controller-runtime client as a
// connection. Connect remains the production constructor; this seam
// exists for callers that exercise connection consumers against a
// client they construct themselves — the offline tests drive the
// framework deploy action through the controller-runtime fake client
// this way. The client is used exactly as given: no copy, timeout
// adjustment, or scheme change is made, so the connection's behavior
// is the wrapped client's behavior.
func NewWithClient(client crclient.Client) *Connection {
	return &Connection{client: client}
}

// ResolveNamespace resolves the effective namespace from the standard
// kubeconfig loader: an explicit getter override, the selected
// context's namespace, or the standard default. It is local
// preparation — no client is constructed and no request is issued —
// so it can run before Connect.
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

// NamespaceExists reports whether the namespace exists. It is a
// read-only check: no namespace is ever created or mutated, and an
// absent namespace is a plain false, leaving the caller to decide
// fatality. Errors other than absence are returned as-is.
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
