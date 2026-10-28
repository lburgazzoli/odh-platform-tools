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

package deployer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

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

// literalControllerDefinitionYAML is the Helm-format definition whose
// main container image is a literal: no image environment is needed,
// so the flows over it run in parallel.
const literalControllerDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
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
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
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

// brokenTemplateYAML is a chart template the Helm backend cannot
// parse: an undefined template function fails the render.
const brokenTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  namespace: "{{ .Release.Namespace }}"
spec:
  replicas: {{ undefinedTemplateFunction .Values }}
`

// testOptions returns valid invocation settings without a client: the
// harness supplies the client through WithClient, so the preset-only
// construction path stays exercisable.
func testOptions() Options {
	return Options{
		DefinitionFile:  "definition.yaml",
		PlatformVersion: "2.19.0",
		Timeout:         5 * time.Minute,
		Namespace:       "run-namespace",
	}
}

// testDefinition returns a typed Helm-format definition whose main
// container image is a whole-token reference.
func testDefinition() toolApi.PlatformModuleDefinition {
	return toolApi.PlatformModuleDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: toolApi.APIVersion, Kind: toolApi.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic"},
		Spec: toolApi.PlatformModuleDefinitionSpec{
			Instance: toolApi.Instance{
				Type: toolApi.ModuleType{
					Group:   "components.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "SyntheticModule",
				},
			},
			Manifests: toolApi.Manifests{
				Format: toolApi.FormatHelm,
				Helm:   &toolApi.HelmManifests{Path: "./manifests/chart"},
			},
			RelatedImages: []string{"RELATED_IMAGE_ALPHA"},
			Controller: toolApi.Controller{
				Deployment: toolApi.ControllerDeployment{
					Name: "synthetic-controller",
					Container: &toolApi.ContainerDefinition{
						Name:  "manager",
						Image: "{{RELATED_IMAGE_CONTROLLER}}",
					},
				},
			},
		},
	}
}

// composeRoot writes one parameterized composition fixture — the
// definition and the chart's deployment template are the callers'
// variants — and returns its root directory.
func composeRoot(t *testing.T, definition string, template string) string {
	t.Helper()

	root := t.TempDir()
	writeFile(t, root, "definition.yaml", definition)
	writeFile(t, root, "manifests/chart/Chart.yaml", composeChartYAML)
	writeFile(t, root, "manifests/chart/values.yaml", composeValuesYAML)
	writeFile(t, root, "manifests/chart/templates/deployment.yaml", template)

	return root
}

// newTestDeployer wires a Deployer over the fixture's intercepted
// client: the options preset carries the invocation settings and
// WithClient injects the client, exactly as the composition layer
// does in production.
func newTestDeployer(t *testing.T, options Options, fixture *prepareCluster, root string) *Deployer {
	t.Helper()

	options.DefinitionFile = filepath.Join(root, "definition.yaml")

	return New(options, WithClient(fixture.client))
}

// writeFile writes one fixture file under root, failing the test on
// any error.
func writeFile(t *testing.T, root string, name string, content string) {
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
func findResource(resources []unstructured.Unstructured, kind string, name string) *unstructured.Unstructured {
	for i := range resources {
		if resources[i].GetKind() == kind && resources[i].GetName() == name {
			return &resources[i]
		}
	}

	return nil
}

// TestNewResolvesOptionsInOrder proves the constructor resolves its
// options in application order — a whole Options preset applies
// wholesale, later WithXxx options override individual fields — and
// ignores nil options, including a typed-nil functional option.
func TestNewResolvesOptionsInOrder(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	other := newPrepareCluster(t)

	empty := New()
	g.Expect(empty.options).To(Equal(Options{
		PlatformType:     DefaultPlatformType,
		FieldManager:     DefaultFieldManager,
		WaitTimeout:      DefaultWaitTimeout,
		WaitPollInterval: DefaultWaitPollInterval,
	}))
	g.Expect(empty.client).To(BeNil())
	g.Expect(empty.stage).To(Equal(stageNew))

	preset := testOptions()
	preset.FieldManager = "preset.example"

	// The preset survives with its unset settings resolved to the
	// documented defaults.
	resolvedPreset := Options{
		DefinitionFile:      preset.DefinitionFile,
		RelatedImagesFile:   preset.RelatedImagesFile,
		Namespace:           preset.Namespace,
		MonitoringNamespace: preset.MonitoringNamespace,
		PlatformType:        DefaultPlatformType,
		PlatformVersion:     preset.PlatformVersion,
		Timeout:             preset.Timeout,
		Client:              preset.Client,
		Wait:                preset.Wait,
		WaitTimeout:         DefaultWaitTimeout,
		WaitPollInterval:    DefaultWaitPollInterval,
		FieldManager:        "preset.example",
	}

	target := New(preset)
	g.Expect(target.options).To(Equal(resolvedPreset))
	g.Expect(target.client).To(BeNil())

	overridden := New(preset,
		WithClient(fixture.client),
		WithWaitPollInterval(5*time.Second),
	)
	g.Expect(overridden.options.WaitPollInterval).To(Equal(5 * time.Second))

	// The preset's untouched fields survive the overrides.
	g.Expect(overridden.options.FieldManager).To(Equal("preset.example"))
	g.Expect(overridden.options.Namespace).To(Equal("run-namespace"))

	// Client wiring is proven behaviorally: the intercepted fake
	// clients carry function fields, so they cannot be compared by
	// value. A probe object created through one fixture's client is
	// visible through exactly that fixture's client.
	fixtureProbe := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "run-namespace", Name: "fixture-wiring-probe"},
	}
	g.Expect(fixture.client.Create(t.Context(), fixtureProbe)).To(Succeed())

	otherProbe := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "run-namespace", Name: "other-wiring-probe"},
	}
	g.Expect(other.client.Create(t.Context(), otherProbe)).To(Succeed())

	g.Expect(servesObject(t, overridden.client, "fixture-wiring-probe")).To(BeTrue())
	g.Expect(servesObject(t, overridden.client, "other-wiring-probe")).To(BeFalse())
	g.Expect(servesObject(t, overridden.options.Client, "fixture-wiring-probe")).To(BeTrue())

	// Later options win over earlier ones.
	last := New(preset,
		WithClient(fixture.client),
		WithClient(other.client),
	)
	g.Expect(servesObject(t, last.client, "other-wiring-probe")).To(BeTrue())
	g.Expect(servesObject(t, last.client, "fixture-wiring-probe")).To(BeFalse())

	// A nil option and a typed-nil functional option are ignored; the
	// resolved defaults are unaffected.
	var nilOption DeployerOption
	g.Expect(New(preset, nilOption).options).To(Equal(resolvedPreset))
	g.Expect(New(preset, option.FunctionalOption[Options](nil)).options).To(Equal(resolvedPreset))

	// Separately constructed deployers hold independent state.
	first, second := New(preset), New(preset)
	first.fail()
	g.Expect(first.stage).To(Equal(stageFailed))
	g.Expect(second.stage).To(Equal(stageNew))
}

// TestNewInitializesRetryBackoff proves every construction owns the
// default retry schedule as independent storage: mutating one
// instance's schedule can affect neither another instance nor a later
// construction, and an Options preset cannot erase it.
func TestNewInitializesRetryBackoff(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	expected := []time.Duration{
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
	}

	empty := New()
	preset := New(testOptions())
	g.Expect(empty.payloadRetryBackoff).To(Equal(expected))
	g.Expect(preset.payloadRetryBackoff).To(Equal(expected))

	// Mutating one instance's schedule leaves the other and later
	// constructions untouched.
	empty.payloadRetryBackoff[0] = time.Hour
	g.Expect(preset.payloadRetryBackoff[0]).To(Equal(time.Second))

	fresh := New()
	g.Expect(fresh.payloadRetryBackoff).To(Equal(expected))
}

// servesObject reports whether client serves the named ConfigMap in
// the run namespace — the behavioral identity probe for intercepted
// clients, whose function fields make value comparison impossible.
func servesObject(t *testing.T, client crclient.Client, name string) bool {
	t.Helper()

	probe := &corev1.ConfigMap{}

	return client.Get(t.Context(),
		crclient.ObjectKey{Namespace: "run-namespace", Name: name},
		probe) == nil
}

// TestLoadRejectsInvalidOptions proves invalid settings fail Load as
// options errors before any file read or client call, and the
// invocation is terminal: later stages reject without input or
// output.
func TestLoadRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		message     string
		prefix      bool
		missingFile bool
		noClient    bool
		options     func(root string) Options
	}{
		{"missing definition file", "invalid deploy options: DefinitionFile is required", false, true, false, func(string) Options {
			options := testOptions()
			options.DefinitionFile = ""
			return options
		}},
		{"missing platform version", "invalid deploy options: PlatformVersion is required", false, false, false, func(string) Options {
			options := testOptions()
			options.PlatformVersion = ""
			return options
		}},
		{"missing namespace", "invalid deploy options: Namespace is required", false, false, false, func(string) Options {
			options := testOptions()
			options.Namespace = ""
			return options
		}},
		{"missing client", "invalid deploy options: Client is required", false, false, true, func(string) Options {
			return testOptions()
		}},
		{"zero timeout", "invalid deploy options: Timeout must be positive", false, false, false, func(string) Options {
			options := testOptions()
			options.Timeout = 0
			return options
		}},
		{"negative timeout", "invalid deploy options: Timeout must be positive", false, false, false, func(string) Options {
			options := testOptions()
			options.Timeout = -time.Minute
			return options
		}},
		{"negative wait poll interval", "invalid deploy options: WaitPollInterval must not be negative", false, false, false, func(string) Options {
			options := testOptions()
			options.WaitPollInterval = -time.Millisecond
			return options
		}},
		{"negative wait timeout on requested wait", "invalid deploy options: WaitTimeout must not be negative", false, false, false, func(string) Options {
			options := testOptions()
			options.Wait = true
			options.WaitTimeout = -time.Second
			return options
		}},
		{"unparsable platform version", "invalid deploy options: parsing platform version:", true, false, false, func(string) Options {
			options := testOptions()
			options.PlatformVersion = "not-a-version"
			return options
		}},
		{"empty-ish whitespace field manager", "invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters", false, false, false, func(string) Options {
			options := testOptions()
			options.FieldManager = " "
			return options
		}},
		{"oversized field manager", "invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters", false, false, false, func(string) Options {
			options := testOptions()
			options.FieldManager = strings.Repeat("a", 64)
			return options
		}},
		{"field manager with slash", "invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters", false, false, false, func(string) Options {
			options := testOptions()
			options.FieldManager = "example.com/team/manager"
			return options
		}},
		{"field manager with space", "invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters", false, false, false, func(string) Options {
			options := testOptions()
			options.FieldManager = "team manager"
			return options
		}},
		{"field manager with control character", "invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters", false, false, false, func(string) Options {
			options := testOptions()
			options.FieldManager = "team\x09manager"
			return options
		}},
		{"field manager with unicode", "invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters", false, false, false, func(string) Options {
			options := testOptions()
			options.FieldManager = "tëam-manager"
			return options
		}},
		{"field manager with leading punctuation", "invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters", false, false, false, func(string) Options {
			options := testOptions()
			options.FieldManager = "-team-manager"
			return options
		}},
		{"field manager with trailing punctuation", "invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters", false, false, false, func(string) Options {
			options := testOptions()
			options.FieldManager = "team-manager."
			return options
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			fixture := newPrepareCluster(t)
			root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

			options := testCase.options(root)
			if !testCase.missingFile {
				options.DefinitionFile = filepath.Join(root, "definition.yaml")
			}

			var target *Deployer
			if testCase.noClient {
				target = New(options)
			} else {
				target = New(options, WithClient(fixture.client))
			}

			err := target.Load(t.Context())
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidDeployOptions)).To(BeTrue())
			g.Expect(errors.Is(err, moduleerrors.ErrStageSequence)).To(BeFalse())

			if testCase.prefix {
				g.Expect(err.Error()).To(HavePrefix(testCase.message))
			} else {
				g.Expect(err.Error()).To(Equal(testCase.message))
			}

			// No client call ran, and the failed invocation is
			// terminal.
			g.Expect(fixture.applies).To(BeEmpty())
			g.Expect(fixture.gets).To(BeEmpty())
			g.Expect(target.Render(t.Context())).NotTo(Succeed())
			g.Expect(target.Deploy(t.Context())).NotTo(Succeed())
			g.Expect(errors.Is(target.Render(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
		})
	}
}

// TestLoadSnapshotsImagesAndDefaults proves Load parses the platform
// version once into the canonical release, snapshots exactly the
// declared related keys plus the main image token from the real
// definition file, and applies the applications namespace and platform
// type defaults. The image sources come from the process environment,
// so this test cannot run in parallel.
func TestLoadSnapshotsImagesAndDefaults(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("RELATED_IMAGE_ALPHA", "quay.io/example/alpha:v1")
	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/example/controller:v2")
	t.Setenv("UNREQUESTED_IMAGE", "quay.io/example/unrequested:v1")

	fixture := newPrepareCluster(t)
	root := composeRoot(t, composeDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, testOptions(), fixture, root)

	g.Expect(target.Load(t.Context())).To(Succeed())

	g.Expect(target.images).To(Equal(map[string]string{
		"RELATED_IMAGE_ALPHA":      "quay.io/example/alpha:v1",
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v2",
	}))

	g.Expect(target.options.Namespace).To(Equal("run-namespace"))
	g.Expect(target.options.PlatformType).To(Equal(DefaultPlatformType))
	g.Expect(target.release.Name).To(Equal(api.Platform(api.ReleasePlatform)))
	g.Expect(target.release.Version.String()).To(Equal("2.19.0"))

	// The local stage performed no client call.
	g.Expect(fixture.applies).To(BeEmpty())
	g.Expect(fixture.gets).To(BeEmpty())
}

// TestLoadAcceptsNoncanonicalVersion proves a noncanonical but
// parsable platform version produces one canonical string shared by
// the injector and deploy stages.
func TestLoadAcceptsNoncanonicalVersion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := testOptions()
	options.PlatformVersion = "v2.19.0+dev"

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, options, fixture, root)

	g.Expect(target.Load(t.Context())).To(Succeed())
	g.Expect(target.release.Version.String()).To(Equal("2.19.0+dev"))
}

// TestLoadKeepsExplicitNamespace proves the namespace is exactly the
// options value, applied verbatim.
func TestLoadKeepsExplicitNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := testOptions()
	options.Namespace = "applications"
	options.MonitoringNamespace = "monitoring"

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, options, fixture, root)

	g.Expect(target.Load(t.Context())).To(Succeed())

	g.Expect(target.options.Namespace).To(Equal("applications"))
}

// TestStageSequence proves stages run exactly in Load → Render →
// Deploy order: out-of-order, repeated, and post-success calls reject
// with the sequence sentinel and no client call.
func TestStageSequence(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, testOptions(), fixture, root)

	// Render and Deploy before Load reject without any client call,
	// with the exact stage-numbered diagnostics.
	g.Expect(target.Render(t.Context()).Error()).To(Equal(
		"invalid deployer stage sequence: stage 2 cannot follow 0"))
	g.Expect(target.Deploy(t.Context()).Error()).To(Equal(
		"invalid deployer stage sequence: stage 3 cannot follow 0"))
	g.Expect(errors.Is(target.Render(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(errors.Is(target.Deploy(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(fixture.applies).To(BeEmpty())

	g.Expect(target.Load(t.Context())).To(Succeed())

	// Deploy before Render, and a repeated Load, reject the same way.
	g.Expect(errors.Is(target.Deploy(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(errors.Is(target.Load(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(fixture.applies).To(BeEmpty())

	g.Expect(target.Render(t.Context())).To(Succeed())

	g.Expect(errors.Is(target.Render(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(fixture.applies).To(BeEmpty())

	g.Expect(target.Deploy(t.Context())).To(Succeed())

	// A successful invocation accepts no further stage.
	g.Expect(errors.Is(target.Deploy(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(errors.Is(target.Load(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
}

// TestFailedStageIsTerminal proves a stage failure makes the
// invocation terminal: no later stage call performs input or output.
// The failure is a real one — a definition file the loader cannot
// read.
func TestFailedStageIsTerminal(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	options := testOptions()
	options.DefinitionFile = filepath.Join(root, "missing.yaml")

	target := New(options, WithClient(fixture.client))

	g.Expect(target.Load(t.Context())).NotTo(Succeed())

	g.Expect(errors.Is(target.Load(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(errors.Is(target.Render(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(errors.Is(target.Deploy(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(fixture.applies).To(BeEmpty())
}

// TestLoadAndRenderAreClientFree proves the local stages perform no
// client call of any kind, and the render lands in the applications
// namespace.
func TestLoadAndRenderAreClientFree(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, testOptions(), fixture, root)

	g.Expect(target.Load(t.Context())).To(Succeed())
	g.Expect(target.Render(t.Context())).To(Succeed())

	g.Expect(fixture.applies).To(BeEmpty())
	g.Expect(fixture.gets).To(BeEmpty())

	// The render went to the applications namespace with the
	// platform config ConfigMap appended.
	g.Expect(target.resources).To(HaveLen(2))
	deployment := findResource(target.resources, "Deployment", "synthetic-controller")
	g.Expect(deployment).NotTo(BeNil())
	g.Expect(deployment.Object).To(jq.Match(`.metadata.namespace == "run-namespace"`))
	g.Expect(findResource(target.resources, "ConfigMap", "odh-synthetic-config")).NotTo(BeNil())
}

// TestRenderFailureIsTerminal proves a real render failure — a chart
// template the backend cannot parse — is terminal and never reaches a
// client call.
func TestRenderFailureIsTerminal(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, brokenTemplateYAML)

	target := newTestDeployer(t, testOptions(), fixture, root)

	g.Expect(target.Load(t.Context())).To(Succeed())
	g.Expect(target.Render(t.Context())).To(HaveOccurred())

	g.Expect(errors.Is(target.Render(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(fixture.applies).To(BeEmpty())
	g.Expect(target.Deploy(t.Context())).NotTo(Succeed())
	g.Expect(fixture.applies).To(BeEmpty())
}

// TestDeploySequencesPrepareApply proves Deploy runs its remote steps
// in order — owner preparation, then the whole payload apply — with
// the preparation performing exactly the CRD and definition applies,
// and the payload writes landing after them with the server-confirmed
// owner and the shared release.
func TestDeploySequencesPrepareApply(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, testOptions(), fixture, root)

	g.Expect(target.Load(t.Context())).To(Succeed())
	g.Expect(target.Render(t.Context())).To(Succeed())
	g.Expect(target.Deploy(t.Context())).To(Succeed())

	g.Expect(target.stage).To(Equal(stageDeployed))
	g.Expect(target.applied).To(BeTrue())

	// The preparation applied the embedded CRD, then the definition,
	// then the payload writes.
	crdApplies, crdFirst := fixture.applyIndex(prepareCRDWrite)
	definitionApplies, definitionFirst := fixture.applyIndex(prepareDefinitionWrite)
	payloadApplies, payloadFirst := fixture.applyIndex(preparePayloadDeploymentWrite)
	g.Expect(fixture.applies).To(HaveLen(4))
	g.Expect(crdApplies).To(Equal(1))
	g.Expect(definitionApplies).To(Equal(1))
	g.Expect(payloadApplies).To(Equal(1))
	g.Expect(definitionFirst).To(BeNumerically(">", crdFirst))
	g.Expect(payloadFirst).To(BeNumerically(">", definitionFirst))
	configMapApplies, _ := fixture.applyIndex(preparePayloadConfigMapWrite)
	g.Expect(configMapApplies).To(Equal(1))

	// The deployer holds the server-confirmed owner: the authored
	// name with the identity the apply response carried.
	g.Expect(target.owner).NotTo(BeNil())
	g.Expect(target.owner.Name).To(Equal("synthetic"))
	g.Expect(string(target.owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))
	g.Expect(target.release.Name).To(Equal(api.Platform(api.ReleasePlatform)))
	g.Expect(target.release.Version.String()).To(Equal("2.19.0"))
}

// TestDeployForwardsEffectiveFieldManager proves the invocation's
// field manager — the custom value, or the canonical platform default
// when the option is empty — reaches the owner preparation and every
// ordinary payload apply as one resolved value.
func TestDeployForwardsEffectiveFieldManager(t *testing.T) {
	t.Parallel()

	t.Run("custom manager", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		options := testOptions()
		options.FieldManager = "team.example.deployer"

		fixture := newPrepareCluster(t)
		root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

		target := newTestDeployer(t, options, fixture, root)

		g.Expect(target.Load(t.Context())).To(Succeed())
		g.Expect(target.Render(t.Context())).To(Succeed())
		g.Expect(target.Deploy(t.Context())).To(Succeed())

		// Every write — both preparation applies and both payload
		// applies — claimed its fields under the invocation's
		// manager.
		g.Expect(fixture.applyOptions).To(HaveLen(4))
		for _, captured := range fixture.applyOptions {
			g.Expect(captured.manager).To(Equal("team.example.deployer"))
			g.Expect(captured.force).To(BeTrue())
		}
	})

	t.Run("empty option means the platform default", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		fixture := newPrepareCluster(t)
		root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

		target := newTestDeployer(t, testOptions(), fixture, root)

		g.Expect(target.Load(t.Context())).To(Succeed())
		g.Expect(target.Render(t.Context())).To(Succeed())
		g.Expect(target.Deploy(t.Context())).To(Succeed())

		g.Expect(fixture.applyOptions).To(HaveLen(4))
		for _, captured := range fixture.applyOptions {
			g.Expect(captured.manager).To(Equal(DefaultFieldManager))
		}
	})
}

// TestDeployPreparationFailureStopsBeforePayload proves a preparation
// failure — the CRD apply or the definition apply refusing — stops
// Deploy before any payload write, reports no owner, and stays
// terminal. A nil owner never claims that no write occurred.
func TestDeployPreparationFailureStopsBeforePayload(t *testing.T) {
	t.Parallel()

	t.Run("crd apply failure", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		fixture := newPrepareCluster(t)
		fixture.failApply[prepareCRDWrite] = errors.New("crd refusal")

		root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)
		target := newTestDeployer(t, testOptions(), fixture, root)

		g.Expect(target.Load(t.Context())).To(Succeed())
		g.Expect(target.Render(t.Context())).To(Succeed())

		err := target.Deploy(t.Context())
		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.Is(err, moduleerrors.ErrStageSequence)).To(BeFalse())

		// No payload write ran, and no owner is confirmed.
		payloadApplies, _ := fixture.applyIndex(preparePayloadDeploymentWrite)
		g.Expect(payloadApplies).To(Equal(0))
		g.Expect(target.owner).To(BeNil())
		g.Expect(target.applied).To(BeFalse())

		// The failed invocation is terminal.
		g.Expect(errors.Is(target.Deploy(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	})

	t.Run("definition apply failure", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		fixture := newPrepareCluster(t)
		fixture.failApply[prepareDefinitionWrite] = errors.New("definition refusal")

		root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)
		target := newTestDeployer(t, testOptions(), fixture, root)

		g.Expect(target.Load(t.Context())).To(Succeed())
		g.Expect(target.Render(t.Context())).To(Succeed())

		g.Expect(target.Deploy(t.Context())).To(HaveOccurred())

		payloadApplies, _ := fixture.applyIndex(preparePayloadDeploymentWrite)
		g.Expect(payloadApplies).To(Equal(0))
		g.Expect(target.owner).To(BeNil())
		g.Expect(target.applied).To(BeFalse())
	})
}

// TestDeployApplyFailureRetainsOwner proves a payload apply failure
// keeps the server-confirmed owner while marking the apply
// incomplete, with the failure's cause reachable through Unwrap.
func TestDeployApplyFailureRetainsOwner(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cause := &markerError{marker: "PAYLOAD-MARKER-refusal"}

	fixture := newPrepareCluster(t)
	fixture.failApply[preparePayloadDeploymentWrite] = cause

	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)
	target := newTestDeployer(t, testOptions(), fixture, root)
	// A shortened schedule keeps the exhausted-retry path fast.
	target.payloadRetryBackoff = []time.Duration{
		time.Millisecond,
		time.Millisecond,
		time.Millisecond,
		time.Millisecond,
	}

	g.Expect(target.Load(t.Context())).To(Succeed())
	g.Expect(target.Render(t.Context())).To(Succeed())

	err := target.Deploy(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrDeployApply)).To(BeTrue())

	g.Expect(target.owner).NotTo(BeNil())
	g.Expect(target.owner.Name).To(Equal("synthetic"))
	g.Expect(string(target.owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))
	g.Expect(target.applied).To(BeFalse())

	// The framework's raw report stays reachable through Unwrap.
	g.Expect(errors.Is(err, moduleerrors.ErrDeployPrepare)).To(BeFalse())
	g.Expect(err.Error()).NotTo(ContainSubstring("PAYLOAD-MARKER-refusal"))
	var marker *markerError
	g.Expect(errors.As(err, &marker)).To(BeTrue())
}

// TestLoadRenderComposeRealServices proves the full offline chain
// through the real loader, the real Helm backend, and the real payload
// apply over the fake cluster: a chart on disk renders through the
// shared engine into the applications namespace with both injectors
// applied — the resolved controller image, the related images, the
// platform env, and the platform config ConfigMap with the canonical
// version — and Deploy then applies exactly that payload with the
// server-confirmed owner. The image sources come from the process
// environment, so this test cannot run in parallel.
func TestLoadRenderComposeRealServices(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("RELATED_IMAGE_ALPHA", "quay.io/example/alpha:v1")
	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/example/controller:v2")

	fixture := newPrepareCluster(t)
	root := composeRoot(t, composeDefinitionYAML, composeTemplateYAML)

	options := testOptions()
	options.PlatformVersion = "v2.19.0+dev"
	options.MonitoringNamespace = "monitoring"

	target := newTestDeployer(t, options, fixture, root)

	g.Expect(target.Load(t.Context())).To(Succeed())
	g.Expect(target.Render(t.Context())).To(Succeed())

	// The local stages never touched the cluster.
	g.Expect(fixture.applies).To(BeEmpty())
	g.Expect(fixture.gets).To(BeEmpty())

	g.Expect(target.resources).To(HaveLen(2))

	deployment := findResource(target.resources, "Deployment", "synthetic-controller")
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
		jq.Match(`.spec.template.spec.containers[0].env[3].name == "MONITORING_NAMESPACE"`),
		jq.Match(`.spec.template.spec.containers[0].env[3].value == "monitoring"`),
		jq.Match(`.spec.template.spec.containers[0].env[4].name == "ODH_MODULE_OPERATOR_PLATFORM_TYPE"`),
		jq.Match(`.spec.template.spec.containers[0].env[4].value == "OpenDataHub"`),
		jq.Match(`.spec.template.spec.containers[0].env | length == 5`),
	))

	configMap := findResource(target.resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap).NotTo(BeNil())
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "run-namespace"`),
		jq.Match(`.data.platformVersion == "2.19.0+dev"`),
	))

	// Deploy runs the real owner preparation and the real payload
	// apply over the fake cluster: the CRD, the definition, then the
	// whole rendered payload.
	g.Expect(target.Deploy(t.Context())).To(Succeed())

	crdApplies, crdFirst := fixture.applyIndex(prepareCRDWrite)
	definitionApplies, definitionFirst := fixture.applyIndex(prepareDefinitionWrite)
	payloadDeploymentApplies, _ := fixture.applyIndex(preparePayloadDeploymentWrite)
	payloadConfigMapApplies, _ := fixture.applyIndex(preparePayloadConfigMapWrite)
	g.Expect(crdApplies).To(Equal(1))
	g.Expect(definitionApplies).To(Equal(1))
	g.Expect(payloadDeploymentApplies).To(Equal(1))
	g.Expect(payloadConfigMapApplies).To(Equal(1))
	g.Expect(definitionFirst).To(BeNumerically(">", crdFirst))

	// The stored payload carries the owner reference of the
	// server-confirmed definition.
	storedDeployment := fixture.stored(t, deploymentGVK, "run-namespace", "synthetic-controller")
	g.Expect(storedDeployment.Object).To(SatisfyAll(
		jq.Match(`.metadata.ownerReferences[0].kind == "PlatformModuleDefinition"`),
		jq.Match(`.metadata.ownerReferences[0].uid == "`+prepareServerUID(prepareDefinitionIdentity)+`"`),
	))

	g.Expect(target.owner).NotTo(BeNil())
	g.Expect(string(target.owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))
}
