package adapt

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	fwactions "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions"
	fwtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// newDefinition returns the synthetic module definition carrying the
// full controller deployment descriptor: a token image, one extra env
// entry, an init container target, and one related image.
func newDefinition() v1alpha1.PlatformModuleDefinition {
	return v1alpha1.PlatformModuleDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "synthetic",
		},
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
			},
			RelatedImages: []string{"RELATED_IMAGE_OPERAND"},
			Controller: v1alpha1.Controller{
				Deployment: v1alpha1.ControllerDeployment{
					Name: "synthetic-controller",
					Container: &v1alpha1.ContainerDefinition{
						Name:  "manager",
						Image: "{{RELATED_IMAGE_CONTROLLER}}",
						Env: []v1alpha1.EnvVar{
							{Name: "LOG_LEVEL", Value: new("debug")},
						},
					},
					InitContainer: &v1alpha1.ContainerTarget{
						Name: "copy-manifests",
					},
				},
			},
		},
	}
}

// newResolvedImages returns the fully resolved image snapshot.
func newResolvedImages() map[string]string {
	return map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
		"RELATED_IMAGE_OPERAND":    "quay.io/example/operand:v1",
	}
}

// newRequest returns a framework reconciliation request carrying only
// the rendered resources, as the render action leaves it.
func newRequest(resources ...unstructured.Unstructured) *fwtypes.ReconciliationRequest {
	return &fwtypes.ReconciliationRequest{
		Resources: resources,
	}
}

// setRelease sets the request's release version through JSON, so no
// semver package import is needed to populate the framework type.
func setRelease(t *testing.T, request *fwtypes.ReconciliationRequest, version string) {
	t.Helper()

	if err := json.Unmarshal([]byte(`{"version":"`+version+`"}`), &request.Release); err != nil {
		t.Fatal(err)
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

// newEnvAction returns the env action for the standard definition and
// fully populated runtime inputs.
func newEnvAction() fwactions.Fn {
	return InjectModuleEnv(
		newDefinition(),
		newResolvedImages(),
		"opendatahub",
		"opendatahub-monitoring",
		"self-managed",
	)
}

// TestActionsComposeOverOneRequest pins that the two actions compose
// over a single framework request, as the operator's controller
// composes its chain.
//
// Operator source: internal/controller/modules/modules_controller.go
// (commonActions chain), commit 5a1d17f57b32.
func TestActionsComposeOverOneRequest(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(
		resource(t, controllerDeploymentYAML),
		resource(t, serviceYAML),
	)

	env := newEnvAction()
	platform := InjectPlatformConfig(newDefinition(), "opendatahub")

	g.Expect(env(t.Context(), request)).To(Succeed())

	setRelease(t, request, "2.19.0")
	g.Expect(platform(t.Context(), request)).To(Succeed())

	deployment := findResource(t, request.Resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.Object).To(jq.Match(
		`.spec.template.spec.containers[] | select(.name == "manager") | .image == "quay.io/example/controller:v1"`,
	))

	configMap := findResource(t, request.Resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "opendatahub"`),
		jq.Match(`.data.platformVersion == "2.19.0"`),
	))
}
