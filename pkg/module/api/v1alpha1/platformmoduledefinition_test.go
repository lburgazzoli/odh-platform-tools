package v1alpha1

import (
	"bytes"
	"testing"

	jsonv2 "encoding/json/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"
)

// validDefinition returns a complete definition for the given backend,
// with the main container name omitted so loader defaulting is
// observable.
func validDefinition(format Format) PlatformModuleDefinition {
	definition := PlatformModuleDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: APIVersion, Kind: Kind},
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic"},
		Spec: PlatformModuleDefinitionSpec{
			Instance: Instance{Type: ModuleType{
				Group:   "components.platform.opendatahub.io",
				Version: "v1alpha1",
				Kind:    "SyntheticModule",
			}},
			RelatedImages: []string{"RELATED_IMAGE_SYNTHETIC_OPERAND"},
			Controller: Controller{
				Deployment: ControllerDeployment{
					Name: "synthetic-controller",
					Container: &ContainerDefinition{
						Image: "{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}",
						Env: []EnvVar{
							{Name: "LOG_LEVEL", Value: new("")},
							{Name: "ENABLE_FEATURE", Value: new("true")},
						},
					},
					InitContainer: &ContainerTarget{
						Name: "copy-manifests",
					},
				},
			},
		},
	}

	switch format {
	case FormatHelm:
		definition.Spec.Manifests = Manifests{
			Format: FormatHelm,
			Helm:   &HelmManifests{Path: "./manifests/chart"},
		}
	case FormatKustomize:
		definition.Spec.Manifests = Manifests{
			Format:    FormatKustomize,
			Kustomize: &KustomizeManifests{Path: "./manifests/controller/overlays/development"},
		}
	}

	return definition
}

func TestAPIVersionMatchesGroupVersion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	g.Expect(APIVersion).To(Equal(GroupVersion.String()))
	g.Expect(CRDName).To(Equal("platformmoduledefinitions." + GroupVersion.Group))
}

func TestModuleTypeGroupVersionKind(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	moduleType := ModuleType{
		Group:   "components.platform.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "SyntheticModule",
	}

	gvk := moduleType.GroupVersionKind()
	g.Expect(gvk.Group).To(Equal("components.platform.opendatahub.io"))
	g.Expect(gvk.Version).To(Equal("v1alpha1"))
	g.Expect(gvk.Kind).To(Equal("SyntheticModule"))
	g.Expect(moduleType.String()).To(Equal(gvk.String()))
}

func TestDeepCopyIsolatesMetadataSpecAndStatus(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := validDefinition(FormatHelm)
	definition.Status.Conditions = []api.Condition{{Type: string(api.ConditionTypeReady), Status: metav1.ConditionTrue}}
	copied := definition.DeepCopy()

	// Mutating every block of the copy leaves the original untouched.
	copied.Name = "other"
	copied.Spec.Controller.Deployment.Container.Image = "mutated"
	copied.Spec.Controller.Deployment.Container.Env[0].Value = new("mutated")
	copied.Status.Conditions[0].Status = metav1.ConditionFalse

	g.Expect(definition.Name).To(Equal("synthetic"))
	g.Expect(definition.Spec.Controller.Deployment.Container.Image).To(Equal("{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}"))
	g.Expect(*definition.Spec.Controller.Deployment.Container.Env[0].Value).To(Equal(""))
	g.Expect(definition.Status.Conditions[0].Status).To(Equal(metav1.ConditionTrue))
}

func TestDefinitionRESTJSONRoundTrip(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	scheme := runtime.NewScheme()
	g.Expect(AddToScheme(scheme)).To(Succeed())

	// All three env value states must survive the standard Kubernetes
	// JSON serializer, not only JSON-v2: the persisted owner is read
	// and written through it.
	definition := validDefinition(FormatHelm)
	definition.Spec.Controller.Deployment.Container.Env = []EnvVar{
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

	typed, ok := decoded.(*PlatformModuleDefinition)
	g.Expect(ok).To(BeTrue())
	g.Expect(typed.GroupVersionKind()).To(Equal(GroupVersion.WithKind(Kind)))
	g.Expect(typed.Spec.Controller.Deployment.Container.Env).To(HaveLen(3))
	g.Expect(typed.Spec.Controller.Deployment.Container.Env[0].Value).To(BeNil())
	g.Expect(typed.Spec.Controller.Deployment.Container.Env[1].Value).To(BeNil())
	g.Expect(*typed.Spec.Controller.Deployment.Container.Env[2].Value).To(Equal(""))
}

// containerJSONOmittedName is a container wire document whose name is
// omitted.
const containerJSONOmittedName = `{"image":"quay.io/example/controller:v1"}`

// containerJSONNullName is a container wire document whose name is an
// explicit null.
const containerJSONNullName = `{"name":null,"image":"quay.io/example/controller:v1"}`

// containerJSONEmptyName is a container wire document whose name is an
// explicit empty string.
const containerJSONEmptyName = `{"name":"","image":"quay.io/example/controller:v1"}`

// containerJSONValidName is a container wire document with a valid
// name.
const containerJSONValidName = `{"name":"manager","image":"quay.io/example/controller:v1"}`

// containerJSONNonStringName is a container wire document whose name is
// not a string.
const containerJSONNonStringName = `{"name":42,"image":"quay.io/example/controller:v1"}`

func TestContainerDefinitionJSONDecoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		json    string
		want    string
		wantErr bool
	}{
		{name: "omitted name", json: containerJSONOmittedName},
		{name: "null name", json: containerJSONNullName},
		{name: "empty name", json: containerJSONEmptyName},
		{name: "valid name", json: containerJSONValidName, want: "manager"},
		{name: "non-string name", json: containerJSONNonStringName, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			var container ContainerDefinition
			err := jsonv2.Unmarshal([]byte(test.json), &container)

			if test.wantErr {
				g.Expect(err).To(HaveOccurred())
				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(container.Name).To(Equal(test.want))
			g.Expect(container.Image).To(Equal("quay.io/example/controller:v1"))
		})
	}
}

// containerTargetJSONOmittedName is an init-container wire document
// whose name is omitted.
const containerTargetJSONOmittedName = `{}`

// containerTargetJSONNullName is an init-container wire document whose
// name is an explicit null.
const containerTargetJSONNullName = `{"name":null}`

// containerTargetJSONEmptyName is an init-container wire document whose
// name is an explicit empty string.
const containerTargetJSONEmptyName = `{"name":""}`

// containerTargetJSONValidName is an init-container wire document with
// a valid name.
const containerTargetJSONValidName = `{"name":"copy-manifests"}`

// containerTargetJSONNonStringName is an init-container wire document
// whose name is not a string.
const containerTargetJSONNonStringName = `{"name":42}`

// containerTargetJSONImageMember is an init-container wire document
// carrying the removed image member.
const containerTargetJSONImageMember = `{"name":"copy-manifests","image":"quay.io/example/controller:v1"}`

// containerTargetJSONEnvMember is an init-container wire document
// carrying the removed env member.
const containerTargetJSONEnvMember = `{"name":"copy-manifests","env":[{"name":"INIT_MODE","value":"copy"}]}`

func TestContainerTargetJSONDecoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		json    string
		want    string
		wantErr bool
	}{
		{name: "omitted name", json: containerTargetJSONOmittedName},
		{name: "null name", json: containerTargetJSONNullName},
		{name: "empty name", json: containerTargetJSONEmptyName},
		{name: "valid name", json: containerTargetJSONValidName, want: "copy-manifests"},
		{name: "non-string name", json: containerTargetJSONNonStringName, wantErr: true},
		{name: "removed image member", json: containerTargetJSONImageMember, wantErr: true},
		{name: "removed env member", json: containerTargetJSONEnvMember, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			var target ContainerTarget
			err := jsonv2.Unmarshal([]byte(test.json), &target, jsonv2.RejectUnknownMembers(true))

			if test.wantErr {
				g.Expect(err).To(HaveOccurred())
				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(target.Name).To(Equal(test.want))
		})
	}
}

// instanceJSONOmittedName is an instance wire document whose name is
// omitted.
const instanceJSONOmittedName = `{
	"type": {
		"group": "components.platform.opendatahub.io",
		"version": "v1alpha1",
		"kind": "SyntheticModule"
	}
}`

// instanceJSONNullName is an instance wire document whose name is an
// explicit null.
const instanceJSONNullName = `{
	"name": null,
	"type": {
		"group": "components.platform.opendatahub.io",
		"version": "v1alpha1",
		"kind": "SyntheticModule"
	}
}`

// instanceJSONEmptyName is an instance wire document whose name is an
// explicit empty string.
const instanceJSONEmptyName = `{
	"name": "",
	"type": {
		"group": "components.platform.opendatahub.io",
		"version": "v1alpha1",
		"kind": "SyntheticModule"
	}
}`

// instanceJSONValidName is an instance wire document with a valid
// name.
const instanceJSONValidName = `{
	"name": "trainer",
	"type": {
		"group": "components.platform.opendatahub.io",
		"version": "v1alpha1",
		"kind": "SyntheticModule"
	}
}`

// instanceJSONNonStringName is an instance wire document whose name is
// not a string.
const instanceJSONNonStringName = `{
	"name": 42,
	"type": {
		"group": "components.platform.opendatahub.io",
		"version": "v1alpha1",
		"kind": "SyntheticModule"
	}
}`

// instanceJSONUnknownMember is an instance wire document with an
// unknown member.
const instanceJSONUnknownMember = `{
	"namespace": "opendatahub",
	"type": {
		"group": "components.platform.opendatahub.io",
		"version": "v1alpha1",
		"kind": "SyntheticModule"
	}
}`

// instanceJSONCaseMismatch is an instance wire document with a
// case-mismatched member.
const instanceJSONCaseMismatch = `{
	"Name": "trainer",
	"type": {
		"group": "components.platform.opendatahub.io",
		"version": "v1alpha1",
		"kind": "SyntheticModule"
	}
}`

// instanceJSONDuplicateName is an instance wire document with a
// duplicate name member.
const instanceJSONDuplicateName = `{
	"name": "trainer",
	"name": "other",
	"type": {
		"group": "components.platform.opendatahub.io",
		"version": "v1alpha1",
		"kind": "SyntheticModule"
	}
}`

func TestInstanceJSONDecoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		json    string
		want    string
		wantErr bool
	}{
		{name: "omitted name", json: instanceJSONOmittedName},
		{name: "null name", json: instanceJSONNullName},
		{name: "empty name", json: instanceJSONEmptyName},
		{name: "valid name", json: instanceJSONValidName, want: "trainer"},
		{name: "non-string name", json: instanceJSONNonStringName, wantErr: true},
		{name: "unknown member", json: instanceJSONUnknownMember, wantErr: true},
		{name: "case-mismatched member", json: instanceJSONCaseMismatch, wantErr: true},
		{name: "duplicate member", json: instanceJSONDuplicateName, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			var instance Instance
			err := jsonv2.Unmarshal([]byte(test.json), &instance, jsonv2.RejectUnknownMembers(true))

			if test.wantErr {
				g.Expect(err).To(HaveOccurred())
				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(instance.Name).To(Equal(test.want))
			g.Expect(instance.Type.Kind).To(Equal("SyntheticModule"))
		})
	}
}

// definitionJSON is a complete valid document in the wire format.
const definitionJSON = `{
	"apiVersion": "config.opendatahub.io/v1alpha1",
	"kind": "PlatformModuleDefinition",
	"metadata": {"name": "synthetic"},
	"spec": {
		"instance": {
			"type": {
				"group": "components.platform.opendatahub.io",
				"version": "v1alpha1",
				"kind": "SyntheticModule"
			}
		},
		"manifests": {
			"format": "kustomize",
			"kustomize": {"path": "./manifests/controller"}
		},
		"relatedImages": ["RELATED_IMAGE_SYNTHETIC_OPERAND"],
		"controller": {
			"deployment": {
				"name": "synthetic-controller",
				"container": {
					"image": "{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}",
					"env": [{"name": "LOG_LEVEL", "value": "{{CONTROLLER_LOG_LEVEL}}"}]
				}
			}
		}
	}
}`

// definitionJSONLegacyType is a definition placing the module type at
// the superseded top-level spec.type location only.
const definitionJSONLegacyType = `{
	"apiVersion": "config.opendatahub.io/v1alpha1",
	"kind": "PlatformModuleDefinition",
	"metadata": {"name": "synthetic"},
	"spec": {
		"type": {
			"group": "components.platform.opendatahub.io",
			"version": "v1alpha1",
			"kind": "SyntheticModule"
		},
		"manifests": {
			"format": "kustomize",
			"kustomize": {"path": "./manifests/controller"}
		},
		"controller": {
			"deployment": {
				"name": "synthetic-controller",
				"container": {"image": "quay.io/example/controller:v1"}
			}
		}
	}
}`

// definitionJSONLegacyAndNestedType is a definition carrying the
// module type at both the legacy and the nested location.
const definitionJSONLegacyAndNestedType = `{
	"apiVersion": "config.opendatahub.io/v1alpha1",
	"kind": "PlatformModuleDefinition",
	"metadata": {"name": "synthetic"},
	"spec": {
		"type": {
			"group": "components.platform.opendatahub.io",
			"version": "v1alpha1",
			"kind": "SyntheticModule"
		},
		"instance": {
			"type": {
				"group": "components.platform.opendatahub.io",
				"version": "v1alpha1",
				"kind": "SyntheticModule"
			}
		},
		"manifests": {
			"format": "kustomize",
			"kustomize": {"path": "./manifests/controller"}
		},
		"controller": {
			"deployment": {
				"name": "synthetic-controller",
				"container": {"image": "quay.io/example/controller:v1"}
			}
		}
	}
}`

func TestDefinitionJSONRejectsLegacySpecType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
	}{
		{name: "legacy location only", json: definitionJSONLegacyType},
		{name: "legacy and nested locations", json: definitionJSONLegacyAndNestedType},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			var definition PlatformModuleDefinition
			err := jsonv2.Unmarshal([]byte(test.json), &definition, jsonv2.RejectUnknownMembers(true))

			g.Expect(err).To(HaveOccurred())
			g.Expect(err.Error()).To(ContainSubstring(`unknown object member name "type"`))
		})
	}
}

func TestDefinitionJSONDecoding(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var definition PlatformModuleDefinition
	err := jsonv2.Unmarshal([]byte(definitionJSON), &definition, jsonv2.RejectUnknownMembers(true))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(definition.APIVersion).To(Equal(APIVersion))
	g.Expect(definition.Kind).To(Equal(Kind))
	g.Expect(definition.Name).To(Equal("synthetic"))
	g.Expect(definition.Spec.Instance.Type.Kind).To(Equal("SyntheticModule"))
	g.Expect(definition.Spec.Manifests.Format).To(Equal(FormatKustomize))
	g.Expect(definition.Spec.Manifests.Kustomize.Path).To(Equal("./manifests/controller"))
	g.Expect(definition.Spec.Controller.Deployment.Container.Name).To(Equal(""))
	g.Expect(definition.Spec.Controller.Deployment.Container.Env).To(HaveLen(1))
	g.Expect(*definition.Spec.Controller.Deployment.Container.Env[0].Value).To(Equal("{{CONTROLLER_LOG_LEVEL}}"))
	g.Expect(definition.Spec.RelatedImages).To(Equal([]string{"RELATED_IMAGE_SYNTHETIC_OPERAND"}))
}

func TestDefinitionJSONRoundTrip(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := validDefinition(FormatHelm)

	encoded, err := jsonv2.Marshal(definition)
	g.Expect(err).NotTo(HaveOccurred())

	// The wire format carries the instance identity exactly once, with
	// lower-case group, version, and kind members, and no legacy
	// top-level spec.type location.
	g.Expect(string(encoded)).To(SatisfyAll(
		jq.Match(`.spec.instance.name == null`),
		jq.Match(`.spec.instance.type.group == "components.platform.opendatahub.io"`),
		jq.Match(`.spec.instance.type.version == "v1alpha1"`),
		jq.Match(`.spec.instance.type.kind == "SyntheticModule"`),
		jq.Match(`.spec | has("type") | not`),
	))

	var decoded PlatformModuleDefinition
	err = jsonv2.Unmarshal(encoded, &decoded, jsonv2.RejectUnknownMembers(true))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(decoded).To(Equal(definition))
}

// envValueOmittedJSON is a main-container wire document whose env
// value is omitted.
const envValueOmittedJSON = `{"name":"LOG_LEVEL"}`

// envValueNullJSON is a main-container wire document whose env value
// is an explicit null.
const envValueNullJSON = `{"name":"LOG_LEVEL","value":null}`

// envValueEmptyJSON is a main-container wire document whose env value
// is an explicit empty string.
const envValueEmptyJSON = `{"name":"LOG_LEVEL","value":""}`

func TestEnvVarJSONValueStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		json    string
		wantNil bool
		want    string
	}{
		{name: "omitted value", json: envValueOmittedJSON, wantNil: true},
		{name: "null value", json: envValueNullJSON, wantNil: true},
		{name: "empty value", json: envValueEmptyJSON, want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			var envVar EnvVar
			err := jsonv2.Unmarshal([]byte(test.json), &envVar)
			g.Expect(err).NotTo(HaveOccurred())

			if test.wantNil {
				g.Expect(envVar.Value).To(BeNil())
				return
			}

			g.Expect(envVar.Value).NotTo(BeNil())
			g.Expect(*envVar.Value).To(Equal(test.want))
		})
	}
}
