package adapt

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"
	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/k8s"

	toolk8s "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// controllerDeploymentYAML is the module controller Deployment with
// an init container, a main manager container carrying one chart env
// entry, and a sidecar.
const controllerDeploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      initContainers:
      - name: copy-manifests
        image: registry.example/chart-init:v0
      containers:
      - name: manager
        image: registry.example/chart-controller:v0
        env:
        - name: CHART_ENV
          value: chart-value
      - name: sidecar
        image: registry.example/chart-sidecar:v0
`

// deploymentWithoutInitYAML is the controller Deployment with no init
// containers.
const deploymentWithoutInitYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      containers:
      - name: manager
        image: registry.example/chart-controller:v0
        env:
        - name: CHART_ENV
          value: chart-value
`

// deploymentEmptyInitYAML is the controller Deployment with an empty
// initContainers list.
const deploymentEmptyInitYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      initContainers: []
      containers:
      - name: manager
        image: registry.example/chart-controller:v0
`

// deploymentOtherInitYAML is the controller Deployment whose init
// container is not the configured one.
const deploymentOtherInitYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      initContainers:
      - name: other-init
        image: registry.example/other-init:v0
      containers:
      - name: manager
        image: registry.example/chart-controller:v0
`

// controllerDeploymentValueFromYAML is the controller Deployment whose
// main container carries an env entry sourced from a valueFrom block.
const controllerDeploymentValueFromYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      containers:
      - name: manager
        image: registry.example/chart-controller:v0
        env:
        - name: RELATED_IMAGE_OPERAND
          valueFrom:
            secretKeyRef:
              name: operand-secret
              key: image
`

// controllerDeploymentAlreadyCorrectYAML is the controller Deployment
// already carrying the resolved main and init images and the extra env
// value the injector would apply.
const controllerDeploymentAlreadyCorrectYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      initContainers:
      - name: copy-manifests
        image: quay.io/example/controller:v1
      containers:
      - name: manager
        image: quay.io/example/controller:v1
        env:
        - name: LOG_LEVEL
          value: debug
`

// controllerDeploymentMalformedEnvYAML is the controller Deployment
// whose main container env is not a list: the typed conversion of the
// targeted Deployment fails.
const controllerDeploymentMalformedEnvYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      containers:
      - name: manager
        image: registry.example/chart-controller:v0
        env: not-a-list
`

// deploymentMalformedContainersYAML is the controller Deployment whose
// containers field is not a list.
const deploymentMalformedContainersYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      containers: not-a-list
`

// deploymentMalformedReplicasYAML is the controller Deployment whose
// replicas field is not an integer: conversion covers the whole
// target, not only the container and env fields.
const deploymentMalformedReplicasYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  replicas: not-an-integer
  template:
    spec:
      containers:
      - name: manager
        image: registry.example/chart-controller:v0
`

// deploymentTargetWithoutContainersYAML is the module's own Deployment
// with no containers field at all.
const deploymentTargetWithoutContainersYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec: {}
`

// deploymentTargetEmptyContainersYAML is the module's own Deployment
// with an empty containers list.
const deploymentTargetEmptyContainersYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      containers: []
`

// foreignDeploymentWithoutContainersYAML is a foreign Deployment with
// no containers at all.
const foreignDeploymentWithoutContainersYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: other-controller
spec:
  template:
    spec: {}
`

// foreignMalformedContainersYAML is a foreign Deployment whose
// containers field is not a list.
const foreignMalformedContainersYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: other-controller
spec:
  template:
    spec:
      containers: not-a-list
`

// foreignMalformedEnvYAML is a foreign Deployment whose main container
// env is not a list.
const foreignMalformedEnvYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: other-controller
spec:
  template:
    spec:
      containers:
      - name: manager
        image: registry.example/other:v0
        env: not-a-list
`

// deploymentWrongMainContainerYAML is the module's own Deployment
// whose only container does not match the configured main container
// name, carrying an unknown field that a typed round-trip would drop.
const deploymentWrongMainContainerYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      containers:
      - name: other-main
        image: registry.example/other-main:v0
        x-custom: keep-me
`

// controllerDeploymentUnknownFieldYAML is the module's controller
// Deployment carrying an unknown extension field on the pod spec,
// which the typed round-trip may drop while known fields survive.
const controllerDeploymentUnknownFieldYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  labels:
    app: synthetic
spec:
  replicas: 2
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      x-extension: sentinel
      containers:
      - name: manager
        image: registry.example/chart-controller:v0
        env:
        - name: CHART_ENV
          value: chart-value
      - name: sidecar
        image: registry.example/chart-sidecar:v0
`

// sameNameWrongGVKYAML is a resource of the targeted name that is not
// an exact apps/v1 Deployment.
const sameNameWrongGVKYAML = `apiVersion: apps/v1beta1
kind: Deployment
metadata:
  name: synthetic-controller
spec:
  template:
    spec:
      containers:
      - name: manager
        image: registry.example/chart-controller:v0
`

// otherDeploymentYAML is a valid foreign Deployment.
const otherDeploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: other-controller
spec:
  template:
    spec:
      containers:
      - name: manager
        image: registry.example/other:v0
`

// moduleNamedDeploymentYAML is a Deployment named after the module
// itself, for the empty deployment-name fallback.
const moduleNamedDeploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic
spec:
  template:
    spec:
      containers:
      - name: manager
        image: registry.example/chart-controller:v0
`

// newEnvInjector returns the env injector for the given definition
// and fully populated runtime inputs.
func newEnvInjector(definition *toolApi.PlatformModuleDefinition) *EnvInjector {
	return &EnvInjector{
		Definition:          definition,
		Images:              newResolvedImages(),
		Namespace:           "opendatahub",
		MonitoringNamespace: "opendatahub-monitoring",
		PlatformType:        "self-managed",
	}
}

// TestEnvInjector pins the happy path: the resolved controller image
// overrides the main and init containers, and the env entries append
// after the chart env in operator precedence order — related images,
// sorted extra env, then the nonempty platform env — into the main and
// init containers alike.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectModuleEnv, injectEnvVarsIntoDeployment), commit 5a1d17f57b32.
func TestEnvInjector(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

	input, err := toolk8s.DecodeResources([]byte(controllerDeploymentYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(deployment.Object).To(SatisfyAll(
		WithTransform(k8s.Container("manager"), SatisfyAll(
			HaveField("Image", "quay.io/example/controller:v1"),
			WithTransform(envVarNames, Equal([]string{
				"CHART_ENV", "RELATED_IMAGE_OPERAND", "LOG_LEVEL", "APPLICATIONS_NAMESPACE",
				"MONITORING_NAMESPACE", "ODH_MODULE_OPERATOR_PLATFORM_TYPE",
			})),
			WithTransform(k8s.EnvVar("RELATED_IMAGE_OPERAND"), HaveField("Value", "quay.io/example/operand:v1")),
			WithTransform(k8s.EnvVar("LOG_LEVEL"), HaveField("Value", "debug")),
			WithTransform(k8s.EnvVar("APPLICATIONS_NAMESPACE"), HaveField("Value", "opendatahub")),
			WithTransform(k8s.EnvVar("MONITORING_NAMESPACE"), HaveField("Value", "opendatahub-monitoring")),
			WithTransform(k8s.EnvVar("ODH_MODULE_OPERATOR_PLATFORM_TYPE"), HaveField("Value", "self-managed")),
		)),
		WithTransform(k8s.InitContainer("copy-manifests"), SatisfyAll(
			HaveField("Image", "quay.io/example/controller:v1"),
			WithTransform(envVarNames, Equal([]string{
				"RELATED_IMAGE_OPERAND", "LOG_LEVEL", "APPLICATIONS_NAMESPACE",
				"MONITORING_NAMESPACE", "ODH_MODULE_OPERATOR_PLATFORM_TYPE",
			})),
		)),
		WithTransform(k8s.Container("sidecar"), SatisfyAll(
			HaveField("Image", "registry.example/chart-sidecar:v0"),
			HaveField("Env", BeNil()),
		)),
	))
}

// TestEnvInjectorLiteralImage pins that a literal controller image
// overrides without any image-source lookup.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, mi.ControllerImage), commit 5a1d17f57b32.
func TestEnvInjectorLiteralImage(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.Container.Image = "quay.io/example/controller:v9"

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: definition}).Run

	input, err := toolk8s.DecodeResources([]byte(controllerDeploymentYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(deployment.Object).To(WithTransform(k8s.Container("manager"), SatisfyAll(
		HaveField("Image", "quay.io/example/controller:v9"),
		WithTransform(envVarNames, Equal([]string{"CHART_ENV", "LOG_LEVEL"})),
	)))
}

// TestEnvInjectorUnresolvedImages pins the unresolved-image policy: a
// controller image that does not resolve — authored or absent
// altogether — fails the pass; the chart default is never kept
// silently.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, os.Getenv empty branches), commit 5a1d17f57b32.
func TestEnvInjectorUnresolvedImages(t *testing.T) {
	t.Parallel()

	t.Run("authored controller image unresolved errors", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = (&EnvInjector{Definition: newDefinition()}).Run

		input, err := toolk8s.DecodeResources([]byte(controllerDeploymentYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`container "manager" image unresolved in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})

	t.Run("nothing authored errors too", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definition := newDefinition()
		definition.Spec.Controller.Deployment.InitContainer = nil
		definition.Spec.Controller.Deployment.Container.Image = ""

		var render enginetypes.PostRenderer = (&EnvInjector{Definition: definition}).Run

		input, err := toolk8s.DecodeResources([]byte(controllerDeploymentYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`container "manager" image unresolved in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})
}

// TestEnvInjectorOverridesExistingEnvAndRemovesValueFrom pins that an
// existing chart env entry is overridden in place and its valueFrom
// block removed.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (setOrOverrideEnv), commit 5a1d17f57b32.
func TestEnvInjectorOverridesExistingEnvAndRemovesValueFrom(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.InitContainer = nil

	var render enginetypes.PostRenderer = newEnvInjector(definition).Run

	input, err := toolk8s.DecodeResources([]byte(controllerDeploymentValueFromYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(deployment.Object).To(WithTransform(k8s.Container("manager"), SatisfyAll(
		WithTransform(envVarNames, Equal([]string{
			"RELATED_IMAGE_OPERAND", "LOG_LEVEL", "APPLICATIONS_NAMESPACE",
			"MONITORING_NAMESPACE", "ODH_MODULE_OPERATOR_PLATFORM_TYPE",
		})),
		WithTransform(k8s.EnvVar("RELATED_IMAGE_OPERAND"), SatisfyAll(
			HaveField("Value", "quay.io/example/operand:v1"),
			HaveField("ValueFrom", BeNil()),
		)),
	)))
}

// TestEnvInjectorPrecedence pins the operator's semantic precedence
// when names collide: explicit extra env overrides a related image
// value, and the platform env overrides an extra env value of the
// same name.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment injection order), commit 5a1d17f57b32.
func TestEnvInjectorPrecedence(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.InitContainer = nil
	definition.Spec.Controller.Deployment.Container.Env = []toolApi.EnvVar{
		{Name: "RELATED_IMAGE_OPERAND", Value: new("extra-wins")},
		{Name: "APPLICATIONS_NAMESPACE", Value: new("extra-ns")},
	}

	var render enginetypes.PostRenderer = (&EnvInjector{
		Definition: definition,
		Images:     newResolvedImages(),
		Namespace:  "opendatahub",
	}).Run

	input, err := toolk8s.DecodeResources([]byte(deploymentWithoutInitYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(deployment.Object).To(WithTransform(k8s.Container("manager"), SatisfyAll(
		WithTransform(k8s.EnvVar("RELATED_IMAGE_OPERAND"), HaveField("Value", "extra-wins")),
		WithTransform(k8s.EnvVar("APPLICATIONS_NAMESPACE"), HaveField("Value", "opendatahub")),
	)))
}

// TestEnvInjectorExtraEnvListMapSemantics pins the list-to-map
// projection of the descriptor env list: later duplicates win, and an
// absent value projects to the empty string.
//
// Operator source: internal/controller/modules/modules_controller.go
// (provisionModules ModuleImages.ExtraEnv build), commit 5a1d17f57b32.
func TestEnvInjectorExtraEnvListMapSemantics(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.InitContainer = nil
	definition.Spec.Controller.Deployment.Container.Image = "quay.io/example/controller:v1"
	definition.Spec.Controller.Deployment.Container.Env = []toolApi.EnvVar{
		{Name: "ZETA", Value: new("one")},
		{Name: "ALPHA"},
		{Name: "ALPHA", Value: new("two")},
		{Name: "BRAVO"},
	}

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: definition}).Run

	input, err := toolk8s.DecodeResources([]byte(deploymentWithoutInitYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(deployment.Object).To(WithTransform(k8s.Container("manager"), SatisfyAll(
		WithTransform(k8s.EnvVar("ZETA"), HaveField("Value", "one")),
		WithTransform(k8s.EnvVar("ALPHA"), HaveField("Value", "two")),
		WithTransform(k8s.EnvVar("BRAVO"), SatisfyAll(
			HaveField("Value", ""),
			HaveField("ValueFrom", BeNil()),
		)),
	)))
}

// TestEnvInjectorPlaceholderValueStaysLiteral pins that an extra-env
// value looking like a replacement token is injected as authored,
// with no interpolation.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, mi.ExtraEnv), commit 5a1d17f57b32.
func TestEnvInjectorPlaceholderValueStaysLiteral(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.InitContainer = nil
	definition.Spec.Controller.Deployment.Container.Image = "quay.io/example/controller:v1"
	definition.Spec.Controller.Deployment.Container.Env = []toolApi.EnvVar{
		{Name: "LOG_LEVEL", Value: new("{{LOG_LEVEL}}")},
	}

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: definition}).Run

	input, err := toolk8s.DecodeResources([]byte(deploymentWithoutInitYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(deployment.Object).To(WithTransform(k8s.Container("manager"),
		WithTransform(k8s.EnvVar("LOG_LEVEL"), HaveField("Value", "{{LOG_LEVEL}}"))))
}

// TestEnvInjectorSortedExtraEnv pins that map-derived extra env
// entries append in sorted key order, keeping the output
// deterministic without changing the operator's semantic precedence.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, mi.ExtraEnv range), commit 5a1d17f57b32.
func TestEnvInjectorSortedExtraEnv(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.InitContainer = nil
	definition.Spec.Controller.Deployment.Container.Image = "quay.io/example/controller:v1"
	definition.Spec.Controller.Deployment.Container.Env = []toolApi.EnvVar{
		{Name: "ZETA", Value: new("z")},
		{Name: "ALPHA", Value: new("a")},
	}

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: definition}).Run

	input, err := toolk8s.DecodeResources([]byte(deploymentWithoutInitYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(deployment.Object).To(WithTransform(k8s.Container("manager"),
		WithTransform(envVarNames, Equal([]string{"CHART_ENV", "ALPHA", "ZETA"}))))
}

// TestEnvInjectorDeploymentNameFallback pins that an empty deployment
// name falls back to the module name — the Helm release name for this
// tool.
//
// Operator source: internal/controller/modules/modules_controller.go
// (ModuleImages.DeploymentName build), commit 5a1d17f57b32.
func TestEnvInjectorDeploymentNameFallback(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.InitContainer = nil
	definition.Spec.Controller.Deployment.Name = ""

	var render enginetypes.PostRenderer = (&EnvInjector{
		Definition: definition,
		Images:     newResolvedImages(),
	}).Run

	input, err := toolk8s.DecodeResources([]byte(moduleNamedDeploymentYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic"})
	g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic in the rendered output")
	g.Expect(deployment.Object).To(WithTransform(k8s.Container("manager"),
		HaveField("Image", "quay.io/example/controller:v1")))
}

// TestEnvInjectorSkipsOtherDeployments pins that a foreign Deployment
// with valid containers is left untouched, while the module's own
// Deployment is adapted.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, mi.DeploymentName mismatch), commit 5a1d17f57b32.
func TestEnvInjectorSkipsOtherDeployments(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

	batch := strings.Join([]string{
		controllerDeploymentYAML,
		otherDeploymentYAML,
	}, "\n---\n")

	input, err := toolk8s.DecodeResources([]byte(batch))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(2))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	other := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "other-controller"})
	g.Expect(other).NotTo(BeNil(), "expected Deployment other-controller in the rendered output")
	g.Expect(other.Object).To(WithTransform(k8s.Container("manager"), SatisfyAll(
		HaveField("Image", "registry.example/other:v0"),
		HaveField("Env", BeNil()),
	)))

	own := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(own).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(own.Object).To(WithTransform(k8s.Container("manager"),
		HaveField("Image", "quay.io/example/controller:v1")))
}

// TestEnvInjectorForeignMalformedDeploymentsPassThrough pins that
// foreign Deployments — without containers, or with malformed
// containers or env — are never converted: they remain byte-for-byte
// as rendered and do not fail the pass, while the module's own
// Deployment in the same batch is still adapted.
func TestEnvInjectorForeignMalformedDeploymentsPassThrough(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

	batch := strings.Join([]string{
		controllerDeploymentYAML,
		foreignDeploymentWithoutContainersYAML,
		foreignMalformedContainersYAML,
		foreignMalformedEnvYAML,
	}, "\n---\n")

	input, err := toolk8s.DecodeResources([]byte(batch))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(4))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	expected, err := toolk8s.DecodeResources([]byte(strings.Join([]string{
		foreignDeploymentWithoutContainersYAML,
		foreignMalformedContainersYAML,
		foreignMalformedEnvYAML,
	}, "\n---\n")))
	g.Expect(err).To(Succeed())
	g.Expect(expected).To(HaveLen(3))

	g.Expect(resources[1]).To(Equal(expected[0]))
	g.Expect(resources[2]).To(Equal(expected[1]))
	g.Expect(resources[3]).To(Equal(expected[2]))

	own := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(own).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(own.Object).To(WithTransform(k8s.Container("manager"),
		HaveField("Image", "quay.io/example/controller:v1")))
}

// TestEnvInjectorTargetWithoutContainersErrors pins that the module's
// own Deployment without containers — absent or empty — errors, and a
// failed pass returns no successful output.
func TestEnvInjectorTargetWithoutContainersErrors(t *testing.T) {
	t.Parallel()

	t.Run("absent containers", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

		input, err := toolk8s.DecodeResources([]byte(deploymentTargetWithoutContainersYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("deployment synthetic-controller has no containers"))
		g.Expect(resources).To(BeNil())
	})

	t.Run("empty containers", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

		input, err := toolk8s.DecodeResources([]byte(deploymentTargetEmptyContainersYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("deployment synthetic-controller has no containers"))
		g.Expect(resources).To(BeNil())
	})
}

// TestEnvInjectorMalformedContainersErrors pins that a non-list
// containers field on the targeted Deployment fails the typed
// conversion, and no failed output is presented as success.
func TestEnvInjectorMalformedContainersErrors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

	input, err := toolk8s.DecodeResources([]byte(deploymentMalformedContainersYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("deployment synthetic-controller"))
	g.Expect(resources).To(BeNil())
}

// TestEnvInjectorMalformedEnvErrors pins that a malformed existing
// env value on the targeted Deployment fails the typed conversion
// instead of being repaired.
func TestEnvInjectorMalformedEnvErrors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

	input, err := toolk8s.DecodeResources([]byte(controllerDeploymentMalformedEnvYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("deployment synthetic-controller"))
	g.Expect(resources).To(BeNil())
}

// TestEnvInjectorMalformedFieldOutsideContainersErrors pins that the
// typed conversion covers the whole targeted Deployment: a malformed
// replicas field fails the pass even though the containers are valid.
func TestEnvInjectorMalformedFieldOutsideContainersErrors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

	input, err := toolk8s.DecodeResources([]byte(deploymentMalformedReplicasYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("deployment synthetic-controller"))
	g.Expect(resources).To(BeNil())
}

// TestEnvInjectorMissingMainContainerErrors pins that a valid target
// whose containers do not include the configured main container name
// fails the pass, and no failed output is presented as success.
func TestEnvInjectorMissingMainContainerErrors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

	input, err := toolk8s.DecodeResources([]byte(deploymentWrongMainContainerYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(`container "manager" not found in Deployment synthetic-controller`))
	g.Expect(resources).To(BeNil())
}

// TestEnvInjectorDropsUnknownFieldsOnAdaptedTarget pins the typed
// round-trip boundary on an adapted target: an unknown pod-spec
// extension field may be dropped, while known metadata, spec, and
// sidecar fields survive semantically.
func TestEnvInjectorDropsUnknownFieldsOnAdaptedTarget(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.InitContainer = nil

	var render enginetypes.PostRenderer = newEnvInjector(definition).Run

	input, err := toolk8s.DecodeResources([]byte(controllerDeploymentUnknownFieldYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec | has("x-extension") | not`),
		jq.Match(`.metadata.labels.app == "synthetic"`),
		jq.Match(`.spec.replicas == 2`),
		jq.Match(`.spec.template.metadata.labels.app == "synthetic"`),
		WithTransform(k8s.Container("manager"), SatisfyAll(
			HaveField("Image", "quay.io/example/controller:v1"),
			WithTransform(k8s.EnvVar("CHART_ENV"), HaveField("Value", "chart-value")),
		)),
		WithTransform(k8s.Container("sidecar"), HaveField("Image", "registry.example/chart-sidecar:v0")),
	))
}

// TestEnvInjectorNoContainerBlock pins the loader contract from the
// injector's side: a definition whose omitted container block the
// loader materialized with the default name carries no image, and the
// render fails on the unresolved image instead of silently keeping the
// chart default.
func TestEnvInjectorNoContainerBlock(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.InitContainer = nil
	definition.Spec.Controller.Deployment.Container = &toolApi.ContainerDefinition{
		Name: toolApi.DefaultMainContainerName,
	}

	var render enginetypes.PostRenderer = (&EnvInjector{
		Definition: definition,
		Images:     newResolvedImages(),
		Namespace:  "opendatahub",
	}).Run

	input, err := toolk8s.DecodeResources([]byte(deploymentWithoutInitYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(`container "manager" image unresolved in Deployment synthetic-controller`))
	g.Expect(resources).To(BeNil())
}

// TestEnvInjectorIgnoresNonDeploymentResources pins that only exact
// Deployment group/version/kind resources are visited: a Service, and
// a resource of the targeted name carrying a different GVK, pass
// through untouched.
func TestEnvInjectorIgnoresNonDeploymentResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

	batch := strings.Join([]string{
		serviceYAML,
		sameNameWrongGVKYAML,
		controllerDeploymentYAML,
	}, "\n---\n")

	input, err := toolk8s.DecodeResources([]byte(batch))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(3))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	service := findResource(resources, toolk8s.Service, types.NamespacedName{Name: "synthetic-service"})
	g.Expect(service).NotTo(BeNil(), "expected Service synthetic-service in the rendered output")
	g.Expect(service.Object).To(jq.Match(`.spec.ports[0].port == 8080`))

	lookalike := findResource(resources, schema.GroupVersionKind{Group: "apps", Version: "v1beta1", Kind: "Deployment"}, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(lookalike).NotTo(BeNil(), "expected apps/v1beta1 Deployment synthetic-controller in the rendered output")

	expected, err := toolk8s.DecodeResources([]byte(sameNameWrongGVKYAML))
	g.Expect(err).To(Succeed())
	g.Expect(expected).To(HaveLen(1))
	g.Expect(*lookalike).To(Equal(expected[0]))
}

func TestEnvInjectorRequiresControllerTarget(t *testing.T) {
	t.Parallel()
	for _, document := range []string{serviceYAML, sameNameWrongGVKYAML, otherDeploymentYAML} {
		t.Run(document[:20], func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)
			input, err := toolk8s.DecodeResources([]byte(document))
			g.Expect(err).To(Succeed())
			resources, err := newEnvInjector(newDefinition()).Run(t.Context(), input)
			g.Expect(err).To(MatchError(ContainSubstring("no rendered apps/v1 Deployment matched controller")))
			g.Expect(resources).To(BeNil())
		})
	}
}

// TestEnvInjectorInitContainerBranches pins the init container
// branches: a configured init container missing from the target —
// absent, empty, or differently named — fails the pass regardless of
// image resolution, an init container carrying its own image gets that
// image instead of the main one, an authored init image that does not
// resolve fails the pass, and no init target leaves the slice
// untouched.
func TestEnvInjectorInitContainerBranches(t *testing.T) {
	t.Parallel()

	t.Run("absent init slice errors", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

		input, err := toolk8s.DecodeResources([]byte(deploymentWithoutInitYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "copy-manifests" not found in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})

	t.Run("empty init slice errors", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

		input, err := toolk8s.DecodeResources([]byte(deploymentEmptyInitYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "copy-manifests" not found in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})

	t.Run("missing named init with resolved image errors", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

		input, err := toolk8s.DecodeResources([]byte(deploymentOtherInitYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "copy-manifests" not found in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})

	t.Run("missing named init with unresolved main image errors on the main image", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = (&EnvInjector{Definition: newDefinition()}).Run

		input, err := toolk8s.DecodeResources([]byte(deploymentOtherInitYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`container "manager" image unresolved in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})

	t.Run("init image distinct from main image", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definition := newDefinition()
		definition.Spec.Controller.Deployment.InitContainer.Image = "quay.io/example/init:v2"

		var render enginetypes.PostRenderer = newEnvInjector(definition).Run

		input, err := toolk8s.DecodeResources([]byte(controllerDeploymentYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(Succeed())

		deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
		g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
		g.Expect(deployment.Object).To(SatisfyAll(
			WithTransform(k8s.Container("manager"), HaveField("Image", "quay.io/example/controller:v1")),
			WithTransform(k8s.InitContainer("copy-manifests"), HaveField("Image", "quay.io/example/init:v2")),
		))
	})

	t.Run("init image reference resolves independently", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definition := newDefinition()
		definition.Spec.Controller.Deployment.InitContainer.Image = "{{RELATED_IMAGE_INIT}}"

		images := newResolvedImages()
		images["RELATED_IMAGE_INIT"] = "quay.io/example/init:v3"

		var render enginetypes.PostRenderer = (&EnvInjector{
			Definition: definition,
			Images:     images,
		}).Run

		input, err := toolk8s.DecodeResources([]byte(controllerDeploymentYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(Succeed())

		deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
		g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
		g.Expect(deployment.Object).To(SatisfyAll(
			WithTransform(k8s.Container("manager"), HaveField("Image", "quay.io/example/controller:v1")),
			WithTransform(k8s.InitContainer("copy-manifests"), HaveField("Image", "quay.io/example/init:v3")),
		))
	})

	t.Run("unresolved init image errors", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definition := newDefinition()
		definition.Spec.Controller.Deployment.InitContainer.Image = "{{RELATED_IMAGE_ABSENT}}"

		var render enginetypes.PostRenderer = newEnvInjector(definition).Run

		input, err := toolk8s.DecodeResources([]byte(controllerDeploymentYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "copy-manifests" image unresolved in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})

	t.Run("no init target leaves slice untouched", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definition := newDefinition()
		definition.Spec.Controller.Deployment.InitContainer = nil

		var render enginetypes.PostRenderer = (&EnvInjector{
			Definition: definition,
			Images:     newResolvedImages(),
		}).Run

		input, err := toolk8s.DecodeResources([]byte(controllerDeploymentYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(Succeed())

		deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
		g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
		g.Expect(deployment.Object).To(WithTransform(k8s.InitContainer("copy-manifests"),
			HaveField("Image", "registry.example/chart-init:v0")))
	})
}

// TestEnvInjectorEmptyCustomizationStillRoundTrips pins the error-only
// customization contract: a matched target with a found main container
// is converted back even when the resolved env list is empty — its
// unknown fields may be dropped — while a foreign Deployment keeps its
// original rendered map.
func TestEnvInjectorEmptyCustomizationStillRoundTrips(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.RelatedImages = nil
	definition.Spec.Controller.Deployment.InitContainer = nil
	definition.Spec.Controller.Deployment.Container.Image = "quay.io/example/controller:v1"
	definition.Spec.Controller.Deployment.Container.Env = nil

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: definition}).Run

	batch := strings.Join([]string{
		controllerDeploymentUnknownFieldYAML,
		otherDeploymentYAML,
	}, "\n---\n")

	input, err := toolk8s.DecodeResources([]byte(batch))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(2))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	// The valid target round-trips even though the env request carried
	// nothing: the sentinel extension is dropped, the image is the
	// literal, and the chart env is unchanged.
	own := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(own).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(own.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec | has("x-extension") | not`),
		WithTransform(k8s.Container("manager"), SatisfyAll(
			HaveField("Image", "quay.io/example/controller:v1"),
			WithTransform(envVarNames, Equal([]string{"CHART_ENV"})),
			WithTransform(k8s.EnvVar("CHART_ENV"), HaveField("Value", "chart-value")),
		)),
	))

	// The passthrough path keeps its original map.
	expected, err := toolk8s.DecodeResources([]byte(otherDeploymentYAML))
	g.Expect(err).To(Succeed())
	g.Expect(expected).To(HaveLen(1))
	g.Expect(resources[1]).To(Equal(expected[0]))
}

// TestEnvInjectorAlreadyCorrectValuesStillRoundTrips pins that a
// matched target already carrying the resolved images and the extra
// env value is still customized successfully — with no change count to
// rely on — and the applied values are identical to the rendered ones.
func TestEnvInjectorAlreadyCorrectValuesStillRoundTrips(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinition()).Run

	input, err := toolk8s.DecodeResources([]byte(controllerDeploymentAlreadyCorrectYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	deployment := findResource(resources, toolk8s.Deployment, types.NamespacedName{Name: "synthetic-controller"})
	g.Expect(deployment).NotTo(BeNil(), "expected Deployment synthetic-controller in the rendered output")
	g.Expect(deployment.Object).To(SatisfyAll(
		WithTransform(k8s.Container("manager"), SatisfyAll(
			HaveField("Image", "quay.io/example/controller:v1"),
			WithTransform(envVarNames, Equal([]string{
				"LOG_LEVEL", "RELATED_IMAGE_OPERAND", "APPLICATIONS_NAMESPACE",
				"MONITORING_NAMESPACE", "ODH_MODULE_OPERATOR_PLATFORM_TYPE",
			})),
			WithTransform(k8s.EnvVar("LOG_LEVEL"), HaveField("Value", "debug")),
		)),
		WithTransform(k8s.InitContainer("copy-manifests"), HaveField("Image", "quay.io/example/controller:v1")),
	))
}

// TestEnvInjectorDoesNotMutateInputs pins that the injector only reads
// the definition and the image snapshot the deployer owns: after a full
// pass over a rendered batch, both are exactly as supplied.
func TestEnvInjectorDoesNotMutateInputs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	injector := newEnvInjector(definition)

	var render enginetypes.PostRenderer = injector.Run

	input, err := toolk8s.DecodeResources([]byte(controllerDeploymentYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())
	g.Expect(resources).To(HaveLen(1))

	g.Expect(*definition).To(Equal(*newDefinition()))
	g.Expect(injector.Images).To(Equal(newResolvedImages()))
}
