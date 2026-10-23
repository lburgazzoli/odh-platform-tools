package renderer

import (
	"errors"
	"testing"

	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// guardModuleInstanceV1YAML is an instance of the configured module
// type at a version other than the configured one: the guard matches
// the GroupKind at any version.
const guardModuleInstanceV1YAML = `apiVersion: components.platform.opendatahub.io/v1
kind: SyntheticModule
metadata:
  name: instance
  namespace: modules
`

// guardModuleInstanceOtherKindYAML is an instance of the configured
// group at a different kind: the guard must let it through.
const guardModuleInstanceOtherKindYAML = `apiVersion: components.platform.opendatahub.io/v1
kind: OtherModule
metadata:
  name: instance
  namespace: modules
`

// guardDefinitionObjectYAML is a config.opendatahub.io
// PlatformModuleDefinition object at a version other than the API's
// current one.
const guardDefinitionObjectYAML = `apiVersion: config.opendatahub.io/v1beta1
kind: PlatformModuleDefinition
metadata:
  name: other
`

// guardReservedCRDYAML is the persisted definition CRD under its
// reserved name.
const guardReservedCRDYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: platformmoduledefinitions.config.opendatahub.io
`

// guardOrdinaryCRDYAML is an unrelated CRD: only the reserved name is
// rejected.
const guardOrdinaryCRDYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: others.config.opendatahub.io
`

// guardOrdinaryConfigMapYAML is an ordinary controller-package
// resource.
const guardOrdinaryConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: module-config
  namespace: modules
`

// guardDefinition returns a definition owning the synthetic module
// type.
func guardDefinition() v1alpha1.PlatformModuleDefinition {
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
		},
	}
}

// guardResource converts one YAML document to an unstructured object.
func guardResource(t *testing.T, document string) unstructured.Unstructured {
	t.Helper()

	g := NewWithT(t)

	object := unstructured.Unstructured{}
	g.Expect(yaml.Unmarshal([]byte(document), &object)).To(Succeed())

	return object
}

// TestCheckReservedRejectsReservedIdentities proves the shared payload
// guard rejects the objects a module run must never deploy — the
// configured module type's own instances at any version, any
// config.opendatahub.io PlatformModuleDefinition, and the reserved
// definition CRD — with the sentinel reachable through errors.Is.
func TestCheckReservedRejectsReservedIdentities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		document string
	}{
		{"module instance at another version", guardModuleInstanceV1YAML},
		{"platform module definition at another version", guardDefinitionObjectYAML},
		{"reserved definition CRD", guardReservedCRDYAML},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			err := CheckReserved(guardDefinition(), []unstructured.Unstructured{
				guardResource(t, guardOrdinaryConfigMapYAML),
				guardResource(t, testCase.document),
			})

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, ErrReservedResource)).To(BeTrue())
		})
	}
}

// TestCheckReservedAllowsOrdinaryResources proves the guard passes
// ordinary controller-package contents through untouched: only the
// reserved identities are examined, and the rendered objects
// themselves are never modified.
func TestCheckReservedAllowsOrdinaryResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		guardResource(t, guardOrdinaryConfigMapYAML),
		guardResource(t, guardModuleInstanceOtherKindYAML),
		guardResource(t, guardOrdinaryCRDYAML),
	}
	before := make([]unstructured.Unstructured, len(resources))
	for i := range resources {
		before[i] = *resources[i].DeepCopy()
	}

	g.Expect(CheckReserved(guardDefinition(), resources)).To(Succeed())

	for i := range resources {
		g.Expect(resources[i]).To(Equal(before[i]))
	}
}
