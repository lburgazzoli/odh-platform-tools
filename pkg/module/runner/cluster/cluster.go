// Package cluster connects the run command to a Kubernetes cluster
// through the standard cli-runtime configuration: the same
// RESTClientGetter the root command's inherited ConfigFlags carry.
//
// The connection is invocation-scoped and lazy. Connect builds every
// client from one copied standard ToRESTConfig result — preserving
// credentials, TLS, impersonation, exec auth, request-timeout, and
// loader behavior — without contacting the server: discovery and the
// REST mapper resolve on first use. All HTTP issued through the
// connection is bounded to the invocation context by a transport
// bridge, including the legacy discovery calls that carry no request
// context of their own.
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
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// crdPollInterval is the delay between CRD establishment polls; the
// first poll is immediate.
const crdPollInterval = 500 * time.Millisecond

// errNilGetter marks a missing standard configuration getter.
var errNilGetter = errors.New("kubernetes configuration getter is nil")

// Connection bundles the invocation-scoped Kubernetes clients a run
// uses: an uncached controller-runtime client, dynamic and discovery
// clients, an apiextensions clientset, and a resettable deferred
// REST mapper sharing one discovery cache. It is created by Connect
// and holds the invocation context that bounds every request issued
// through it; the runner keeps that context alive through the
// optional post-apply wait.
//
// A Connection is not safe for concurrent use and is never reused
// across invocations.
type Connection struct {
	// invocation is the context the transport bridge bounds requests
	// to. It is the CLI invocation context, not any single stage
	// context: earlier stage/request deadlines are preserved by the
	// bridge rather than extended.
	invocation context.Context

	config        *rest.Config
	scheme        *runtime.Scheme
	client        crclient.Client
	dynamic       dynamic.Interface
	discovery     discovery.CachedDiscoveryInterfaceWithContext
	apiExtensions apiextensionsclientset.Interface
	mapper        *restmapper.DeferredDiscoveryRESTMapper
}

// Connect builds a connection from the standard cli-runtime getter.
// It performs no bootstrap, payload, or cleanup writes and contacts
// no server: clients and the mapper are lazy. The standard REST
// configuration is copied so the connection never mutates the
// caller's getter state; credentials stay inside the standard client
// objects.
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
	bridgeTransport(ctx, config)

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

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating discovery client: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	apiExtensionsClient, err := apiextensionsclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating apiextensions client: %w", err)
	}

	cachedDiscovery := memory.NewMemCacheClient(discoveryClient)
	contextualDiscovery := discovery.ToCachedDiscoveryInterfaceWithContext(cachedDiscovery)
	mapper := restmapper.NewDeferredDiscoveryRESTMapperWithContext(contextualDiscovery)

	client, err := crclient.New(config, crclient.Options{
		Scheme: scheme,
		Mapper: mapper,
	})
	if err != nil {
		return nil, fmt.Errorf("creating controller-runtime client: %w", err)
	}

	return &Connection{
		invocation:    ctx,
		config:        config,
		scheme:        scheme,
		client:        client,
		dynamic:       dynamicClient,
		discovery:     contextualDiscovery,
		apiExtensions: apiExtensionsClient,
		mapper:        mapper,
	}, nil
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

// Client returns the uncached controller-runtime client sharing the
// connection's scheme and REST mapper.
func (c *Connection) Client() crclient.Client {
	return c.client
}

// Dynamic returns the dynamic client.
func (c *Connection) Dynamic() dynamic.Interface {
	return c.dynamic
}

// APIExtensions returns the apiextensions clientset used for CRD
// establishment checks.
func (c *Connection) APIExtensions() apiextensionsclientset.Interface {
	return c.apiExtensions
}

// Discovery returns the shared cached discovery interface; its cache
// is invalidated by RefreshDiscovery.
func (c *Connection) Discovery() discovery.CachedDiscoveryInterfaceWithContext {
	return c.discovery
}

// Mapper returns the resettable deferred REST mapper. Its discovery
// cache is shared with the connection; use RefreshDiscovery rather
// than resetting the mapper alone.
func (c *Connection) Mapper() *restmapper.DeferredDiscoveryRESTMapper {
	return c.mapper
}

// Scheme returns the connection scheme: the standard client-go and
// apiextensions types plus the module definition API.
func (c *Connection) Scheme() *runtime.Scheme {
	return c.scheme
}

// RefreshDiscovery drops the cached discovery data and resets the
// REST mapper so the next mapping request re-discovers, picking up
// CRDs established since the last refresh. The invalidation itself
// is bounded by ctx.
func (c *Connection) RefreshDiscovery(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.discovery.InvalidateWithContext(ctx)
	c.mapper.ResetWithContext(ctx)

	return nil
}

// WaitCRDs polls the named custom resource definitions until every
// one reports both Established and NamesAccepted, then refreshes
// discovery so newly served groups are mappable. Errors are returned
// with their identity preserved — an apiextensions NotFound, a
// discovery NoMatch, a context cancellation, or any request error
// reaches the caller unwrapped in kind, and a failure means no
// successful establishment is claimed.
func (c *Connection) WaitCRDs(ctx context.Context, names ...string) error {
	if len(names) == 0 {
		return ctx.Err()
	}
	crds := c.apiExtensions.ApiextensionsV1().CustomResourceDefinitions()

	err := wait.PollUntilContextCancel(ctx, crdPollInterval, true, func(ctx context.Context) (bool, error) {
		for _, name := range names {
			crd, err := crds.Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}

			if !conditionTrue(crd.Status.Conditions, apiextensionsv1.Established) {
				return false, nil
			}

			if !conditionTrue(crd.Status.Conditions, apiextensionsv1.NamesAccepted) {
				return false, nil
			}
		}

		return true, nil
	})
	if err != nil {
		return err
	}

	return c.RefreshDiscovery(ctx)
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

// conditionTrue reports whether the named condition is present and
// true.
func conditionTrue(
	conditions []apiextensionsv1.CustomResourceDefinitionCondition,
	conditionType apiextensionsv1.CustomResourceDefinitionConditionType,
) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == apiextensionsv1.ConditionTrue
		}
	}

	return false
}
