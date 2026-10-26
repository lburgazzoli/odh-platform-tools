package adapt

import (
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// newDefinition returns the synthetic module definition carrying the
// full controller deployment descriptor: a token image, one extra env
// entry, an init container target, and one related image.
func newDefinition() toolApi.PlatformModuleDefinition {
	return toolApi.PlatformModuleDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "synthetic",
		},
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
			},
			RelatedImages: []string{"RELATED_IMAGE_OPERAND"},
			Controller: toolApi.Controller{
				Deployment: toolApi.ControllerDeployment{
					Name: "synthetic-controller",
					Container: &toolApi.ContainerDefinition{
						Name:  "manager",
						Image: "{{RELATED_IMAGE_CONTROLLER}}",
						Env: []toolApi.EnvVar{
							{Name: "LOG_LEVEL", Value: new("debug")},
						},
					},
					InitContainer: &toolApi.ContainerTarget{
						Name: "copy-manifests",
					},
				},
			},
		},
	}
}

// newDefinitionPointer returns a pointer to a fresh standard
// definition, as the runner hands one to the injectors.
func newDefinitionPointer() *toolApi.PlatformModuleDefinition {
	definition := newDefinition()
	return &definition
}

// newResolvedImages returns the fully resolved image snapshot.
func newResolvedImages() map[string]string {
	return map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
		"RELATED_IMAGE_OPERAND":    "quay.io/example/operand:v1",
	}
}

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

// resource decodes one YAML document into an unstructured object.
func resource(t *testing.T, document string) unstructured.Unstructured {
	t.Helper()

	var object unstructured.Unstructured
	if err := yaml.Unmarshal([]byte(document), &object); err != nil {
		t.Fatal(err)
	}

	return object
}

// findResource returns the resource of the given kind and name.
func findResource(
	t *testing.T,
	resources []unstructured.Unstructured,
	kind string,
	name string,
) unstructured.Unstructured {
	t.Helper()

	for _, resource := range resources {
		if resource.GetKind() == kind && resource.GetName() == name {
			return resource
		}
	}

	t.Fatalf("no %s %s in resources: %v", kind, name, resources)

	return unstructured.Unstructured{}
}

// TestInjectorsComposeOverOneBatch pins that the two post-renderers
// compose over a single private resource batch, as the operator's
// controller composes its chain — sharing one definition pointer.
//
// Operator source: internal/controller/modules/modules_controller.go
// (commonActions chain), commit 5a1d17f57b32.
func TestInjectorsComposeOverOneBatch(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinitionPointer()

	resources := []unstructured.Unstructured{
		resource(t, controllerDeploymentYAML),
		resource(t, serviceYAML),
	}

	var envRender enginetypes.PostRenderer = newEnvInjector(definition).Run
	var platformRender enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            definition,
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources, err := envRender(t.Context(), resources)
	g.Expect(err).To(Succeed())
	resources, err = platformRender(t.Context(), resources)
	g.Expect(err).To(Succeed())

	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(jq.Match(
		`.spec.template.spec.containers[] | select(.name == "manager") | .image == "quay.io/example/controller:v1"`,
	))

	configMap := findResource(t, resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "opendatahub"`),
		jq.Match(`.data.platformVersion == "2.19.0"`),
	))
}

// TestInjectorsDoNotMutateDefinitionOrSnapshot pins that both
// injectors only read the definition and the image snapshot the
// runner owns: after a full env and platform pass over a rendered
// batch, both are exactly as supplied.
func TestInjectorsDoNotMutateDefinitionOrSnapshot(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinitionPointer()
	snapshot := newResolvedImages()

	var envRender enginetypes.PostRenderer = (&EnvInjector{
		Definition:            definition,
		Images:                snapshot,
		ApplicationsNamespace: "opendatahub",
		MonitoringNamespace:   "opendatahub-monitoring",
		PlatformType:          "self-managed",
	}).Run
	var platformRender enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            definition,
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources := []unstructured.Unstructured{
		resource(t, controllerDeploymentYAML),
		resource(t, serviceYAML),
	}

	resources, err := envRender(t.Context(), resources)
	g.Expect(err).To(Succeed())
	resources, err = platformRender(t.Context(), resources)
	g.Expect(err).To(Succeed())
	g.Expect(resources).To(HaveLen(3))

	g.Expect(*definition).To(Equal(newDefinition()))
	g.Expect(snapshot).To(Equal(newResolvedImages()))
}

// TestInjectorsPassGateConfigMapsThrough pins that a ConfigMap
// carrying the operator's upgrade-gate label is an ordinary rendered
// resource for both injectors: it is neither extracted, reported, nor
// otherwise touched — no gates subsystem exists here.
//
// Operator gate label: pkg/controller/gates/gates.go
// (UpgradeGateLabel), commit 5a1d17f57b32.
func TestInjectorsPassGateConfigMapsThrough(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinitionPointer()

	var envRender enginetypes.PostRenderer = newEnvInjector(definition).Run
	var platformRender enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            definition,
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources := []unstructured.Unstructured{resource(t, gateConfigMapYAML)}

	resources, err := envRender(t.Context(), resources)
	g.Expect(err).To(Succeed())
	resources, err = platformRender(t.Context(), resources)
	g.Expect(err).To(Succeed())

	// The gate ConfigMap passes through untouched; the only addition
	// is the ordinary platform config ConfigMap.
	g.Expect(resources).To(HaveLen(2))
	g.Expect(resources[0]).To(Equal(resource(t, gateConfigMapYAML)))

	configMap := findResource(t, resources[1:], "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap.Object).To(jq.Match(`.data.platformVersion == "2.19.0"`))
}
