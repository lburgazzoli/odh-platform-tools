package loader

import (
	"bytes"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// validDefinition returns a complete Helm definition whose main
// container name is omitted.
func validDefinition() v1alpha1.PlatformModuleDefinition {
	return v1alpha1.PlatformModuleDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic"},
		Spec: v1alpha1.PlatformModuleDefinitionSpec{
			Instance: v1alpha1.Instance{Type: v1alpha1.ModuleType{
				Group:   "components.platform.opendatahub.io",
				Version: "v1alpha1",
				Kind:    "SyntheticModule",
			}},
			Manifests: v1alpha1.Manifests{
				Format: v1alpha1.FormatHelm,
				Helm:   &v1alpha1.HelmManifests{Path: "./manifests/chart"},
			},
			RelatedImages: []string{"RELATED_IMAGE_SYNTHETIC_OPERAND"},
			Controller: v1alpha1.Controller{
				Deployment: v1alpha1.ControllerDeployment{
					Name: "synthetic-controller",
					Container: &v1alpha1.ContainerDefinition{
						Image: "{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}",
						Env: []v1alpha1.EnvVar{
							{Name: "LOG_LEVEL", Value: new("")},
							{Name: "ENABLE_FEATURE", Value: new("true")},
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

// TestDefinitionRESTJSONRoundTrip proves the loaded definition type
// works as a real Kubernetes API object: it registers in a scheme,
// round-trips through the standard JSON serializer with the expected
// group, version, and kind, and all three env value states — omitted,
// null, and empty — survive a serializer the local JSON-v2 decoding
// never exercises.
func TestDefinitionRESTJSONRoundTrip(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	scheme := runtime.NewScheme()
	g.Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())

	definition := validDefinition()
	definition.Spec.Controller.Deployment.Container.Env = []v1alpha1.EnvVar{
		{Name: "OMITTED"},
		{Name: "NULL", Value: nil},
		{Name: "EMPTY", Value: new("")},
	}

	serializer := json.NewSerializerWithOptions(
		json.DefaultMetaFactory, scheme, scheme,
		json.SerializerOptions{Yaml: false, Pretty: false},
	)

	encoded := &bytes.Buffer{}
	err := serializer.Encode(&definition, encoded)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(encoded.String()).To(ContainSubstring(`"apiVersion":"config.opendatahub.io/v1alpha1"`))

	decoded, _, err := serializer.Decode(encoded.Bytes(), nil, nil)
	g.Expect(err).NotTo(HaveOccurred())

	typed, ok := decoded.(*v1alpha1.PlatformModuleDefinition)
	g.Expect(ok).To(BeTrue())
	g.Expect(typed.GroupVersionKind()).To(Equal(v1alpha1.GroupVersion.WithKind(v1alpha1.Kind)))
	g.Expect(typed.Spec.Controller.Deployment.Container.Env).To(HaveLen(3))
	g.Expect(typed.Spec.Controller.Deployment.Container.Env[0].Value).To(BeNil())
	g.Expect(typed.Spec.Controller.Deployment.Container.Env[1].Value).To(BeNil())
	g.Expect(*typed.Spec.Controller.Deployment.Container.Env[2].Value).To(Equal(""))
}
