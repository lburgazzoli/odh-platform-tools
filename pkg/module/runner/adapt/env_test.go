package adapt

import (
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

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
		Definition:            definition,
		Images:                newResolvedImages(),
		ApplicationsNamespace: "opendatahub",
		MonitoringNamespace:   "opendatahub-monitoring",
		PlatformType:          "self-managed",
	}
}

// TestEnvInjector pins the happy path: the resolved controller image
// overrides the main and init containers, and the env entries append
// after the chart env in operator precedence order — related images,
// sorted extra env, then the nonempty platform env.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectModuleEnv, injectEnvVarsIntoDeployment), commit 5a1d17f57b32.
func TestEnvInjector(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "quay.io/example/controller:v1"`),
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["CHART_ENV","RELATED_IMAGE_OPERAND","LOG_LEVEL","APPLICATIONS_NAMESPACE","MONITORING_NAMESPACE","ODH_MODULE_OPERATOR_PLATFORM_TYPE"]`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "RELATED_IMAGE_OPERAND") | .value == "quay.io/example/operand:v1"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "LOG_LEVEL") | .value == "debug"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "APPLICATIONS_NAMESPACE") | .value == "opendatahub"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "MONITORING_NAMESPACE") | .value == "opendatahub-monitoring"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "ODH_MODULE_OPERATOR_PLATFORM_TYPE") | .value == "self-managed"`),
		jq.Match(`.spec.template.spec.initContainers[] | select(.name == "copy-manifests")
			| .image == "quay.io/example/controller:v1"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "sidecar")
			| .image == "registry.example/chart-sidecar:v0"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "sidecar") | has("env") | not`),
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

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: &definition}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "quay.io/example/controller:v9"`),
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["CHART_ENV","LOG_LEVEL"]`),
	))
}

// TestEnvInjectorUnresolvedImages pins that missing image sources keep
// the rendered defaults for the main container, the init container,
// and the related-image env entries.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, os.Getenv empty branches), commit 5a1d17f57b32.
func TestEnvInjectorUnresolvedImages(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: newDefinitionPointer()}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "registry.example/chart-controller:v0"`),
		jq.Match(`.spec.template.spec.initContainers[] | select(.name == "copy-manifests")
			| .image == "registry.example/chart-init:v0"`),
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["CHART_ENV","LOG_LEVEL"]`),
	))
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

	var render enginetypes.PostRenderer = newEnvInjector(&definition).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentValueFromYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["RELATED_IMAGE_OPERAND","LOG_LEVEL","APPLICATIONS_NAMESPACE","MONITORING_NAMESPACE","ODH_MODULE_OPERATOR_PLATFORM_TYPE"]`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "RELATED_IMAGE_OPERAND") | .value == "quay.io/example/operand:v1"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "RELATED_IMAGE_OPERAND") | has("valueFrom") | not`),
	))
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
		Definition:            &definition,
		Images:                newResolvedImages(),
		ApplicationsNamespace: "opendatahub",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentWithoutInitYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "RELATED_IMAGE_OPERAND") | .value == "extra-wins"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "APPLICATIONS_NAMESPACE") | .value == "opendatahub"`),
	))
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
	definition.Spec.Controller.Deployment.Container.Env = []toolApi.EnvVar{
		{Name: "ZETA", Value: new("one")},
		{Name: "ALPHA"},
		{Name: "ALPHA", Value: new("two")},
		{Name: "BRAVO"},
	}

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: &definition}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentWithoutInitYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "ZETA") | .value == "one"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "ALPHA") | .value == "two"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "BRAVO") | (.value // "") == ""`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "BRAVO") | has("valueFrom") | not`),
	))
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
	definition.Spec.Controller.Deployment.Container.Env = []toolApi.EnvVar{
		{Name: "LOG_LEVEL", Value: new("{{LOG_LEVEL}}")},
	}

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: &definition}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentWithoutInitYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
		| .env[] | select(.name == "LOG_LEVEL") | .value == "{{LOG_LEVEL}}"`))
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
	definition.Spec.Controller.Deployment.Container.Env = []toolApi.EnvVar{
		{Name: "ZETA", Value: new("z")},
		{Name: "ALPHA", Value: new("a")},
	}

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: &definition}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentWithoutInitYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
		== ["CHART_ENV","ALPHA","ZETA"]`))
}

// TestEnvInjectorContainerNameFallback pins that an empty container
// name targets the operator manager default.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, defaultContainerName), commit 5a1d17f57b32.
func TestEnvInjectorContainerNameFallback(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.InitContainer = nil
	definition.Spec.Controller.Deployment.Container.Name = ""

	var render enginetypes.PostRenderer = newEnvInjector(&definition).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentWithoutInitYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "quay.io/example/controller:v1"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "LOG_LEVEL") | .value == "debug"`),
	))
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
		Definition: &definition,
		Images:     newResolvedImages(),
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, moduleNamedDeploymentYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic")
	g.Expect(deployment.Object).To(jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
		| .image == "quay.io/example/controller:v1"`))
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

	var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{
		resource(t, controllerDeploymentYAML),
		resource(t, otherDeploymentYAML),
	})
	g.Expect(err).To(Succeed())

	other := findResource(t, resources, "Deployment", "other-controller")
	g.Expect(other.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "registry.example/other:v0"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager") | has("env") | not`),
	))

	own := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(own.Object).To(jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
		| .image == "quay.io/example/controller:v1"`))
}

// TestEnvInjectorForeignMalformedDeploymentsPassThrough pins that
// foreign Deployments — without containers, or with malformed
// containers or env — are never converted: they remain byte-for-byte
// as rendered and do not fail the pass, while the module's own
// Deployment in the same batch is still adapted.
func TestEnvInjectorForeignMalformedDeploymentsPassThrough(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{
		resource(t, controllerDeploymentYAML),
		resource(t, foreignDeploymentWithoutContainersYAML),
		resource(t, foreignMalformedContainersYAML),
		resource(t, foreignMalformedEnvYAML),
	})
	g.Expect(err).To(Succeed())

	g.Expect(resources[1]).To(Equal(resource(t, foreignDeploymentWithoutContainersYAML)))
	g.Expect(resources[2]).To(Equal(resource(t, foreignMalformedContainersYAML)))
	g.Expect(resources[3]).To(Equal(resource(t, foreignMalformedEnvYAML)))

	own := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(own.Object).To(jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
		| .image == "quay.io/example/controller:v1"`))
}

// TestEnvInjectorTargetWithoutContainersErrors pins that the module's
// own Deployment without containers — absent or empty — errors, and a
// failed pass returns no successful output.
func TestEnvInjectorTargetWithoutContainersErrors(t *testing.T) {
	t.Parallel()

	t.Run("absent containers", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

		resources, err := render(t.Context(), []unstructured.Unstructured{
			resource(t, deploymentTargetWithoutContainersYAML),
		})
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("deployment synthetic-controller has no containers"))
		g.Expect(resources).To(BeNil())
	})

	t.Run("empty containers", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

		resources, err := render(t.Context(), []unstructured.Unstructured{
			resource(t, deploymentTargetEmptyContainersYAML),
		})
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

	var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentMalformedContainersYAML)})
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

	var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentMalformedEnvYAML)})
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

	var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentMalformedReplicasYAML)})
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

	var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentWrongMainContainerYAML)})
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

	var render enginetypes.PostRenderer = newEnvInjector(&definition).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentUnknownFieldYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec | has("x-extension") | not`),
		jq.Match(`.metadata.labels.app == "synthetic"`),
		jq.Match(`.spec.replicas == 2`),
		jq.Match(`.spec.template.metadata.labels.app == "synthetic"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "quay.io/example/controller:v1"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "CHART_ENV") | .value == "chart-value"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "sidecar")
			| .image == "registry.example/chart-sidecar:v0"`),
	))
}

// TestEnvInjectorNoContainerBlock pins that a definition without a
// container block injects no image and no extra env, while the
// platform env still lands on the default-named container.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, mi.ControllerImage empty), commit 5a1d17f57b32.
func TestEnvInjectorNoContainerBlock(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.InitContainer = nil
	definition.Spec.Controller.Deployment.Container = nil

	var render enginetypes.PostRenderer = (&EnvInjector{
		Definition:            &definition,
		Images:                newResolvedImages(),
		ApplicationsNamespace: "opendatahub",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentWithoutInitYAML)})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "registry.example/chart-controller:v0"`),
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["CHART_ENV","RELATED_IMAGE_OPERAND","APPLICATIONS_NAMESPACE"]`),
	))
}

// TestEnvInjectorIgnoresNonDeploymentResources pins that only exact
// Deployment group/version/kind resources are visited: a Service, and
// a resource of the targeted name carrying a different GVK, pass
// through untouched.
func TestEnvInjectorIgnoresNonDeploymentResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{
		resource(t, serviceYAML),
		resource(t, sameNameWrongGVKYAML),
	})
	g.Expect(err).To(Succeed())

	service := findResource(t, resources, "Service", "synthetic-service")
	g.Expect(service.Object).To(jq.Match(`.spec.ports[0].port == 8080`))

	g.Expect(resources[1]).To(Equal(resource(t, sameNameWrongGVKYAML)))
}

// TestEnvInjectorInitContainerBranches pins the init container
// branches: a configured init container missing from the target —
// absent, empty, or differently named — fails the pass regardless of
// image resolution, an init container carrying its own image gets that
// image instead of the main one, and no init target leaves the slice
// untouched.
func TestEnvInjectorInitContainerBranches(t *testing.T) {
	t.Parallel()

	t.Run("absent init slice errors", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

		resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentWithoutInitYAML)})
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "copy-manifests" not found in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})

	t.Run("empty init slice errors", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

		resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentEmptyInitYAML)})
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "copy-manifests" not found in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})

	t.Run("missing named init with resolved image errors", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

		resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentOtherInitYAML)})
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "copy-manifests" not found in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})

	t.Run("missing named init with unresolved image errors", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = (&EnvInjector{Definition: newDefinitionPointer()}).Run

		resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, deploymentOtherInitYAML)})
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "copy-manifests" not found in Deployment synthetic-controller`))
		g.Expect(resources).To(BeNil())
	})

	t.Run("init image distinct from main image", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definition := newDefinition()
		definition.Spec.Controller.Deployment.InitContainer.Image = "quay.io/example/init:v2"

		var render enginetypes.PostRenderer = newEnvInjector(&definition).Run

		resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentYAML)})
		g.Expect(err).To(Succeed())

		deployment := findResource(t, resources, "Deployment", "synthetic-controller")
		g.Expect(deployment.Object).To(SatisfyAll(
			jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
				| .image == "quay.io/example/controller:v1"`),
			jq.Match(`.spec.template.spec.initContainers[] | select(.name == "copy-manifests")
				| .image == "quay.io/example/init:v2"`),
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
			Definition: &definition,
			Images:     images,
		}).Run

		resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentYAML)})
		g.Expect(err).To(Succeed())

		deployment := findResource(t, resources, "Deployment", "synthetic-controller")
		g.Expect(deployment.Object).To(SatisfyAll(
			jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
				| .image == "quay.io/example/controller:v1"`),
			jq.Match(`.spec.template.spec.initContainers[] | select(.name == "copy-manifests")
				| .image == "quay.io/example/init:v3"`),
		))
	})

	t.Run("unresolved init image keeps chart default", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definition := newDefinition()
		definition.Spec.Controller.Deployment.InitContainer.Image = "{{RELATED_IMAGE_ABSENT}}"

		var render enginetypes.PostRenderer = newEnvInjector(&definition).Run

		resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentYAML)})
		g.Expect(err).To(Succeed())

		deployment := findResource(t, resources, "Deployment", "synthetic-controller")
		g.Expect(deployment.Object).To(SatisfyAll(
			jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
				| .image == "quay.io/example/controller:v1"`),
			jq.Match(`.spec.template.spec.initContainers[] | select(.name == "copy-manifests")
				| .image == "registry.example/chart-init:v0"`),
		))
	})

	t.Run("no init target leaves slice untouched", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definition := newDefinition()
		definition.Spec.Controller.Deployment.InitContainer = nil

		var render enginetypes.PostRenderer = (&EnvInjector{
			Definition: &definition,
			Images:     newResolvedImages(),
		}).Run

		resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentYAML)})
		g.Expect(err).To(Succeed())

		deployment := findResource(t, resources, "Deployment", "synthetic-controller")
		g.Expect(deployment.Object).To(jq.Match(`.spec.template.spec.initContainers[] | select(.name == "copy-manifests")
			| .image == "registry.example/chart-init:v0"`))
	})
}

// TestEnvInjectorEmptyCustomizationStillRoundTrips pins the error-only
// customization contract: a matched target with a found main container
// is converted back even when the image is unresolved and the resolved
// env list is empty — its unknown fields may be dropped — while a
// foreign Deployment keeps its original rendered map.
func TestEnvInjectorEmptyCustomizationStillRoundTrips(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.RelatedImages = nil
	definition.Spec.Controller.Deployment.InitContainer = nil
	definition.Spec.Controller.Deployment.Container.Env = nil

	var render enginetypes.PostRenderer = (&EnvInjector{Definition: &definition}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{
		resource(t, controllerDeploymentUnknownFieldYAML),
		resource(t, otherDeploymentYAML),
	})
	g.Expect(err).To(Succeed())

	// The valid target round-trips even though no image or env request
	// had an effect: the sentinel extension is dropped and the chart
	// image and env are unchanged.
	own := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(own.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec | has("x-extension") | not`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "registry.example/chart-controller:v0"`),
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["CHART_ENV"]`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "CHART_ENV") | .value == "chart-value"`),
	))

	// The passthrough path keeps its original map.
	g.Expect(resources[1]).To(Equal(resource(t, otherDeploymentYAML)))
}

// TestEnvInjectorAlreadyCorrectValuesStillRoundTrips pins that a
// matched target already carrying the resolved images and the extra
// env value is still customized successfully — with no change count to
// rely on — and the applied values are identical to the rendered ones.
func TestEnvInjectorAlreadyCorrectValuesStillRoundTrips(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newEnvInjector(newDefinitionPointer()).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{
		resource(t, controllerDeploymentAlreadyCorrectYAML),
	})
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "quay.io/example/controller:v1"`),
		jq.Match(`.spec.template.spec.initContainers[] | select(.name == "copy-manifests")
			| .image == "quay.io/example/controller:v1"`),
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["LOG_LEVEL","RELATED_IMAGE_OPERAND","APPLICATIONS_NAMESPACE","MONITORING_NAMESPACE","ODH_MODULE_OPERATOR_PLATFORM_TYPE"]`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "LOG_LEVEL") | .value == "debug"`),
	))
}

// TestEnvInjectorDoesNotMutateInputs pins that the injector only reads
// the definition and the image snapshot the runner owns: after a full
// pass over a rendered batch, both are exactly as supplied.
func TestEnvInjectorDoesNotMutateInputs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinitionPointer()
	injector := newEnvInjector(definition)

	var render enginetypes.PostRenderer = injector.Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, controllerDeploymentYAML)})
	g.Expect(err).To(Succeed())
	g.Expect(resources).To(HaveLen(1))

	g.Expect(*definition).To(Equal(newDefinition()))
	g.Expect(injector.Images).To(Equal(newResolvedImages()))
}
