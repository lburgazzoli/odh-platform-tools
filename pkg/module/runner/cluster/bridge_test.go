package cluster

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
)

// stalledHandler never answers on its own: it returns only when the
// request context is done, standing in for a wedged API server.
func stalledHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.WriteHeader(http.StatusInternalServerError)
	})
}

// stalledConnection connects to a wedged server through standard
// ConfigFlags with an explicit invocation context.
func stalledConnection(
	invocation context.Context,
	t *testing.T,
	g *WithT,
) *Connection {
	t.Helper()

	server := httptest.NewServer(stalledHandler())
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "kubeconfig")
	g.Expect(os.WriteFile(path, []byte(testKubeconfigYAML), 0o600)).To(Succeed())

	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path

	serverURL := server.URL
	flags.APIServer = &serverURL

	connection, err := Connect(invocation, flags)
	g.Expect(err).To(Succeed())

	return connection
}

// stalledLegacyDiscoveryFailsWithinInvocationDeadline pins the
// transport bridge: a legacy discovery call with no request context
// of its own still ends when the invocation deadline expires, instead
// of hanging until the server answers.
func TestStalledLegacyDiscoveryFailsWithinInvocationDeadline(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	invocation, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	connection := stalledConnection(invocation, t, g)

	start := time.Now()

	// The legacy no-context discovery surface is what the deferred
	// mapper exercises internally; call it directly so the stall is
	// not masked by any contextual wrapper.
	legacy, ok := connection.Discovery().(discovery.DiscoveryInterface)
	g.Expect(ok).To(BeTrue())

	_, err := legacy.ServerGroups()
	g.Expect(err).To(HaveOccurred())
	g.Expect(time.Since(start)).To(BeNumerically("<", 10*time.Second))
	g.Expect(invocation.Err()).To(HaveOccurred())
}

// TestStalledRequestFailsWithinEarlierRequestDeadline pins the
// deadline composition: with a long-lived invocation, a request whose
// own context carries an earlier deadline fails on that deadline and
// the invocation stays alive.
func TestStalledRequestFailsWithinEarlierRequestDeadline(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	invocation, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	connection := stalledConnection(invocation, t, g)

	requestCtx, requestCancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer requestCancel()

	widgets := schema.GroupVersionResource{Group: "widgets.example.com", Version: "v1", Resource: "widgets"}

	start := time.Now()
	_, err := connection.Dynamic().Resource(widgets).List(requestCtx, metav1.ListOptions{})
	g.Expect(err).To(HaveOccurred())
	g.Expect(time.Since(start)).To(BeNumerically("<", 10*time.Second))
	g.Expect(invocation.Err()).NotTo(HaveOccurred())
}

// TestBridgedBodyCloseReleasesRequestContext pins the cleanup
// contract at the transport level: a fully-read response releases the
// merged request context and unregisters the invocation hook when
// its body closes, so a finished request leaves nothing bound to the
// invocation.
func TestBridgedBodyCloseReleasesRequestContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	invocation, cancel := context.WithCancel(t.Context())
	defer cancel()

	var requestContext context.Context
	transport := &bridgedTransport{
		invocation: invocation,
		base: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			requestContext = req.Context()

			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("payload")),
				Request:    req,
			}, nil
		}),
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/api", nil)
	g.Expect(err).To(Succeed())

	resp, err := transport.RoundTrip(req)
	g.Expect(err).To(Succeed())

	// While the body is open the merged context is alive.
	g.Expect(requestContext.Err()).NotTo(HaveOccurred())

	payload, err := io.ReadAll(resp.Body)
	g.Expect(err).To(Succeed())
	g.Expect(payload).To(Equal([]byte("payload")))
	g.Expect(resp.Body.Close()).To(Succeed())

	// Closing the body released the merged context and unhooked the
	// invocation: a later invocation end has nothing left to release.
	g.Expect(requestContext.Err()).To(HaveOccurred())

	cancel()

	g.Eventually(func() error {
		return requestContext.Err()
	}).Should(HaveOccurred())
}

// TestInvocationCancellationAbortsOpenBodies pins that an open
// response body is cut off when the invocation ends, so legacy
// background-context calls cannot outlive the invocation.
func TestInvocationCancellationAbortsOpenBodies(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	invocation, cancel := context.WithCancel(t.Context())
	defer cancel()

	var requestContext context.Context
	transport := &bridgedTransport{
		invocation: invocation,
		base: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			requestContext = req.Context()

			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("payload")),
				Request:    req,
			}, nil
		}),
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/api", nil)
	g.Expect(err).To(Succeed())

	resp, err := transport.RoundTrip(req)
	g.Expect(err).To(Succeed())
	g.Expect(requestContext.Err()).NotTo(HaveOccurred())

	cancel()

	g.Eventually(func() error {
		return requestContext.Err()
	}).Should(HaveOccurred())
	g.Expect(resp.Body.Close()).To(Succeed())
}

// roundTripperFunc adapts a function to an http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestMergeRequestContext pins the deadline composition directly: the
// invocation bounds requests that carry no earlier deadline of their
// own, and never extends a shorter request deadline.
func TestMergeRequestContext(t *testing.T) {
	t.Parallel()

	now := time.Now()

	t.Run("request without deadline inherits the invocation deadline", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		invocation, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		invocationDeadline, _ := invocation.Deadline()

		merged, mergedCancel := mergeRequestContext(t.Context(), invocation)
		defer mergedCancel()

		deadline, has := merged.Deadline()
		g.Expect(has).To(BeTrue())
		g.Expect(deadline).To(BeTemporally("~", invocationDeadline, time.Second))
		g.Expect(deadline).To(BeTemporally(">", now.Add(25*time.Second)))
	})

	t.Run("earlier request deadline is preserved", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		invocation, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		request, requestCancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer requestCancel()

		merged, mergedCancel := mergeRequestContext(request, invocation)
		defer mergedCancel()

		deadline, has := merged.Deadline()
		g.Expect(has).To(BeTrue())
		g.Expect(deadline).To(BeTemporally("~", time.Now().Add(100*time.Millisecond), time.Second))
	})

	t.Run("later request deadline is capped to the invocation deadline", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		invocation, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()

		request, requestCancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer requestCancel()

		merged, mergedCancel := mergeRequestContext(request, invocation)
		defer mergedCancel()

		deadline, has := merged.Deadline()
		g.Expect(has).To(BeTrue())
		g.Expect(deadline).To(BeTemporally("~", time.Now().Add(100*time.Millisecond), time.Second))
	})

	t.Run("request stays cancellable without any deadline", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		merged, mergedCancel := mergeRequestContext(t.Context(), context.Background())
		defer mergedCancel()

		mergedCancel()
		g.Expect(merged.Err()).To(HaveOccurred())
	})
}
