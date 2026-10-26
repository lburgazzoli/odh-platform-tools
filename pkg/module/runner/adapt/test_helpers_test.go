package adapt

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

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

// serviceYAML is a non-Deployment rendered resource.
const serviceYAML = `apiVersion: v1
kind: Service
metadata:
  name: synthetic-service
spec:
  ports:
  - port: 8080
`
