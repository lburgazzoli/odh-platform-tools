package cluster

import (
	"context"
	"io"
	"net/http"
	"sync"

	"k8s.io/client-go/rest"
)

// bridgeTransport installs the invocation-scoped request bridge on a
// copied REST configuration. The bridge composes with any transport
// wrapper the standard configuration already carries — exec auth and
// TLS setup happen at the rest client layer and are untouched — so
// every client built from the configuration issues bounded requests.
func bridgeTransport(invocation context.Context, config *rest.Config) {
	base := config.WrapTransport

	config.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		if base != nil {
			rt = base(rt)
		}

		return &bridgedTransport{invocation: invocation, base: rt}
	}
}

// bridgedTransport bounds every request to the invocation context.
// Legacy discovery and mapper calls issue requests with no request
// context of their own; through this bridge those requests still
// honor the invocation's cancellation and deadline, and a request
// whose own context carries an earlier deadline keeps it.
//
// The merged context must outlive RoundTrip itself — HTTP response
// bodies stay bound to the request context until closed — so the
// derived context is released when the body is closed, or when the
// invocation ends, whichever comes first. No goroutine outlives the
// invocation: the cancellation hook is a context.AfterFunc that is
// unregistered on body close.
type bridgedTransport struct {
	invocation context.Context
	base       http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *bridgedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.invocation.Err(); err != nil {
		return nil, err
	}

	merged, cancel := mergeRequestContext(req.Context(), t.invocation)
	req = req.WithContext(merged)

	// Cancel the merged context when the invocation ends. For requests
	// whose own context is already a child of the invocation this is a
	// harmless safety net; for background-context legacy calls it is
	// the only cancellation path.
	stop := context.AfterFunc(t.invocation, cancel)

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		stop()
		cancel()

		return nil, err
	}

	resp.Body = &boundedBody{ReadCloser: resp.Body, stop: stop, cancel: cancel}

	return resp, nil
}

// mergeRequestContext derives a child of the request context that
// also honors the invocation: an earlier deadline always wins, so a
// shorter request or stage context is never extended. The returned
// cancel function releases the derived context.
func mergeRequestContext(request, invocation context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := invocation.Deadline(); ok {
		if current, has := request.Deadline(); !has || deadline.Before(current) {
			return context.WithDeadline(request, deadline)
		}
	}

	return context.WithCancel(request)
}

// boundedBody releases the bridged request context when the response
// body is closed, unregistering the invocation hook so a connection
// that finishes normally leaves nothing behind.
type boundedBody struct {
	io.ReadCloser

	stop   func() bool
	cancel context.CancelFunc
	once   sync.Once
}

// Close implements io.Closer.
func (b *boundedBody) Close() error {
	b.once.Do(func() {
		b.stop()
		b.cancel()
	})

	return b.ReadCloser.Close()
}
