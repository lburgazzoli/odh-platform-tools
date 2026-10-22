package config

import (
	"context"
	"errors"
	"testing"

	jsonv2 "encoding/json/v2"

	"k8s.io/apimachinery/pkg/runtime/schema"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"
)

// validDefinition returns a complete definition for the given backend,
// with the main container name omitted so defaulting is observable.
func validDefinition(format Format) PlatformModuleDefinition {
	definition := PlatformModuleDefinition{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   Metadata{Name: "synthetic"},
		Spec: Spec{
			Instance: Instance{Type: Type{GroupVersionKind: schema.GroupVersionKind{
				Group:   "components.platform.opendatahub.io",
				Version: "v1alpha1",
				Kind:    "SyntheticModule",
			}}},
			RelatedImages: []string{"RELATED_IMAGE_SYNTHETIC_OPERAND"},
			Controller: Controller{
				Deployment: Deployment{
					Name: "synthetic-controller",
					Container: &ContainerDefinition{
						Image: "{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}",
						Env: []EnvVar{
							{Name: "LOG_LEVEL", Value: new("")},
							{Name: "ENABLE_FEATURE", Value: new("true")},
						},
					},
					InitContainer: &ContainerDefinition{
						Name:  "copy-manifests",
						Image: "{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}",
						Env: []EnvVar{
							{Name: "INIT_MODE", Value: new("copy")},
						},
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

// containerJSONNonStringName is a container wire document whose name
// is not a string.
const containerJSONNonStringName = `{"name":42,"image":"quay.io/example/controller:v1"}`

// definitionJSON is a complete valid document in the wire format.
const definitionJSON = `{
	"apiVersion": "config.opendatahub.io/v1",
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

func TestNormalizeDefaultsOmittedMainContainerName(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := validDefinition(FormatHelm)

	normalized := definition.Normalize()
	g.Expect(normalized.Spec.Controller.Deployment.Container.Name).To(Equal(DefaultMainContainerName))

	// The input definition is not mutated: the original container still
	// has no name.
	g.Expect(definition.Spec.Controller.Deployment.Container.Name).To(Equal(""))
}

func TestNormalizeDefaultsNamesIndependently(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		instanceName  string
		containerName string
		wantInstance  string
		wantContainer string
	}{
		{
			name:          "both names omitted",
			wantInstance:  "synthetic",
			wantContainer: DefaultMainContainerName,
		},
		{
			name:          "instance name omitted",
			containerName: "custom-manager",
			wantInstance:  "synthetic",
			wantContainer: "custom-manager",
		},
		{
			name:          "container name omitted",
			instanceName:  "trainer",
			wantInstance:  "trainer",
			wantContainer: DefaultMainContainerName,
		},
		{
			name:          "both names authored",
			instanceName:  "trainer",
			containerName: "custom-manager",
			wantInstance:  "trainer",
			wantContainer: "custom-manager",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			definition := validDefinition(FormatHelm)
			definition.Spec.Instance.Name = test.instanceName
			definition.Spec.Controller.Deployment.Container.Name = test.containerName

			normalized := definition.Normalize()
			g.Expect(normalized.Spec.Instance.Name).To(Equal(test.wantInstance))
			g.Expect(normalized.Spec.Controller.Deployment.Container.Name).To(Equal(test.wantContainer))

			// The container default does not alias storage back into
			// the caller.
			g.Expect(definition.Spec.Controller.Deployment.Container.Name).To(Equal(test.containerName))

			// Normalization is idempotent.
			repeated := normalized.Normalize()
			g.Expect(repeated).To(Equal(normalized))
		})
	}
}

func TestNormalizePreservesAuthoredValues(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := validDefinition(FormatKustomize)
	definition.Spec.Controller.Deployment.Container.Name = "custom-manager"
	// Structural validation is deferred, so even names that are not
	// valid object names survive normalization untouched.
	definition.Metadata.Name = "Synthetic_Module"
	definition.Spec.Instance.Name = "synthetic-module"

	normalized := definition.Normalize()
	g.Expect(normalized.Spec.Controller.Deployment.Container.Name).To(Equal("custom-manager"))
	g.Expect(normalized.Spec.Controller.Deployment.InitContainer.Name).To(Equal("copy-manifests"))
	g.Expect(normalized.Metadata.Name).To(Equal("Synthetic_Module"))
	g.Expect(normalized.Spec.Instance.Name).To(Equal("synthetic-module"))
	g.Expect(normalized.Spec.Manifests.Kustomize.Path).To(Equal("./manifests/controller/overlays/development"))

	// Replacement references stay opaque, and an explicit empty env
	// value survives.
	g.Expect(normalized.Spec.Controller.Deployment.Container.Image).To(Equal("{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}"))
	g.Expect(*normalized.Spec.Controller.Deployment.Container.Env[0].Value).To(Equal(""))
}

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

// definitionJSONLegacyType is a definition placing the module type at
// the superseded top-level spec.type location only.
const definitionJSONLegacyType = `{
	"apiVersion": "config.opendatahub.io/v1",
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
	"apiVersion": "config.opendatahub.io/v1",
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
	err := jsonv2.Unmarshal([]byte(definitionJSON), &definition)
	g.Expect(err).NotTo(HaveOccurred())

	normalized := definition.Normalize()
	g.Expect(normalized.APIVersion).To(Equal(APIVersion))
	g.Expect(normalized.Kind).To(Equal(Kind))
	g.Expect(normalized.Metadata.Name).To(Equal("synthetic"))
	g.Expect(normalized.Spec.Instance.Type.Kind).To(Equal("SyntheticModule"))
	g.Expect(normalized.Spec.Instance.Name).To(Equal("synthetic"))
	g.Expect(normalized.Spec.Manifests.Format).To(Equal(FormatKustomize))
	g.Expect(normalized.Spec.Manifests.Kustomize.Path).To(Equal("./manifests/controller"))
	g.Expect(normalized.Spec.Controller.Deployment.Container.Name).To(Equal(DefaultMainContainerName))
	g.Expect(normalized.Spec.Controller.Deployment.Container.Env).To(HaveLen(1))
	g.Expect(*normalized.Spec.Controller.Deployment.Container.Env[0].Value).To(Equal("{{CONTROLLER_LOG_LEVEL}}"))
	g.Expect(normalized.Spec.RelatedImages).To(Equal([]string{"RELATED_IMAGE_SYNTHETIC_OPERAND"}))
}

func TestDefinitionJSONRoundTrip(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := validDefinition(FormatHelm)
	normalized := definition.Normalize()

	encoded, err := jsonv2.Marshal(normalized)
	g.Expect(err).NotTo(HaveOccurred())

	// The wire format carries the instance identity exactly once, with
	// lower-case group, version, and kind members, and no legacy
	// top-level spec.type location.
	g.Expect(string(encoded)).To(SatisfyAll(
		jq.Match(`.spec.instance.name == "synthetic"`),
		jq.Match(`.spec.instance.type.group == "components.platform.opendatahub.io"`),
		jq.Match(`.spec.instance.type.version == "v1alpha1"`),
		jq.Match(`.spec.instance.type.kind == "SyntheticModule"`),
		jq.Match(`.spec | has("type") | not`),
	))

	var decoded PlatformModuleDefinition
	err = jsonv2.Unmarshal(encoded, &decoded)
	g.Expect(err).NotTo(HaveOccurred())

	decodedNormalized := decoded.Normalize()
	g.Expect(decodedNormalized).To(Equal(normalized))
}

func TestNewDefinitionErrorPreservesCause(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := NewDefinitionError(context.Canceled)

	g.Expect(errors.Is(err, ErrInvalidDefinition)).To(BeTrue())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
}
