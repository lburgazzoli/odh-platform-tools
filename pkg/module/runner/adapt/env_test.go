package adapt

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
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

// controllerDeploymentMalformedEnvYAML is the controller Deployment
// whose main container env is not a list.
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

// deploymentWithoutContainersYAML is a foreign Deployment with no
// containers at all.
const deploymentWithoutContainersYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: other-controller
spec:
  template:
    spec: {}
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

// serviceYAML is a non-Deployment rendered resource.
const serviceYAML = `apiVersion: v1
kind: Service
metadata:
  name: synthetic-service
spec:
  ports:
  - port: 8080
`

// TestInjectModuleEnv pins the happy path: the resolved controller
// image overrides the main and init containers, and the env entries
// append after the chart env in operator precedence order — related
// images, sorted extra env, then the nonempty platform env.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectModuleEnv, injectEnvVarsIntoDeployment), commit 5a1d17f57b32.
func TestInjectModuleEnv(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(resource(t, controllerDeploymentYAML))
	g.Expect(newEnvAction()(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
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

// TestInjectModuleEnvLiteralImage pins that a literal controller image
// overrides without any image-source lookup.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, mi.ControllerImage), commit 5a1d17f57b32.
func TestInjectModuleEnvLiteralImage(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.Container.Image = "quay.io/example/controller:v9"

	request := newRequest(resource(t, controllerDeploymentYAML))

	action := InjectModuleEnv(definition, nil, "", "", "")
	g.Expect(action(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "quay.io/example/controller:v9"`),
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["CHART_ENV","LOG_LEVEL"]`),
	))
}

// TestInjectModuleEnvUnresolvedImages pins that missing image sources
// keep the rendered defaults for the main container, the init
// container, and the related-image env entries.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, os.Getenv empty branches), commit 5a1d17f57b32.
func TestInjectModuleEnvUnresolvedImages(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(resource(t, controllerDeploymentYAML))

	action := InjectModuleEnv(newDefinition(), nil, "", "", "")
	g.Expect(action(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "registry.example/chart-controller:v0"`),
		jq.Match(`.spec.template.spec.initContainers[] | select(.name == "copy-manifests")
			| .image == "registry.example/chart-init:v0"`),
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["CHART_ENV","LOG_LEVEL"]`),
	))
}

// TestInjectModuleEnvOverridesExistingEnvAndRemovesValueFrom pins that
// an existing chart env entry is overridden in place and its valueFrom
// block removed.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (setOrOverrideEnv), commit 5a1d17f57b32.
func TestInjectModuleEnvOverridesExistingEnvAndRemovesValueFrom(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(resource(t, controllerDeploymentValueFromYAML))
	g.Expect(newEnvAction()(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["RELATED_IMAGE_OPERAND","LOG_LEVEL","APPLICATIONS_NAMESPACE","MONITORING_NAMESPACE","ODH_MODULE_OPERATOR_PLATFORM_TYPE"]`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "RELATED_IMAGE_OPERAND") | .value == "quay.io/example/operand:v1"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "RELATED_IMAGE_OPERAND") | has("valueFrom") | not`),
	))
}

// TestInjectModuleEnvPrecedence pins the operator's semantic
// precedence when names collide: explicit extra env overrides a
// related image value, and the platform env overrides an extra env
// value of the same name.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment injection order), commit 5a1d17f57b32.
func TestInjectModuleEnvPrecedence(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.Container.Env = []v1alpha1.EnvVar{
		{Name: "RELATED_IMAGE_OPERAND", Value: new("extra-wins")},
		{Name: "APPLICATIONS_NAMESPACE", Value: new("extra-ns")},
	}

	request := newRequest(resource(t, deploymentWithoutInitYAML))

	action := InjectModuleEnv(definition, newResolvedImages(), "opendatahub", "", "")
	g.Expect(action(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "RELATED_IMAGE_OPERAND") | .value == "extra-wins"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "APPLICATIONS_NAMESPACE") | .value == "opendatahub"`),
	))
}

// TestInjectModuleEnvExtraEnvListMapSemantics pins the list-to-map
// projection of the descriptor env list: later duplicates win, and an
// absent value projects to the empty string.
//
// Operator source: internal/controller/modules/modules_controller.go
// (provisionModules ModuleImages.ExtraEnv build), commit 5a1d17f57b32.
func TestInjectModuleEnvExtraEnvListMapSemantics(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.Container.Env = []v1alpha1.EnvVar{
		{Name: "ZETA", Value: new("one")},
		{Name: "ALPHA"},
		{Name: "ALPHA", Value: new("two")},
		{Name: "BRAVO"},
	}

	request := newRequest(resource(t, deploymentWithoutInitYAML))

	action := InjectModuleEnv(definition, nil, "", "", "")
	g.Expect(action(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "ZETA") | .value == "one"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "ALPHA") | .value == "two"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "BRAVO") | .value == ""`),
	))
}

// TestInjectModuleEnvPlaceholderValueStaysLiteral pins that an
// extra-env value looking like a replacement token is injected as
// authored, with no interpolation.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, mi.ExtraEnv), commit 5a1d17f57b32.
func TestInjectModuleEnvPlaceholderValueStaysLiteral(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.Container.Env = []v1alpha1.EnvVar{
		{Name: "LOG_LEVEL", Value: new("{{LOG_LEVEL}}")},
	}

	request := newRequest(resource(t, deploymentWithoutInitYAML))

	action := InjectModuleEnv(definition, nil, "", "", "")
	g.Expect(action(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
		| .env[] | select(.name == "LOG_LEVEL") | .value == "{{LOG_LEVEL}}"`))
}

// TestInjectModuleEnvSortedExtraEnv pins that map-derived extra env
// entries append in sorted key order, keeping the output
// deterministic without changing the operator's semantic precedence.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, mi.ExtraEnv range), commit 5a1d17f57b32.
func TestInjectModuleEnvSortedExtraEnv(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.Container.Env = []v1alpha1.EnvVar{
		{Name: "ZETA", Value: new("z")},
		{Name: "ALPHA", Value: new("a")},
	}

	request := newRequest(resource(t, deploymentWithoutInitYAML))

	action := InjectModuleEnv(definition, nil, "", "", "")
	g.Expect(action(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
		== ["CHART_ENV","ALPHA","ZETA"]`))
}

// TestInjectModuleEnvContainerNameFallback pins that an empty
// container name targets the operator manager default.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, defaultContainerName), commit 5a1d17f57b32.
func TestInjectModuleEnvContainerNameFallback(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.Container.Name = ""

	request := newRequest(resource(t, deploymentWithoutInitYAML))
	g.Expect(newEnvAction()(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "quay.io/example/controller:v1"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .env[] | select(.name == "LOG_LEVEL") | .value == "debug"`),
	))
}

// TestInjectModuleEnvDeploymentNameFallback pins that an empty
// deployment name falls back to the module name — the Helm release
// name for this tool.
//
// Operator source: internal/controller/modules/modules_controller.go
// (ModuleImages.DeploymentName build), commit 5a1d17f57b32.
func TestInjectModuleEnvDeploymentNameFallback(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.Name = ""

	request := newRequest(resource(t, moduleNamedDeploymentYAML))

	action := InjectModuleEnv(definition, newResolvedImages(), "", "", "")
	g.Expect(action(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic")
	g.Expect(deployment.Object).To(jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
		| .image == "quay.io/example/controller:v1"`))
}

// TestInjectModuleEnvSkipsOtherDeployments pins that a foreign
// Deployment with valid containers is left untouched, while the
// module's own Deployment is adapted.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, mi.DeploymentName mismatch), commit 5a1d17f57b32.
func TestInjectModuleEnvSkipsOtherDeployments(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(
		resource(t, controllerDeploymentYAML),
		resource(t, otherDeploymentYAML),
	)
	g.Expect(newEnvAction()(t.Context(), request)).To(Succeed())

	other := findResource(t, request.Resources, "Deployment", "other-controller")
	g.Expect(other.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "registry.example/other:v0"`),
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager") | has("env") | not`),
	))

	own := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(own.Object).To(jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
		| .image == "quay.io/example/controller:v1"`))
}

// TestInjectModuleEnvDeploymentWithoutContainersErrors pins that a
// Deployment without containers errors even when it is not the
// module's own: upstream reads every Deployment's containers before
// matching names.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, no-containers error), commit 5a1d17f57b32.
func TestInjectModuleEnvDeploymentWithoutContainersErrors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(resource(t, deploymentWithoutContainersYAML))

	err := newEnvAction()(t.Context(), request)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("deployment other-controller has no containers"))
}

// TestInjectModuleEnvMalformedContainersErrors pins that a non-list
// containers field errors with the unstructured accessor's error.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, NestedSlice error), commit 5a1d17f57b32.
func TestInjectModuleEnvMalformedContainersErrors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(resource(t, deploymentMalformedContainersYAML))

	err := newEnvAction()(t.Context(), request)
	g.Expect(err).To(HaveOccurred())
}

// TestInjectModuleEnvMalformedEnvReplaced pins that a malformed
// existing env value is treated as empty and replaced by the injected
// entries.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, existingEnv type assertion), commit 5a1d17f57b32.
func TestInjectModuleEnvMalformedEnvReplaced(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(resource(t, controllerDeploymentMalformedEnvYAML))
	g.Expect(newEnvAction()(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
		== ["RELATED_IMAGE_OPERAND","LOG_LEVEL","APPLICATIONS_NAMESPACE","MONITORING_NAMESPACE","ODH_MODULE_OPERATOR_PLATFORM_TYPE"]`))
}

// TestInjectModuleEnvNoContainerBlock pins that a definition without a
// container block injects no image and no extra env, while the
// platform env still lands on the default-named container.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectEnvVarsIntoDeployment, mi.ControllerImage empty), commit 5a1d17f57b32.
func TestInjectModuleEnvNoContainerBlock(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()
	definition.Spec.Controller.Deployment.Container = nil

	request := newRequest(resource(t, deploymentWithoutInitYAML))

	action := InjectModuleEnv(definition, newResolvedImages(), "opendatahub", "", "")
	g.Expect(action(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
			| .image == "registry.example/chart-controller:v0"`),
		jq.Match(`[.spec.template.spec.containers[] | select(.name == "manager") | .env[].name]
			== ["CHART_ENV","RELATED_IMAGE_OPERAND","APPLICATIONS_NAMESPACE"]`),
	))
}

// TestInjectModuleEnvIgnoresNonDeploymentResources pins that only
// exact Deployment group/version/kind resources are visited.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (isDeployment), commit 5a1d17f57b32.
func TestInjectModuleEnvIgnoresNonDeploymentResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(resource(t, serviceYAML))
	g.Expect(newEnvAction()(t.Context(), request)).To(Succeed())

	service := findResource(t, request.Resources, "Service", "synthetic-service")
	g.Expect(service.Object).To(jq.Match(`.spec.ports[0].port == 8080`))
}

// TestInjectModuleEnvInitContainerBranches pins the init container
// image injection branches: an absent initContainers list skips, a
// configured name missing from a nonempty list errors only when the
// controller image resolved, and no init target leaves the slice
// untouched.
//
// Operator source: internal/controller/modules/modules_controller_actions_inject_env.go
// (injectInitContainerImage), commit 5a1d17f57b32.
func TestInjectModuleEnvInitContainerBranches(t *testing.T) {
	t.Parallel()

	t.Run("absent init slice skips", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		request := newRequest(resource(t, deploymentWithoutInitYAML))
		g.Expect(newEnvAction()(t.Context(), request)).To(Succeed())

		deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
		g.Expect(deployment.Object).To(SatisfyAll(
			jq.Match(`.spec.template.spec.containers[] | select(.name == "manager")
				| .image == "quay.io/example/controller:v1"`),
			jq.Match(`.spec.template.spec | has("initContainers") | not`),
		))
	})

	t.Run("missing named init with resolved image errors", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		request := newRequest(resource(t, deploymentOtherInitYAML))

		err := newEnvAction()(t.Context(), request)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "copy-manifests" not found in Deployment synthetic-controller`))
	})

	t.Run("missing named init with unresolved image skips", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		request := newRequest(resource(t, deploymentOtherInitYAML))

		action := InjectModuleEnv(newDefinition(), nil, "", "", "")
		g.Expect(action(t.Context(), request)).To(Succeed())

		deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
		g.Expect(deployment.Object).To(jq.Match(`.spec.template.spec.initContainers[] | select(.name == "other-init")
			| .image == "registry.example/other-init:v0"`))
	})

	t.Run("no init target leaves slice untouched", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definition := newDefinition()
		definition.Spec.Controller.Deployment.InitContainer = nil

		request := newRequest(resource(t, controllerDeploymentYAML))

		action := InjectModuleEnv(definition, newResolvedImages(), "", "", "")
		g.Expect(action(t.Context(), request)).To(Succeed())

		deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
		g.Expect(deployment.Object).To(jq.Match(`.spec.template.spec.initContainers[] | select(.name == "copy-manifests")
			| .image == "registry.example/chart-init:v0"`))
	})
}
