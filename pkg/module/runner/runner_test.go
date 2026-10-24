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

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/yaml"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"

	moduleowner "github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/owner"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/helm"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/kustomize"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
)

// runnerKubeconfigYAML is a standalone kubeconfig whose context
// carries an explicit namespace, pointing at an unreachable server:
// namespace resolution is local preparation and never contacts it.
const runnerKubeconfigYAML = `apiVersion: v1
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
    namespace: run-namespace
current-context: test-context
users:
- name: test-user
  user: {}
`

// renderTargetDeploymentYAML is the controller Deployment the
// captured injector post-renderers run over: its name and container
// match the test definition's controller descriptor, and it carries
// one pre-existing env entry the injected entries must preserve.
const renderTargetDeploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  namespace: run-namespace
spec:
  template:
    spec:
      containers:
      - name: manager
        image: quay.io/example/chart-default:v1
        env:
        - name: LOG_LEVEL
          value: debug
`

// composeDefinitionYAML is a Helm-format definition whose main
// container image is a whole-token reference.
const composeDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    type:
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
  manifests:
    format: helm
    helm:
      path: ./manifests/chart
  relatedImages:
  - RELATED_IMAGE_ALPHA
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: "{{RELATED_IMAGE_CONTROLLER}}"
`

// composeChartYAML is the chart descriptor of the composition
// fixture.
const composeChartYAML = `apiVersion: v2
name: synthetic
version: 0.1.0
`

// composeValuesYAML is the chart's default values.
const composeValuesYAML = `replicas: 1
`

// composeTemplateYAML renders the controller Deployment with a
// chart-default image and a literal extra env value.
const composeTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  namespace: "{{ .Release.Namespace }}"
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      containers:
      - name: manager
        image: quay.io/example/chart-default:v1
        env:
        - name: LOG_LEVEL
          value: debug
`

// testGetter returns standard ConfigFlags bound to a kubeconfig whose
// context namespace is run-namespace.
func testGetter(t *testing.T) genericclioptions.RESTClientGetter {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(runnerKubeconfigYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path

	return flags
}

// testOptions returns valid invocation settings.
func testOptions() Options {
	return Options{
		DefinitionFile:  "definition.yaml",
		PlatformVersion: "2.19.0",
		Timeout:         5 * time.Minute,
	}
}

// testDefinition returns a typed Helm-format definition whose main
// container image is a whole-token reference.
func testDefinition() v1alpha1.PlatformModuleDefinition {
	return v1alpha1.PlatformModuleDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic"},
		Spec: v1alpha1.PlatformModuleDefinitionSpec{
			Instance: v1alpha1.Instance{
				Type: v1alpha1.ModuleType{
					Group:   "components.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "SyntheticModule",
				},
			},
			Manifests: v1alpha1.Manifests{
				Format: v1alpha1.FormatHelm,
				Helm:   &v1alpha1.HelmManifests{Path: "./manifests/chart"},
			},
			RelatedImages: []string{"RELATED_IMAGE_ALPHA"},
			Controller: v1alpha1.Controller{
				Deployment: v1alpha1.ControllerDeployment{
					Name: "synthetic-controller",
					Container: &v1alpha1.ContainerDefinition{
						Name:  "manager",
						Image: "{{RELATED_IMAGE_CONTROLLER}}",
					},
				},
			},
		},
	}
}

// testConnection is a non-nil connection placeholder the stubs pass
// around; no request is ever issued through it.
func testConnection() *cluster.Connection {
	return cluster.NewWithClient(nil)
}

// stubRunner wires a Runner whose service seams are recording fakes
// around one fixed loaded definition. Every seam appends its name to
// the shared call log; nil function fields keep the default recorded
// success behavior.
type stubRunner struct {
	runner *Runner
	calls  *[]string

	definition v1alpha1.PlatformModuleDefinition

	// renderOptions captures the shared options Render passed.
	renderOptions *renderer.Options

	// loadError, renderError, connectError, ensureOwner, ensureError,
	// and applyError drive the stub behavior; appliedOwner,
	// appliedRelease, and appliedResources capture what apply
	// received.
	loadError        error
	renderError      error
	connectError     error
	ensureOwner      *v1alpha1.PlatformModuleDefinition
	ensureError      error
	applyError       error
	appliedOwner     *v1alpha1.PlatformModuleDefinition
	appliedRelease   api.Release
	appliedResources []unstructured.Unstructured

	// renderCtx, connectCtx, and ensureCtx capture the invocation
	// contexts the stages passed to the seams.
	renderCtx  context.Context
	connectCtx context.Context
	ensureCtx  context.Context

	// renderResult, when set, replaces the render stub's default
	// payload; the wait tests select targets out of it.
	renderResult []unstructured.Unstructured

	// connection, when set, replaces the connect stub's placeholder
	// connection; the wait tests drive polling through a fake
	// client.
	connection *cluster.Connection

	// renderHook, connectHook, and ensureHook, when set, run inside
	// the matching stub with the invocation context before it
	// returns; the run tests use them to cancel the invocation at
	// controlled points.
	renderHook  func(ctx context.Context)
	connectHook func(ctx context.Context)
	ensureHook  func(ctx context.Context)
}

// newStubRunner returns the stub harness with the test definition.
func newStubRunner(t *testing.T, options Options) *stubRunner {
	t.Helper()

	stub := &stubRunner{
		calls:       &[]string{},
		definition:  testDefinition(),
		ensureOwner: &v1alpha1.PlatformModuleDefinition{},
	}

	stub.runner = New(
		options,
		testGetter(t),
		stub.load,
		stub.render,
		stub.render,
		stub.connect,
		stub.ensure,
		stub.apply,
	)

	return stub
}

// record appends one call to the shared log.
func (s *stubRunner) record(name string) {
	*s.calls = append(*s.calls, name)
}

// load is the loader stub.
func (s *stubRunner) load(_ context.Context, _ string) (v1alpha1.PlatformModuleDefinition, error) {
	s.record("load")

	return s.definition, s.loadError
}

// render is the backend render stub; it captures the shared options
// and the invocation context, runs the optional hook, and returns
// one empty resource so Deploy has a payload.
func (s *stubRunner) render(
	ctx context.Context,
	_ v1alpha1.PlatformModuleDefinition,
	_ string,
	options renderer.Options,
) ([]unstructured.Unstructured, error) {
	s.record("render")

	s.renderCtx = ctx

	captured := options
	s.renderOptions = &captured

	if s.renderHook != nil {
		s.renderHook(ctx)
	}

	if s.renderError != nil {
		return nil, s.renderError
	}

	if s.renderResult != nil {
		return s.renderResult, nil
	}

	return []unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "payload", "namespace": "run-namespace"},
	}}}, nil
}

// connect is the connection stub.
func (s *stubRunner) connect(
	ctx context.Context,
	_ genericclioptions.RESTClientGetter,
) (*cluster.Connection, error) {
	s.record("connect")

	s.connectCtx = ctx

	if s.connectHook != nil {
		s.connectHook(ctx)
	}

	if s.connection != nil {
		return s.connection, nil
	}

	return testConnection(), s.connectError
}

// ensure is the owner publication stub. It mirrors the real
// service's contract: an error is always accompanied by a nil owner,
// with any still-confirmed identity carried inside the typed error.
func (s *stubRunner) ensure(
	ctx context.Context,
	_ *cluster.Connection,
	_ v1alpha1.PlatformModuleDefinition,
) (*v1alpha1.PlatformModuleDefinition, error) {
	s.record("ensure")

	s.ensureCtx = ctx

	if s.ensureHook != nil {
		s.ensureHook(ctx)
	}

	if s.ensureError != nil {
		return nil, s.ensureError
	}

	return s.ensureOwner, nil
}

// apply is the payload apply stub.
func (s *stubRunner) apply(
	_ context.Context,
	_ *cluster.Connection,
	owner *v1alpha1.PlatformModuleDefinition,
	release api.Release,
	resources []unstructured.Unstructured,
) error {
	s.record("apply")

	s.appliedOwner = owner
	s.appliedRelease = release
	s.appliedResources = resources

	return s.applyError
}

// TestLoadRejectsInvalidOptions proves invalid settings fail Load as
// options errors before any service call, and the invocation is
// terminal: later stages reject without input or output.
func TestLoadRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		options func() Options
	}{
		{"missing definition file", func() Options {
			options := testOptions()
			options.DefinitionFile = ""
			return options
		}},
		{"missing platform version", func() Options {
			options := testOptions()
			options.PlatformVersion = ""
			return options
		}},
		{"zero timeout", func() Options {
			options := testOptions()
			options.Timeout = 0
			return options
		}},
		{"negative timeout", func() Options {
			options := testOptions()
			options.Timeout = -time.Minute
			return options
		}},
		{"unparsable platform version", func() Options {
			options := testOptions()
			options.PlatformVersion = "not-a-version"
			return options
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			stub := newStubRunner(t, testCase.options())

			err := stub.runner.Load(t.Context())
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, ErrInvalidOptions)).To(BeTrue())

			// No service ran, and the failed invocation is terminal.
			g.Expect(*stub.calls).To(BeEmpty())
			g.Expect(stub.runner.Render(t.Context())).NotTo(Succeed())
			g.Expect(stub.runner.Deploy(t.Context())).NotTo(Succeed())
			g.Expect(errors.Is(stub.runner.Render(t.Context()), ErrStageSequence)).To(BeTrue())
		})
	}
}

// TestLoadSnapshotsImagesAndResolvesNamespace proves Load parses the
// platform version once into the canonical release, snapshots exactly
// the declared related keys plus the main image token, and resolves
// the effective namespace locally with the applications namespace and
// platform type defaults applied. The image sources come from the
// process environment, so this test cannot run in parallel.
func TestLoadSnapshotsImagesAndResolvesNamespace(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("RELATED_IMAGE_ALPHA", "quay.io/example/alpha:v1")
	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/example/controller:v2")
	t.Setenv("UNREQUESTED_IMAGE", "quay.io/example/unrequested:v1")

	stub := newStubRunner(t, testOptions())

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())

	g.Expect(*stub.calls).To(Equal([]string{"load"}))
	g.Expect(stub.runner.images).To(Equal(map[string]string{
		"RELATED_IMAGE_ALPHA":      "quay.io/example/alpha:v1",
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v2",
	}))

	g.Expect(stub.runner.namespace).To(Equal("run-namespace"))
	g.Expect(stub.runner.applicationsNamespace).To(Equal("run-namespace"))
	g.Expect(stub.runner.platformType).To(Equal(DefaultPlatformType))
	g.Expect(stub.runner.release.Name).To(Equal(api.Platform(api.ReleasePlatform)))
	g.Expect(stub.runner.release.Version.String()).To(Equal("2.19.0"))
}

// TestLoadAcceptsNoncanonicalVersion proves a noncanonical but
// parsable platform version produces one canonical string shared by
// the injector and deploy stages.
func TestLoadAcceptsNoncanonicalVersion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := testOptions()
	options.PlatformVersion = "v2.19.0+dev"

	stub := newStubRunner(t, options)

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())
	g.Expect(stub.runner.release.Version.String()).To(Equal("2.19.0+dev"))
}

// TestLoadApplicationsNamespaceOverride proves an explicit
// applications namespace is kept while the effective installation
// namespace stays the standard resolution.
func TestLoadApplicationsNamespaceOverride(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := testOptions()
	options.ApplicationsNamespace = "applications"
	options.MonitoringNamespace = "monitoring"

	stub := newStubRunner(t, options)

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())

	g.Expect(stub.runner.namespace).To(Equal("run-namespace"))
	g.Expect(stub.runner.applicationsNamespace).To(Equal("applications"))
}

// TestStageSequence proves stages run exactly in Load → Render →
// Deploy order: out-of-order, repeated, and post-success calls reject
// with the sequence sentinel and no service call.
func TestStageSequence(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())

	// Render and Deploy before Load reject without any service call.
	g.Expect(errors.Is(stub.runner.Render(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(errors.Is(stub.runner.Deploy(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(*stub.calls).To(BeEmpty())

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())

	// Deploy before Render, and a repeated Load, reject the same way.
	g.Expect(errors.Is(stub.runner.Deploy(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(errors.Is(stub.runner.Load(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(*stub.calls).To(Equal([]string{"load"}))

	g.Expect(stub.runner.Render(t.Context())).To(Succeed())

	g.Expect(errors.Is(stub.runner.Render(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(*stub.calls).To(Equal([]string{"load", "render"}))

	g.Expect(stub.runner.Deploy(t.Context())).To(Succeed())

	// A successful invocation accepts no further stage.
	g.Expect(errors.Is(stub.runner.Deploy(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(errors.Is(stub.runner.Load(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "ensure", "apply"}))
}

// TestFailedStageIsTerminal proves a stage failure makes the
// invocation terminal: no later stage call performs input or output.
func TestFailedStageIsTerminal(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())
	stub.loadError = errors.New("definition failure")

	g.Expect(stub.runner.Load(t.Context())).NotTo(Succeed())

	g.Expect(errors.Is(stub.runner.Load(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(errors.Is(stub.runner.Render(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(errors.Is(stub.runner.Deploy(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(*stub.calls).To(Equal([]string{"load"}))
}

// TestLoadAndRenderAreClientFree proves the local stages never
// connect: after a successful Load and Render no connection,
// publication, or apply happened, and the render seam received the
// applications namespace with both injector post-renderers.
func TestLoadAndRenderAreClientFree(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())
	g.Expect(stub.runner.Render(t.Context())).To(Succeed())

	g.Expect(*stub.calls).To(Equal([]string{"load", "render"}))
	g.Expect(stub.renderOptions.Namespace).To(Equal("run-namespace"))
	g.Expect(stub.renderOptions.PostRenderers).To(HaveLen(2))
}

// TestRenderFailureStopsBeforeConnection proves a render error is
// terminal and never reaches the connection stage.
func TestRenderFailureStopsBeforeConnection(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())

	stub.renderError = errors.New("render failure")

	g.Expect(stub.runner.Render(t.Context())).To(HaveOccurred())
	g.Expect(errors.Is(stub.runner.Render(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(*stub.calls).To(Equal([]string{"load", "render"}))
	g.Expect(stub.runner.Deploy(t.Context())).NotTo(Succeed())
	g.Expect(*stub.calls).To(Equal([]string{"load", "render"}))
}

// TestRenderWiresInjectors proves the injector post-renderers Render
// registers are the operator's transformations bound to the loaded
// definition and snapshot: running them over a controller Deployment
// injects the resolved image, the related images, and the platform
// env, and appends the platform config ConfigMap with the canonical
// version.
func TestRenderWiresInjectors(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("RELATED_IMAGE_ALPHA", "quay.io/example/alpha:v1")
	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/example/controller:v2")

	options := testOptions()
	options.PlatformVersion = "v2.19.0+dev"
	options.MonitoringNamespace = "monitoring"

	stub := newStubRunner(t, options)

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())
	g.Expect(stub.runner.Render(t.Context())).To(Succeed())

	deployment := unstructured.Unstructured{}
	g.Expect(yaml.Unmarshal([]byte(renderTargetDeploymentYAML), &deployment)).To(Succeed())

	enriched := []unstructured.Unstructured{deployment}
	for _, postRenderer := range stub.renderOptions.PostRenderers {
		var err error
		enriched, err = postRenderer(t.Context(), enriched)
		g.Expect(err).NotTo(HaveOccurred())
	}

	g.Expect(enriched).To(HaveLen(2))

	rendered := enriched[0].Object
	g.Expect(rendered).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[0].image == "quay.io/example/controller:v2"`),
		jq.Match(`.spec.template.spec.containers[0].env[0].name == "LOG_LEVEL"`),
		jq.Match(`.spec.template.spec.containers[0].env[0].value == "debug"`),
		jq.Match(`.spec.template.spec.containers[0].env[1].name == "RELATED_IMAGE_ALPHA"`),
		jq.Match(`.spec.template.spec.containers[0].env[1].value == "quay.io/example/alpha:v1"`),
		jq.Match(`.spec.template.spec.containers[0].env[2].name == "APPLICATIONS_NAMESPACE"`),
		jq.Match(`.spec.template.spec.containers[0].env[2].value == "run-namespace"`),
		jq.Match(`.spec.template.spec.containers[0].env[3].name == "MONITORING_NAMESPACE"`),
		jq.Match(`.spec.template.spec.containers[0].env[3].value == "monitoring"`),
		jq.Match(`.spec.template.spec.containers[0].env[4].name == "ODH_MODULE_OPERATOR_PLATFORM_TYPE"`),
		jq.Match(`.spec.template.spec.containers[0].env[4].value == "OpenDataHub"`),
		jq.Match(`.spec.template.spec.containers[0].env | length == 5`),
	))

	g.Expect(enriched[1].Object).To(SatisfyAll(
		jq.Match(`.kind == "ConfigMap"`),
		jq.Match(`.metadata.name == "odh-synthetic-config"`),
		jq.Match(`.metadata.namespace == "run-namespace"`),
		jq.Match(`.data.platformVersion == "2.19.0+dev"`),
	))
}

// TestDeploySequencesConnectEnsureApply proves Deploy runs the three
// remote steps in order — connect, owner publication, payload apply —
// and the apply receives the confirmed owner, the same release the
// injector used, and the whole rendered payload.
func TestDeploySequencesConnectEnsureApply(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	confirmed := &v1alpha1.PlatformModuleDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic", UID: "definition-uid"},
	}

	stub := newStubRunner(t, testOptions())
	stub.ensureOwner = confirmed

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())
	g.Expect(stub.runner.Render(t.Context())).To(Succeed())
	g.Expect(stub.runner.Deploy(t.Context())).To(Succeed())

	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "ensure", "apply"}))
	g.Expect(stub.runner.stage).To(Equal(stageDeployed))
	g.Expect(stub.runner.applied).To(BeTrue())
	g.Expect(stub.appliedOwner).To(BeIdenticalTo(confirmed))
	g.Expect(stub.appliedRelease.Version.String()).To(Equal("2.19.0"))
	g.Expect(stub.appliedRelease.Name).To(Equal(api.Platform(api.ReleasePlatform)))
	g.Expect(stub.runner.owner).To(BeIdenticalTo(confirmed))
	g.Expect(stub.runner.publication).To(Equal(moduleowner.OutcomeConfirmed))
}

// TestDeployRetainsOwnerOnEnsureFailure proves an owner-publication
// failure stops before any payload write while keeping whatever
// identity and publication outcome it still confirmed: a nil owner
// with an unknown outcome asserts an ambiguous write, not that none
// occurred.
func TestDeployRetainsOwnerOnEnsureFailure(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	confirmed := &v1alpha1.PlatformModuleDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic", UID: "definition-uid"},
	}

	stub := newStubRunner(t, testOptions())
	stub.ensureError = &moduleowner.Error{
		Outcome: moduleowner.OutcomeUnknown,
		Owner:   confirmed,
	}

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())
	g.Expect(stub.runner.Render(t.Context())).To(Succeed())

	err := stub.runner.Deploy(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, ErrStageSequence)).To(BeFalse())

	// No payload apply ran.
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "ensure"}))
	g.Expect(stub.appliedOwner).To(BeNil())

	// The confirmed identity and ambiguous outcome survive.
	g.Expect(stub.runner.owner).To(BeIdenticalTo(confirmed))
	g.Expect(stub.runner.publication).To(Equal(moduleowner.OutcomeUnknown))
	g.Expect(stub.runner.applied).To(BeFalse())

	// The failed invocation is terminal.
	g.Expect(errors.Is(stub.runner.Deploy(t.Context()), ErrStageSequence)).To(BeTrue())
}

// TestDeployRetainsNilOwnerOnUnknownOutcome proves a lost owner
// publication with no confirmed identity is retained as exactly
// that: no owner, an unknown outcome, and no payload write.
func TestDeployRetainsNilOwnerOnUnknownOutcome(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())
	stub.ensureOwner = nil
	stub.ensureError = &moduleowner.Error{Outcome: moduleowner.OutcomeUnknown}

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())
	g.Expect(stub.runner.Render(t.Context())).To(Succeed())

	g.Expect(stub.runner.Deploy(t.Context())).To(HaveOccurred())

	g.Expect(stub.runner.owner).To(BeNil())
	g.Expect(stub.runner.publication).To(Equal(moduleowner.OutcomeUnknown))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "ensure"}))
}

// TestDeployConnectFailureIsTerminal proves a connection failure
// stops the stage before any publication or write.
func TestDeployConnectFailureIsTerminal(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())
	stub.connectError = errors.New("connection failure")

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())
	g.Expect(stub.runner.Render(t.Context())).To(Succeed())

	g.Expect(stub.runner.Deploy(t.Context())).To(HaveOccurred())
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect"}))
	g.Expect(stub.runner.owner).To(BeNil())
}

// TestDeployApplyFailureRetainsOwner proves a payload apply failure
// keeps the published owner and confirmed publication while marking
// the apply incomplete.
func TestDeployApplyFailureRetainsOwner(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	confirmed := &v1alpha1.PlatformModuleDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic", UID: "definition-uid"},
	}

	stub := newStubRunner(t, testOptions())
	stub.ensureOwner = confirmed
	stub.applyError = errors.New("apply failure")

	g.Expect(stub.runner.Load(t.Context())).To(Succeed())
	g.Expect(stub.runner.Render(t.Context())).To(Succeed())

	g.Expect(stub.runner.Deploy(t.Context())).To(HaveOccurred())

	g.Expect(stub.runner.owner).To(BeIdenticalTo(confirmed))
	g.Expect(stub.runner.publication).To(Equal(moduleowner.OutcomeConfirmed))
	g.Expect(stub.runner.applied).To(BeFalse())
}

// writeFile writes one fixture file under root, failing the test on
// any error.
func writeFile(t *testing.T, root, name, content string) {
	t.Helper()

	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// findResource returns the first resource of the payload with the
// given kind and name, or nil.
func findResource(resources []unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	for i := range resources {
		if resources[i].GetKind() == kind && resources[i].GetName() == name {
			return &resources[i]
		}
	}

	return nil
}

// TestLoadRenderComposeRealServicesOffline proves the staged local
// chain through the real loader and the real Helm backend with stub
// remote seams: a chart on disk renders through the shared engine
// into the applications namespace with the injectors applied — the
// resolved controller image, the related images, and the platform
// config ConfigMap with the canonical version — and nothing connects
// until Deploy, whose stubbed remote seams then receive exactly that
// payload with the confirmed owner and the shared release.
func TestLoadRenderComposeRealServicesOffline(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("RELATED_IMAGE_ALPHA", "quay.io/example/alpha:v1")
	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/example/controller:v2")

	root := t.TempDir()
	writeFile(t, root, "definition.yaml", composeDefinitionYAML)
	writeFile(t, root, "manifests/chart/Chart.yaml", composeChartYAML)
	writeFile(t, root, "manifests/chart/values.yaml", composeValuesYAML)
	writeFile(t, root, "manifests/chart/templates/deployment.yaml", composeTemplateYAML)

	options := testOptions()
	options.DefinitionFile = filepath.Join(root, "definition.yaml")

	stub := &stubRunner{
		calls:      &[]string{},
		definition: testDefinition(),
	}

	confirmed := &v1alpha1.PlatformModuleDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic", UID: "definition-uid"},
	}
	stub.ensureOwner = confirmed

	composed := New(
		options,
		testGetter(t),
		loader.LoadFromPath,
		helm.RenderWithOptions,
		kustomize.RenderWithOptions,
		stub.connect,
		stub.ensure,
		stub.apply,
	)

	g.Expect(composed.Load(t.Context())).To(Succeed())
	g.Expect(composed.Render(t.Context())).To(Succeed())

	// The local stages never touched a remote seam.
	g.Expect(*stub.calls).To(BeEmpty())

	g.Expect(composed.resources).To(HaveLen(2))

	deployment := findResource(composed.resources, "Deployment", "synthetic-controller")
	g.Expect(deployment).NotTo(BeNil())
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "run-namespace"`),
		jq.Match(`.spec.template.spec.containers[0].image == "quay.io/example/controller:v2"`),
		jq.Match(`.spec.template.spec.containers[0].env[0].name == "LOG_LEVEL"`),
		jq.Match(`.spec.template.spec.containers[0].env[0].value == "debug"`),
		jq.Match(`.spec.template.spec.containers[0].env[1].name == "RELATED_IMAGE_ALPHA"`),
		jq.Match(`.spec.template.spec.containers[0].env[1].value == "quay.io/example/alpha:v1"`),
		jq.Match(`.spec.template.spec.containers[0].env[2].name == "APPLICATIONS_NAMESPACE"`),
		jq.Match(`.spec.template.spec.containers[0].env[2].value == "run-namespace"`),
		jq.Match(`.spec.template.spec.containers[0].env[3].name == "ODH_MODULE_OPERATOR_PLATFORM_TYPE"`),
		jq.Match(`.spec.template.spec.containers[0].env[3].value == "OpenDataHub"`),
		jq.Match(`.spec.template.spec.containers[0].env | length == 4`),
	))

	configMap := findResource(composed.resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap).NotTo(BeNil())
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "run-namespace"`),
		jq.Match(`.data.platformVersion == "2.19.0"`),
	))

	// Deploy drives exactly the stubbed remote seams, in order, with
	// the confirmed owner, the shared release, and the whole rendered
	// payload.
	g.Expect(composed.Deploy(t.Context())).To(Succeed())

	g.Expect(*stub.calls).To(Equal([]string{"connect", "ensure", "apply"}))
	g.Expect(stub.appliedOwner).To(BeIdenticalTo(confirmed))
	g.Expect(stub.appliedRelease.Name).To(Equal(api.Platform(api.ReleasePlatform)))
	g.Expect(stub.appliedRelease.Version.String()).To(Equal("2.19.0"))
	g.Expect(stub.appliedResources).To(HaveLen(2))
	g.Expect(findResource(stub.appliedResources, "Deployment", "synthetic-controller")).NotTo(BeNil())
	g.Expect(findResource(stub.appliedResources, "ConfigMap", "odh-synthetic-config")).NotTo(BeNil())
}
