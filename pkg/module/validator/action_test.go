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
	"errors"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// fakeCluster scripts discovery and per-target read outcomes. Each target
// advances one step per cycle and repeats its last step when exhausted, so
// a scenario only names the transitions it cares about.
type fakeCluster struct {
	discoverySteps  []discoveryStep
	moduleSteps     []readStep
	deploymentSteps []readStep
	configMapSteps  []readStep

	discoveryCalls  int
	moduleCalls     int
	deploymentCalls int
	configMapCalls  int

	moduleReads []string
}

type discoveryStep struct {
	list *metav1.APIResourceList
	err  error
}

type readStep struct {
	object *unstructured.Unstructured
	err    error

	// hook runs before the step is returned; it lets a scenario cancel
	// the caller's context deterministically.
	hook func()
}

func notFound(gvr schema.GroupVersionResource, name string) error {
	return apierrors.NewNotFound(gvr.GroupResource(), name)
}

func forbidden(message string) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: "mockmodules"}, "mock",
		errors.New(message))
}

func (f *fakeCluster) discover(_ context.Context, _ string) (*metav1.APIResourceList, error) {
	defer func() { f.discoveryCalls++ }()

	step := f.discoveryStepAt()

	if step.err != nil {
		return nil, step.err
	}

	return step.list, nil
}

func (f *fakeCluster) discoveryStepAt() discoveryStep {
	if f.discoveryCalls >= len(f.discoverySteps) {
		return f.discoverySteps[len(f.discoverySteps)-1]
	}

	return f.discoverySteps[f.discoveryCalls]
}

func (f *fakeCluster) readStepAt(steps []readStep, call int) readStep {
	if call >= len(steps) {
		return steps[len(steps)-1]
	}

	return steps[call]
}

func (f *fakeCluster) read(_ context.Context, gvr schema.GroupVersionResource, namespace string, name string) (*unstructured.Unstructured, error) {
	step := readStep{}

	switch gvr.Resource {
	case "deployments":
		step = f.readStepAt(f.deploymentSteps, f.deploymentCalls)
		f.deploymentCalls++
	case "configmaps":
		step = f.readStepAt(f.configMapSteps, f.configMapCalls)
		f.configMapCalls++
	default:
		step = f.readStepAt(f.moduleSteps, f.moduleCalls)
		f.moduleCalls++
		f.moduleReads = append(f.moduleReads,
			fmt.Sprintf("%s/%s/%s", gvr.Resource, namespace, name))
	}

	if step.hook != nil {
		step.hook()
	}

	if step.err != nil {
		return nil, step.err
	}

	return step.object, nil
}

// readTargetError reports an error that is neither NotFound nor a context
// cause, for execution-failure scenarios.
func readTargetError() error {
	return forbidden("mockmodules is forbidden")
}

func discoveryStepError() error {
	return errors.New("connection refused")
}

func testDefinition() v1alpha1.PlatformModuleDefinition {
	definition := v1alpha1.PlatformModuleDefinition{}
	definition.Name = "mock"
	definition.Spec.Instance.Name = "mock"
	definition.Spec.Instance.Type = v1alpha1.ModuleType{
		Group:   "mock.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "MockModule",
	}
	definition.Spec.Controller.Deployment.Name = "mock-controller"

	return definition
}

// healthyCluster scripts an all-pass first cycle.
func healthyCluster() *fakeCluster {
	return &fakeCluster{
		discoverySteps: []discoveryStep{
			{list: &metav1.APIResourceList{APIResources: testDiscovery(false, true).Resources}},
		},
		moduleSteps:     []readStep{{object: healthyModuleCR()}},
		deploymentSteps: []readStep{{object: healthyDeployment()}},
		configMapSteps:  []readStep{{object: healthyConfigMap()}},
	}
}

// unreadyModuleCR returns a healthy CR with Ready=False.
func unreadyModuleCR() *unstructured.Unstructured {
	cr := healthyModuleCR()
	status := cr.Object["status"].(map[string]any)
	status["conditions"] = []any{
		map[string]any{"type": "Ready", "status": "False"},
		map[string]any{"type": "ProvisioningSucceeded", "status": "True"},
	}

	return cr
}

// terminatingModuleCR stamps a deletion timestamp on the healthy CR.
func terminatingModuleCR() *unstructured.Unstructured {
	cr := healthyModuleCR()
	cr.Object["metadata"].(map[string]any)["deletionTimestamp"] = "2026-01-02T03:04:05Z"

	return cr
}

// mismatchedConfigMap returns the healthy ConfigMap with a different
// platform version than the CR reports.
func mismatchedConfigMap() *unstructured.Unstructured {
	cm := healthyConfigMap()
	cm.Object["data"].(map[string]any)["platformVersion"] = "9.9.9"

	return cm
}

func testService(cluster *fakeCluster, interval time.Duration) *Service {
	return NewService(cluster.discover, cluster.read, WithPollInterval(interval))
}

// TestValidateImmediateSuccess checks that an all-pass first cycle returns
// without waiting or re-observing.
func TestValidateImmediateSuccess(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()

	result, err := testService(cluster, time.Minute).Validate(
		context.Background(), testDefinition(), testNamespace)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Passed()).To(BeTrue())
	g.Expect(result.Namespace).To(Equal(testNamespace))

	// Exactly one complete cycle: no extra discovery or reads.
	g.Expect(cluster.discoveryCalls).To(Equal(1))
	g.Expect(cluster.moduleCalls).To(Equal(1))
	g.Expect(cluster.deploymentCalls).To(Equal(1))
	g.Expect(cluster.configMapCalls).To(Equal(1))

	// The CR read used the discovered plural and no namespace.
	g.Expect(cluster.moduleReads).To(Equal([]string{"mockmodules//mock"}))
}

// TestValidateConvergence checks that transitional targets are re-observed
// without a user wait flag until every check passes in one cycle.
func TestValidateConvergence(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()
	cluster.moduleSteps = []readStep{
		{object: unreadyModuleCR()},
		{object: healthyModuleCR()},
	}
	cluster.configMapSteps = []readStep{
		{object: mismatchedConfigMap()},
		{object: healthyConfigMap()},
	}

	result, err := testService(cluster, time.Millisecond).Validate(
		context.Background(), testDefinition(), testNamespace)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Passed()).To(BeTrue())
	g.Expect(cluster.moduleCalls).To(Equal(2))
	g.Expect(cluster.configMapCalls).To(Equal(2))
}

// TestValidateMissingAPIThenDiscovered checks that a missing group/version
// is rediscovered on later cycles and can converge.
func TestValidateMissingAPIThenDiscovered(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()
	cluster.discoverySteps = []discoveryStep{
		{err: notFound(schema.GroupVersionResource{Group: "mock.opendatahub.io", Resource: "mockmodules"}, "")},
		{list: &metav1.APIResourceList{APIResources: testDiscovery(false, true).Resources}},
	}
	cluster.moduleSteps = []readStep{
		{err: notFound(schema.GroupVersionResource{
			Group: "mock.opendatahub.io", Version: "v1alpha1", Resource: "mockmodules",
		}, "mock")},
		{object: healthyModuleCR()},
	}

	result, err := testService(cluster, time.Millisecond).Validate(
		context.Background(), testDefinition(), testNamespace)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Passed()).To(BeTrue())

	// Cycle one found no API and read nothing; cycle two rediscovered the
	// API but the CR was still absent; cycle three read the created CR.
	g.Expect(cluster.discoveryCalls).To(Equal(3))
	g.Expect(cluster.moduleCalls).To(Equal(2))
}

// TestValidateNamespacedMappingNeverReadsCR checks that a namespaced
// mapping fails the API check without ever reading a namespaced instance,
// while independent checks keep observing.
func TestValidateNamespacedMappingNeverReadsCR(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()
	cluster.discoverySteps = []discoveryStep{
		{list: &metav1.APIResourceList{APIResources: testDiscovery(true, true).Resources}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	result, err := testService(cluster, time.Millisecond).Validate(ctx, testDefinition(), testNamespace)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(cluster.moduleCalls).To(Equal(0))
	g.Expect(cluster.deploymentCalls).To(BeNumerically(">", 0))
	g.Expect(cluster.configMapCalls).To(BeNumerically(">", 0))
	g.Expect(check(result, CheckModuleAPI).Status).To(Equal(StatusFail))
	g.Expect(check(result, CheckModuleExists).Status).To(Equal(StatusSkipped))
	g.Expect(check(result, CheckDeployment).Status).To(Equal(StatusPass))
}

// TestValidateDeadline checks the caller-deadline outcome: the latest
// cycle's findings are retained and success is never reported.
func TestValidateDeadline(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()
	cluster.moduleSteps = []readStep{{err: notFound(schema.GroupVersionResource{
		Group: "mock.opendatahub.io", Version: "v1alpha1", Resource: "mockmodules",
	}, "mock")}}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	result, err := testService(cluster, time.Millisecond).Validate(ctx, testDefinition(), testNamespace)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(result.Passed()).To(BeFalse())

	// The report retains the latest cycle's complete observation.
	g.Expect(result.Checks).To(HaveLen(7))
	g.Expect(check(result, CheckModuleExists).Status).To(Equal(StatusFail))
	g.Expect(check(result, CheckModuleReady).Status).To(Equal(StatusSkipped))
	g.Expect(check(result, CheckDeployment).Status).To(Equal(StatusPass))
	g.Expect(check(result, CheckPlatformConsumed).Status).To(Equal(StatusSkipped))
}

// TestValidateCancellation checks prompt termination with the cancellation
// identity preserved.
func TestValidateCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()

	ctx, cancel := context.WithCancel(context.Background())

	// The second module read cancels the caller's context, so the wait
	// between cycles terminates promptly.
	cluster.moduleSteps = []readStep{
		{object: unreadyModuleCR()},
		{object: unreadyModuleCR(), hook: cancel},
	}

	result, err := testService(cluster, time.Millisecond).Validate(ctx, testDefinition(), testNamespace)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	g.Expect(result.Passed()).To(BeFalse())
	g.Expect(check(result, CheckModuleReady).Status).To(Equal(StatusFail))
}

// TestValidateAlreadyExpiredContext checks the pre-flight deadline check.
func TestValidateAlreadyExpiredContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := testService(cluster, time.Millisecond).Validate(ctx, testDefinition(), testNamespace)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	g.Expect(cluster.discoveryCalls).To(Equal(0))
	g.Expect(result.Namespace).To(Equal(testNamespace))
}

// TestValidateReadErrorReturnsPromptly checks that a denied read is an
// execution failure, not an absence: independent checks are collected,
// then the error returns without waiting out the budget.
func TestValidateReadErrorReturnsPromptly(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()
	cluster.moduleSteps = []readStep{{err: readTargetError()}}

	started := time.Now()

	result, err := testService(cluster, time.Minute).Validate(
		context.Background(), testDefinition(), testNamespace)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrValidateRuntime)).To(BeTrue())
	g.Expect(time.Since(started)).To(BeNumerically("<", 10*time.Second))

	// The partial cycle still reports every independent observation.
	g.Expect(check(result, CheckModuleExists).Status).To(Equal(StatusError))
	g.Expect(check(result, CheckDeployment).Status).To(Equal(StatusPass))
	g.Expect(check(result, CheckPlatformConfig).Status).To(Equal(StatusPass))
	g.Expect(check(result, CheckPlatformConsumed).Status).To(Equal(StatusSkipped))

	// One cycle only; the error did not wait or retry.
	g.Expect(cluster.discoveryCalls).To(Equal(1))
}

// TestValidateDiscoveryError checks the discovery execution failure: the
// module branch errors, independent checks are still collected.
func TestValidateDiscoveryError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()
	cluster.discoverySteps = []discoveryStep{{err: discoveryStepError()}}

	result, err := testService(cluster, time.Minute).Validate(
		context.Background(), testDefinition(), testNamespace)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrValidateRuntime)).To(BeTrue())
	g.Expect(check(result, CheckModuleAPI).Status).To(Equal(StatusError))
	g.Expect(check(result, CheckModuleExists).Status).To(Equal(StatusSkipped))
	g.Expect(check(result, CheckDeployment).Status).To(Equal(StatusPass))
	g.Expect(cluster.moduleCalls).To(Equal(0))
}

// TestValidateFreshObservations checks that a formerly passing config
// observation cannot survive a later failed read: reports contain the
// latest cycle only.
func TestValidateFreshObservations(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()
	cluster.moduleSteps = []readStep{
		{object: unreadyModuleCR()},
		{object: unreadyModuleCR()},
	}
	cluster.configMapSteps = []readStep{
		{object: healthyConfigMap()},
		{err: readTargetError()},
	}

	result, err := testService(cluster, time.Millisecond).Validate(
		context.Background(), testDefinition(), testNamespace)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrValidateRuntime)).To(BeTrue())

	// The second cycle's failed read replaced the first cycle's pass.
	g.Expect(check(result, CheckPlatformConfig).Status).To(Equal(StatusError))
	g.Expect(check(result, CheckPlatformConsumed).Status).To(Equal(StatusSkipped))
	g.Expect(result.Passed()).To(BeFalse())
}

// TestValidateTerminatingObject checks that a replaced or terminating CR
// keeps failing existence and never combines cycles into success.
func TestValidateTerminatingObject(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cluster := healthyCluster()
	cluster.moduleSteps = []readStep{
		{object: unreadyModuleCR()},
		{object: terminatingModuleCR()},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	result, err := testService(cluster, time.Millisecond).Validate(ctx, testDefinition(), testNamespace)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(check(result, CheckModuleExists).Status).To(Equal(StatusFail))
	g.Expect(result.Passed()).To(BeFalse())
}

// TestValidateInvalidSelectors checks that invalid definitions fail before
// any cluster access.
func TestValidateInvalidSelectors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := testDefinition()
	definition.Spec.Instance.Name = ""

	cluster := healthyCluster()

	result, err := testService(cluster, time.Millisecond).Validate(
		context.Background(), definition, testNamespace)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	g.Expect(cluster.discoveryCalls).To(Equal(0))
	g.Expect(result.Namespace).To(Equal(testNamespace))
}

// stubContextDiscovery records the context-aware discovery seam's inputs.
type stubContextDiscovery struct {
	ctx          context.Context
	groupVersion string
	list         *metav1.APIResourceList
	err          error
}

func (s *stubContextDiscovery) ServerResourcesForGroupVersionWithContext(
	ctx context.Context,
	groupVersion string,
) (*metav1.APIResourceList, error) {
	s.ctx = ctx
	s.groupVersion = groupVersion

	return s.list, s.err
}

// TestNewDiscoveryFunc checks that the adapter passes the caller's context
// and group/version through unchanged.
func TestNewDiscoveryFunc(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := &stubContextDiscovery{
		list: &metav1.APIResourceList{APIResources: testDiscovery(false, true).Resources},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	list, err := NewDiscoveryFunc(stub)(ctx, "mock.opendatahub.io/v1alpha1")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(list).To(Equal(stub.list))
	g.Expect(stub.ctx).To(Equal(ctx))
	g.Expect(stub.groupVersion).To(Equal("mock.opendatahub.io/v1alpha1"))
}

// TestNewObjectReaderUsesOnlyGET checks that the production reader
// performs only GET operations over the standard dynamic client.
func TestNewObjectReaderUsesOnlyGET(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(), map[schema.GroupVersionResource]string{})

	var verbs []string

	client.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		verbs = append(verbs, action.GetVerb())

		return false, nil, nil
	})

	reader := NewObjectReader(client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := reader(ctx, deploymentGVR, testNamespace, "mock-controller")

	// A missing object is an expected outcome here; the read-only verb
	// record is the assertion target.
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

	for i := range verbs {
		g.Expect(verbs[i]).To(Equal("get"))
	}

	g.Expect(verbs).NotTo(BeEmpty())
}
