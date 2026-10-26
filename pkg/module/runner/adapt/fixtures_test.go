package adapt

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// newDefinition returns a pointer to the synthetic module definition
// carrying the full controller deployment descriptor: a token image,
// one extra env entry, an init container target, and one related
// image. The pointer form is what the runner hands to the injectors;
// tests that tailor it mutate through the pointer directly.
func newDefinition() *toolApi.PlatformModuleDefinition {
	return &toolApi.PlatformModuleDefinition{
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

// newResolvedImages returns the fully resolved image snapshot.
func newResolvedImages() map[string]string {
	return map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
		"RELATED_IMAGE_OPERAND":    "quay.io/example/operand:v1",
	}
}

// resource decodes one YAML document into an unstructured object
// through the framework's shared document decoder — the same resource
// decoding the loader uses. The document must hold exactly one object.
func resource(t *testing.T, document string) unstructured.Unstructured {
	t.Helper()

	documents, err := fwresources.Decode(
		serializer.NewCodecFactory(clientgoscheme.Scheme).UniversalDeserializer(),
		[]byte(document))
	if err != nil {
		t.Fatal(err)
	}

	if len(documents) != 1 {
		t.Fatalf("document must hold exactly one object, got %d", len(documents))
	}

	return documents[0]
}

// findResource returns the resource of the given kind and name. The
// pointer form feeds the gomega-matchers extractors that accept only
// *unstructured.Unstructured, such as k8s.Data.
func findResource(
	t *testing.T,
	resources []unstructured.Unstructured,
	kind string,
	name string,
) *unstructured.Unstructured {
	t.Helper()

	for i := range resources {
		if resources[i].GetKind() == kind && resources[i].GetName() == name {
			return &resources[i]
		}
	}

	t.Fatalf("no %s %s in resources: %v", kind, name, resources)

	return nil
}

// envVarNames returns the env var names of the container in order.
func envVarNames(container corev1.Container) []string {
	names := make([]string, 0, len(container.Env))
	for _, env := range container.Env {
		names = append(names, env.Name)
	}

	return names
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
