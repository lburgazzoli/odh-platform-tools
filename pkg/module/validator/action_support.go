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

package validator

import (
	"context"
	stderrors "errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// The standard discovery client satisfies the narrow context-aware seam.
var _ ContextDiscovery = (*discovery.DiscoveryClient)(nil)

// ContextDiscovery is the narrow context-aware discovery seam the
// validator needs; the standard discovery client satisfies it.
type ContextDiscovery interface {
	// ServerResourcesForGroupVersionWithContext returns the resources one
	// group/version serves, bounded by ctx.
	ServerResourcesForGroupVersionWithContext(
		ctx context.Context,
		groupVersion string,
	) (*metav1.APIResourceList, error)
}

// DiscoveryFunc discovers the resources one group/version serves. It must
// be context-aware; a missing group/version is reported as a Kubernetes
// NotFound error, which is a finding, not an execution failure.
type DiscoveryFunc func(ctx context.Context, groupVersion string) (*metav1.APIResourceList, error)

// ObjectReader reads one object by explicit GVR. A missing object is
// reported as a Kubernetes NotFound error, which is a finding, not an
// execution failure.
type ObjectReader func(
	ctx context.Context,
	gvr schema.GroupVersionResource,
	namespace string,
	name string,
) (*unstructured.Unstructured, error)

// NewDiscoveryFunc adapts a standard discovery client to the narrow
// discovery seam. The returned function performs only the one
// context-aware group/version request.
func NewDiscoveryFunc(client ContextDiscovery) DiscoveryFunc {
	return func(ctx context.Context, groupVersion string) (*metav1.APIResourceList, error) {
		return client.ServerResourcesForGroupVersionWithContext(ctx, groupVersion)
	}
}

// NewObjectReader adapts a standard dynamic client to the narrow reader
// seam. The returned function performs only GET requests; an empty
// namespace addresses a cluster-scoped object.
func NewObjectReader(client dynamic.Interface) ObjectReader {
	return func(
		ctx context.Context,
		gvr schema.GroupVersionResource,
		namespace string,
		name string,
	) (*unstructured.Unstructured, error) {
		return client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
}

// deploymentGVR and configMapGVR are the fixed read targets beside the
// discovered module resource.
var (
	deploymentGVR = schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "deployments",
	}

	configMapGVR = schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "configmaps",
	}
)

// isContextCause reports whether err carries a context cancellation or
// deadline identity, which takes precedence over runtime categories.
func isContextCause(err error) bool {
	return err != nil &&
		(stderrors.Is(err, context.Canceled) || stderrors.Is(err, context.DeadlineExceeded))
}

// cycleError collects the first execution failure of one observation
// cycle. Context causes take precedence and are returned unwrapped so
// their identity stays discoverable; every other failure is classified
// as a validator runtime error.
type cycleError struct {
	contextCause error
	runtimeCause error
}

// record classifies one observation failure. A Kubernetes NotFound is not
// an execution failure and is never recorded.
func (e *cycleError) record(cause error, operation string) {
	switch {
	case cause == nil:
	case apierrors.IsNotFound(cause):
	case isContextCause(cause):
		if e.contextCause == nil {
			e.contextCause = cause
		}
	case e.runtimeCause == nil:
		e.runtimeCause = moduleerrors.New(
			moduleerrors.ErrValidateRuntime,
			"module validation failed to observe the cluster: "+operation+": "+cause.Error(),
			moduleerrors.WithCause(fmt.Errorf("%s: %w", operation, cause)),
		)
	}
}

// err returns the cycle's execution failure, with context identity first,
// or nil when the cycle observed everything it attempted.
func (e *cycleError) err() error {
	if e.contextCause != nil {
		return e.contextCause
	}

	return e.runtimeCause
}

// observation converts one read outcome, recording execution failures.
func (e *cycleError) observation(
	object *unstructured.Unstructured,
	err error,
	operation string,
) ObjectObservation {
	e.record(err, operation)

	switch {
	case err != nil && !apierrors.IsNotFound(err):
		return ObjectObservation{Err: err}
	case err != nil:
		return ObjectObservation{NotFound: true}
	default:
		return ObjectObservation{Object: object}
	}
}
