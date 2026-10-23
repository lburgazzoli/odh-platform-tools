package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// testKubeconfigYAML is a standalone kubeconfig with one context
// carrying an explicit namespace, pointing at an unreachable server:
// lazy connection construction must not need it.
const testKubeconfigYAML = `apiVersion: v1
kind: Config
clusters:
- name: test-cluster
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
    namespace: kubeconfig-namespace
current-context: test-context
users:
- name: test-user
  user: {}
`

// testKubeconfigNoNamespaceYAML is a standalone kubeconfig whose
// context sets no namespace, pinning the standard default fallback.
const testKubeconfigNoNamespaceYAML = `apiVersion: v1
kind: Config
clusters:
- name: test-cluster
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
current-context: test-context
users:
- name: test-user
  user: {}
`

// apiVersionsJSON is the core /api discovery document.
const apiVersionsJSON = `{"kind":"APIVersions","versions":["v1"],"serverAddressByClientCIDRs":[]}
`

// coreV1ResourcesJSON is the core /api/v1 discovery document carrying
// the namespaces resource.
const coreV1ResourcesJSON = `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"v1","resources":[{"name":"namespaces","singularName":"namespace","namespaced":false,"kind":"Namespace","verbs":["get","list"]}]}
`

// emptyGroupListJSON is the /apis discovery document with no groups.
const emptyGroupListJSON = `{"kind":"APIGroupList","apiVersion":"v1","groups":[]}
`

// widgetsGroupListJSON is the /apis discovery document after the
// widget CRD group is served.
const widgetsGroupListJSON = `{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"widgets.example.com","versions":[{"groupVersion":"widgets.example.com/v1","version":"v1"}],"preferredVersion":{"groupVersion":"widgets.example.com/v1","version":"v1"}}]}
`

// widgetsResourcesJSON is the widget group's resource discovery
// document.
const widgetsResourcesJSON = `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"widgets.example.com/v1","resources":[{"name":"widgets","singularName":"widget","namespaced":true,"kind":"Widget","verbs":["get","list","watch","create","update","patch","delete"]}]}
`

// namespaceExistsJSON is the namespace body served for an existing
// namespace.
const namespaceExistsJSON = `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"exists"}}
`

// notFoundStatusJSON is a Kubernetes NotFound status body.
const notFoundStatusJSON = `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"not found","reason":"NotFound","code":404}
`

// internalErrorStatusJSON is a Kubernetes internal-error status body.
const internalErrorStatusJSON = `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"broken","reason":"InternalError","code":500}
`

// crdEstablishedJSON is an established custom resource definition.
const crdEstablishedJSON = `{"apiVersion":"apiextensions.k8s.io/v1","kind":"CustomResourceDefinition","metadata":{"name":"widgets.example.com"},"status":{"conditions":[{"type":"Established","status":"True"},{"type":"NamesAccepted","status":"True"}]}}
`

// crdUnestablishedJSON is a custom resource definition that exists
// but is not established yet.
const crdUnestablishedJSON = `{"apiVersion":"apiextensions.k8s.io/v1","kind":"CustomResourceDefinition","metadata":{"name":"widgets.example.com"},"status":{"conditions":[{"type":"Established","status":"False"},{"type":"NamesAccepted","status":"True"}]}}
`

// emptyListJSON is an empty list response for dynamic reads.
const emptyListJSON = `{"apiVersion":"v1","kind":"List","items":[]}
`

// fakeAPIServer is a minimal API server double: core discovery,
// toggleable widget-group discovery, namespace reads, CRD reads, and
// a generic empty-list response. It records the impersonation user of
// the requests it receives.
type fakeAPIServer struct {
	*httptest.Server

	widgets     atomic.Bool
	impersonate atomic.Pointer[string]

	mu         sync.Mutex
	namespaces map[string]int
	crdBodies  map[string][]string
	crdCalls   map[string]int
}

// newFakeAPIServer starts the double; the server closes with the
// test.
func newFakeAPIServer(t *testing.T) *fakeAPIServer {
	t.Helper()

	fake := &fakeAPIServer{
		namespaces: map[string]int{},
		crdBodies:  map[string][]string{},
		crdCalls:   map[string]int{},
	}
	fake.Server = httptest.NewServer(fake)

	t.Cleanup(fake.Close)

	return fake
}

// setNamespace serves the given HTTP status for one namespace name.
func (s *fakeAPIServer) setNamespace(name string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.namespaces[name] = status
}

// setCRD serves the given response bodies for one CRD name in order;
// the last body is sticky.
func (s *fakeAPIServer) setCRD(name string, bodies ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.crdBodies[name] = bodies
}

// crdRequestCount returns how many reads a CRD name received.
func (s *fakeAPIServer) crdRequestCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.crdCalls[name]
}

// ServeHTTP implements http.Handler.
func (s *fakeAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if user := r.Header.Get("Impersonate-User"); user != "" {
		s.impersonate.Store(&user)
	}

	writeJSON := func(body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api":
		writeJSON(apiVersionsJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1":
		writeJSON(coreV1ResourcesJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/apis":
		if s.widgets.Load() {
			writeJSON(widgetsGroupListJSON)
		} else {
			writeJSON(emptyGroupListJSON)
		}
	case r.Method == http.MethodGet && r.URL.Path == "/apis/widgets.example.com/v1":
		writeJSON(widgetsResourcesJSON)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/"):
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/")

		s.mu.Lock()
		status := s.namespaces[name]
		s.mu.Unlock()

		switch status {
		case http.StatusOK:
			writeJSON(namespaceExistsJSON)
		case http.StatusNotFound:
			w.WriteHeader(http.StatusNotFound)
			writeJSON(notFoundStatusJSON)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(internalErrorStatusJSON)
		}
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/"):
		name := strings.TrimPrefix(r.URL.Path, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/")

		s.mu.Lock()
		s.crdCalls[name]++
		bodies := s.crdBodies[name]
		s.mu.Unlock()

		if len(bodies) == 0 {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(notFoundStatusJSON)

			return
		}

		call := s.crdRequestCount(name)
		if call > len(bodies) {
			call = len(bodies)
		}

		writeJSON(bodies[call-1])
	default:
		writeJSON(emptyListJSON)
	}
}

// connected returns a connection to the fake server through standard
// ConfigFlags: a real kubeconfig on disk with the server address and
// optional impersonation applied as standard overrides.
func connected(
	invocation context.Context,
	t *testing.T,
	g *WithT,
	server *fakeAPIServer,
	impersonate string,
) *Connection {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	g.Expect(os.WriteFile(path, []byte(testKubeconfigYAML), 0o600)).To(Succeed())

	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path

	serverURL := server.URL
	flags.APIServer = &serverURL

	if impersonate != "" {
		flags.Impersonate = &impersonate
	}

	connection, err := Connect(invocation, flags)
	g.Expect(err).To(Succeed())

	return connection
}

// testConfigFlags writes the kubeconfig fixture to disk and returns
// standard ConfigFlags bound to it.
func testConfigFlags(t *testing.T, document string) *genericclioptions.ConfigFlags {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path

	return flags
}

// TestResolveNamespace reads the context namespace from the standard
// loader, honors an explicit override set before first use — the
// persistent ConfigFlags loader snapshots overrides on first call —
// and needs no client or request: the kubeconfig points at an
// unreachable server.
func TestResolveNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	namespace, err := ResolveNamespace(testConfigFlags(t, testKubeconfigYAML))
	g.Expect(err).To(Succeed())
	g.Expect(namespace).To(Equal("kubeconfig-namespace"))

	path := filepath.Join(t.TempDir(), "kubeconfig")
	g.Expect(os.WriteFile(path, []byte(testKubeconfigYAML), 0o600)).To(Succeed())

	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path
	override := "override-namespace"
	flags.Namespace = &override

	namespace, err = ResolveNamespace(flags)
	g.Expect(err).To(Succeed())
	g.Expect(namespace).To(Equal("override-namespace"))
}

// TestResolveNamespaceStandardDefault pins the standard fallback when
// the selected context carries no namespace.
func TestResolveNamespaceStandardDefault(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	namespace, err := ResolveNamespace(testConfigFlags(t, testKubeconfigNoNamespaceYAML))
	g.Expect(err).To(Succeed())
	g.Expect(namespace).To(Equal("default"))
}

// TestResolveNamespaceRequiresGetter pins the nil-getter error.
func TestResolveNamespaceRequiresGetter(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	_, err := ResolveNamespace(nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("getter is nil"))
}

// TestConnectBuildsLazyClients pins that Connect constructs every
// client without contacting the server — the kubeconfig points at an
// unreachable address — and that the scheme knows the module
// definition API.
func TestConnectBuildsLazyClients(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	connection, err := Connect(t.Context(), testConfigFlags(t, testKubeconfigYAML))
	g.Expect(err).To(Succeed())

	g.Expect(connection.Client()).NotTo(BeNil())
	g.Expect(connection.Dynamic()).NotTo(BeNil())
	g.Expect(connection.APIExtensions()).NotTo(BeNil())
	g.Expect(connection.Mapper()).NotTo(BeNil())

	definitionGVK := v1alpha1.GroupVersion.WithKind("PlatformModuleDefinition")
	g.Expect(connection.Scheme().Recognizes(definitionGVK)).To(BeTrue())
}

// TestConnectRequiresGetter pins the nil-getter error.
func TestConnectRequiresGetter(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	_, err := Connect(t.Context(), nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("getter is nil"))
}

// TestConnectRejectsCanceledContext pins that an expired invocation
// never builds clients.
func TestConnectRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Connect(ctx, testConfigFlags(t, testKubeconfigYAML))
	g.Expect(err).To(MatchError(context.Canceled))
}

// TestConnectPropagatesStandardOverrides pins that the standard
// server and impersonation overrides reach the connection's clients:
// the request succeeds against the overridden server — the kubeconfig
// address is unreachable — and carries the impersonation header.
func TestConnectPropagatesStandardOverrides(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := newFakeAPIServer(t)
	connection := connected(t.Context(), t, g, server, "impersonated-user")

	list, err := connection.Dynamic().
		Resource(schema.GroupVersionResource{Group: "widgets.example.com", Version: "v1", Resource: "widgets"}).
		List(t.Context(), metav1.ListOptions{})
	g.Expect(err).To(Succeed())
	g.Expect(list.Items).To(BeEmpty())

	g.Eventually(func() string {
		if user := server.impersonate.Load(); user != nil {
			return *user
		}

		return ""
	}).Should(Equal("impersonated-user"))
}

// TestNamespaceExists pins the read-only existence check: present,
// absent, and failing responses, through the controller-runtime
// client and the shared mapper.
func TestNamespaceExists(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := newFakeAPIServer(t)
	server.setNamespace("exists", http.StatusOK)
	server.setNamespace("missing", http.StatusNotFound)
	server.setNamespace("broken", http.StatusInternalServerError)

	connection := connected(t.Context(), t, g, server, "")

	exists, err := connection.NamespaceExists(t.Context(), "exists")
	g.Expect(err).To(Succeed())
	g.Expect(exists).To(BeTrue())

	missing, err := connection.NamespaceExists(t.Context(), "missing")
	g.Expect(err).To(Succeed())
	g.Expect(missing).To(BeFalse())

	broken, err := connection.NamespaceExists(t.Context(), "broken")
	g.Expect(err).To(HaveOccurred())
	g.Expect(apierrors.IsInternalError(err)).To(BeTrue())
	g.Expect(broken).To(BeFalse())
}

// TestWaitCRDsWaitsForEstablishment pins the polling: a CRD that is
// not established on the first read is retried until both conditions
// hold, without inventing success from a partial state.
func TestWaitCRDsWaitsForEstablishment(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := newFakeAPIServer(t)
	server.setCRD("widgets.example.com", crdUnestablishedJSON, crdEstablishedJSON)

	connection := connected(t.Context(), t, g, server, "")

	g.Expect(connection.WaitCRDs(t.Context(), "widgets.example.com")).To(Succeed())
	g.Expect(server.crdRequestCount("widgets.example.com")).To(BeNumerically(">=", 2))
}

// TestWaitCRDsPreservesNotFoundIdentity pins that a missing CRD
// surfaces as an apiextensions NotFound, not a generic poll timeout.
func TestWaitCRDsPreservesNotFoundIdentity(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := newFakeAPIServer(t)
	connection := connected(t.Context(), t, g, server, "")

	err := connection.WaitCRDs(t.Context(), "widgets.example.com")
	g.Expect(err).To(HaveOccurred())
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
}

// TestWaitCRDsHonorsCancellation pins that a canceled context stops
// the wait with its own identity.
func TestWaitCRDsHonorsCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := newFakeAPIServer(t)
	server.setCRD("widgets.example.com", crdUnestablishedJSON)

	connection := connected(t.Context(), t, g, server, "")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	g.Expect(connection.WaitCRDs(ctx, "widgets.example.com")).To(MatchError(context.Canceled))
}

// TestWaitCRDsRequiresNames pins the empty-input contract: no CRDs
// means nothing to wait for.
func TestWaitCRDsRequiresNames(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := newFakeAPIServer(t)
	connection := connected(t.Context(), t, g, server, "")

	g.Expect(connection.WaitCRDs(t.Context())).To(Succeed())
}

// TestRefreshDiscoveryPicksUpEstablishedCRDs pins the invalidation:
// a group served only after the mapper cached discovery stays absent
// until RefreshDiscovery drops the cache, then maps.
func TestRefreshDiscoveryPicksUpEstablishedCRDs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := newFakeAPIServer(t)
	connection := connected(t.Context(), t, g, server, "")

	widget := schema.GroupKind{Group: "widgets.example.com", Kind: "Widget"}

	_, err := connection.Mapper().RESTMapping(widget, "v1")
	g.Expect(err).To(HaveOccurred())
	g.Expect(meta.IsNoMatchError(err)).To(BeTrue())

	server.widgets.Store(true)

	// The cached discovery still answers without the new group.
	_, err = connection.Mapper().RESTMapping(widget, "v1")
	g.Expect(meta.IsNoMatchError(err)).To(BeTrue())

	g.Expect(connection.RefreshDiscovery(t.Context())).To(Succeed())

	mapping, err := connection.Mapper().RESTMapping(widget, "v1")
	g.Expect(err).To(Succeed())
	g.Expect(mapping.Resource.Resource).To(Equal("widgets"))
}

// TestRefreshDiscoveryHonorsCancellation pins the bounded
// invalidation.
func TestRefreshDiscoveryHonorsCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := newFakeAPIServer(t)
	connection := connected(t.Context(), t, g, server, "")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	g.Expect(connection.RefreshDiscovery(ctx)).To(MatchError(context.Canceled))
}

// TestConnectionDiscoveryIsBounded pins that the cached discovery
// interface handed to consumers still answers the legacy no-context
// calls, which the transport bridge bounds to the invocation.
func TestConnectionDiscoveryIsBounded(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := newFakeAPIServer(t)
	connection := connected(t.Context(), t, g, server, "")

	var _ discovery.DiscoveryInterfaceWithContext = connection.Discovery()

	groups, err := connection.Discovery().ServerGroupsWithContext(t.Context())
	g.Expect(err).To(Succeed())

	// The legacy core group is always reported from /api.
	g.Expect(groups.Groups).To(HaveLen(1))
	g.Expect(groups.Groups[0].PreferredVersion.Version).To(Equal("v1"))
}
