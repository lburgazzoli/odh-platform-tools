package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"

	. "github.com/onsi/gomega"
)

// validDefinition returns a definition that passes Normalize for the
// given backend, with the main container name omitted so defaulting is
// observable.
func validDefinition(format Format) PlatformModuleDefinition {
	definition := PlatformModuleDefinition{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   Metadata{Name: "synthetic"},
		Spec: Spec{
			Type: Type{
				Group:   "components.platform.opendatahub.io",
				Version: "v1alpha1",
				Kind:    "SyntheticModule",
			},
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
						Name:  new("copy-manifests"),
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

// JSON documents exercising the container name presence rules through
// the JSON decoder.
const (
	containerJSONOmittedName   = `{"image":"quay.io/example/controller:v1"}`
	containerJSONNullName      = `{"name":null,"image":"quay.io/example/controller:v1"}`
	containerJSONEmptyName     = `{"name":"","image":"quay.io/example/controller:v1"}`
	containerJSONValidName     = `{"name":"manager","image":"quay.io/example/controller:v1"}`
	containerJSONNonStringName = `{"name":42,"image":"quay.io/example/controller:v1"}`
)

// definitionJSON is a complete valid document in the wire format.
const definitionJSON = `{
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

// fieldPaths returns the paths of every FieldError in err's tree, in
// collection order.
func fieldPaths(err error) []string {
	var paths []string

	var walk func(error)
	walk = func(err error) {
		if field, ok := err.(*FieldError); ok {
			paths = append(paths, field.Path)
			return
		}

		if unwrapper, ok := err.(interface{ Unwrap() []error }); ok {
			for _, cause := range unwrapper.Unwrap() {
				walk(cause)
			}
			return
		}

		if unwrapper, ok := err.(interface{ Unwrap() error }); ok {
			walk(unwrapper.Unwrap())
		}
	}

	walk(err)

	return paths
}

func TestNormalizeDefaultsOmittedMainContainerName(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := validDefinition(FormatHelm)

	normalized, err := definition.Normalize()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(*normalized.Spec.Controller.Deployment.Container.Name).To(Equal(DefaultMainContainerName))

	// The input definition is not mutated: the original container still
	// has no name.
	g.Expect(definition.Spec.Controller.Deployment.Container.Name).To(BeNil())
}

func TestNormalizePreservesAuthoredValues(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := validDefinition(FormatKustomize)
	definition.Spec.Controller.Deployment.Container.Name = new("custom-manager")
	// A name that would not be a valid Helm release name is fine for
	// the Kustomize backend.
	definition.Metadata.Name = "Synthetic_Module"

	normalized, err := definition.Normalize()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(*normalized.Spec.Controller.Deployment.Container.Name).To(Equal("custom-manager"))
	g.Expect(*normalized.Spec.Controller.Deployment.InitContainer.Name).To(Equal("copy-manifests"))
	g.Expect(normalized.Metadata.Name).To(Equal("Synthetic_Module"))
	g.Expect(normalized.Spec.Manifests.Kustomize.Path).To(Equal("./manifests/controller/overlays/development"))

	// Replacement references stay opaque, and an explicit empty env
	// value survives.
	g.Expect(normalized.Spec.Controller.Deployment.Container.Image).To(Equal("{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}"))
	g.Expect(*normalized.Spec.Controller.Deployment.Container.Env[0].Value).To(Equal(""))
}

func TestNormalizeRejectsInvalidDefinitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*PlatformModuleDefinition)
		paths  []string
	}{
		{
			name:   "wrong api version",
			mutate: func(d *PlatformModuleDefinition) { d.APIVersion = "example.com/v1" },
			paths:  []string{"apiVersion"},
		},
		{
			name:   "wrong kind",
			mutate: func(d *PlatformModuleDefinition) { d.Kind = "Module" },
			paths:  []string{"kind"},
		},
		{
			name:   "missing metadata name",
			mutate: func(d *PlatformModuleDefinition) { d.Metadata.Name = "" },
			paths:  []string{"metadata.name"},
		},
		{
			name: "helm release name with uppercase",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Manifests = Manifests{Format: FormatHelm, Helm: &HelmManifests{Path: "./chart"}}
				d.Metadata.Name = "Synthetic"
			},
			paths: []string{"metadata.name"},
		},
		{
			name: "helm release name too long",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Manifests = Manifests{Format: FormatHelm, Helm: &HelmManifests{Path: "./chart"}}
				d.Metadata.Name = "s" + strings.Repeat("a", 53)
			},
			paths: []string{"metadata.name"},
		},
		{
			name: "missing type",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Type = Type{}
			},
			paths: []string{"spec.type.group", "spec.type.version", "spec.type.kind"},
		},
		{
			name:   "unknown manifest format",
			mutate: func(d *PlatformModuleDefinition) { d.Spec.Manifests.Format = "raw" },
			paths:  []string{"spec.manifests.format"},
		},
		{
			name: "helm format with kustomize block",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Manifests.Format = FormatHelm
				d.Spec.Manifests.Helm = &HelmManifests{Path: "./chart"}
				d.Spec.Manifests.Kustomize = &KustomizeManifests{Path: "./tree"}
			},
			paths: []string{"spec.manifests.kustomize"},
		},
		{
			name: "helm format without helm block",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Manifests = Manifests{Format: FormatHelm}
			},
			paths: []string{"spec.manifests.helm"},
		},
		{
			name: "empty helm path",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Manifests = Manifests{Format: FormatHelm, Helm: &HelmManifests{}}
			},
			paths: []string{"spec.manifests.helm.path"},
		},
		{
			name: "kustomize format with helm block",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Manifests.Helm = &HelmManifests{Path: "./chart"}
			},
			paths: []string{"spec.manifests.helm"},
		},
		{
			name:   "missing deployment name",
			mutate: func(d *PlatformModuleDefinition) { d.Spec.Controller.Deployment.Name = "" },
			paths:  []string{"spec.controller.deployment.name"},
		},
		{
			name: "missing main container",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Controller.Deployment.Container = nil
			},
			paths: []string{"spec.controller.deployment.container"},
		},
		{
			name:   "empty main container name",
			mutate: func(d *PlatformModuleDefinition) { d.Spec.Controller.Deployment.Container.Name = new("") },
			paths:  []string{"spec.controller.deployment.container.name"},
		},
		{
			name:   "missing main container image",
			mutate: func(d *PlatformModuleDefinition) { d.Spec.Controller.Deployment.Container.Image = "" },
			paths:  []string{"spec.controller.deployment.container.image"},
		},
		{
			name: "missing init container image",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Controller.Deployment.InitContainer.Image = ""
			},
			paths: []string{"spec.controller.deployment.initContainer.image"},
		},
		{
			name: "init container without name",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Controller.Deployment.InitContainer.Name = nil
			},
			paths: []string{"spec.controller.deployment.initContainer.name"},
		},
		{
			name: "env entry without name",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Controller.Deployment.Container.Env[0].Name = ""
			},
			paths: []string{"spec.controller.deployment.container.env[0].name"},
		},
		{
			name: "env entry without value",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Controller.Deployment.Container.Env[0].Value = nil
			},
			paths: []string{"spec.controller.deployment.container.env[0].value"},
		},
		{
			name: "duplicate env names",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.Controller.Deployment.Container.Env[1].Name = "LOG_LEVEL"
			},
			paths: []string{"spec.controller.deployment.container.env[1].name"},
		},
		{
			name:   "unportable related image name",
			mutate: func(d *PlatformModuleDefinition) { d.Spec.RelatedImages = []string{"related-image"} },
			paths:  []string{"spec.relatedImages[0]"},
		},
		{
			name: "duplicate related images",
			mutate: func(d *PlatformModuleDefinition) {
				d.Spec.RelatedImages = []string{"RELATED_IMAGE_A", "RELATED_IMAGE_A"}
			},
			paths: []string{"spec.relatedImages[1]"},
		},
		{
			name:   "related image colliding with explicit env",
			mutate: func(d *PlatformModuleDefinition) { d.Spec.RelatedImages = []string{"LOG_LEVEL"} },
			paths:  []string{"spec.relatedImages[0]"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			definition := validDefinition(FormatKustomize)
			test.mutate(&definition)

			_, err := definition.Normalize()
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, ErrInvalidDefinition)).To(BeTrue())
			g.Expect(fieldPaths(err)).To(ConsistOf(test.paths))
		})
	}
}

func TestNormalizeAggregatesIndependentProblems(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := validDefinition(FormatHelm)
	definition.APIVersion = "example.com/v1"
	definition.Spec.Controller.Deployment.Container.Image = ""
	definition.Spec.Controller.Deployment.Name = ""

	_, err := definition.Normalize()
	g.Expect(err).To(HaveOccurred())
	g.Expect(fieldPaths(err)).To(ConsistOf(
		"apiVersion",
		"spec.controller.deployment.name",
		"spec.controller.deployment.container.image",
	))
}

func TestContainerDefinitionJSONDecoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		json    string
		want    *string
		wantErr bool
	}{
		{name: "omitted name", json: containerJSONOmittedName},
		{name: "null name", json: containerJSONNullName, want: new("")},
		{name: "empty name", json: containerJSONEmptyName, want: new("")},
		{name: "valid name", json: containerJSONValidName, want: new("manager")},
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
			if test.want == nil {
				g.Expect(container.Name).To(BeNil())
			} else {
				g.Expect(container.Name).To(Equal(test.want))
			}
			g.Expect(container.Image).To(Equal("quay.io/example/controller:v1"))
		})
	}
}

func TestDefinitionJSONDecoding(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var definition PlatformModuleDefinition
	err := jsonv2.Unmarshal([]byte(definitionJSON), &definition)
	g.Expect(err).NotTo(HaveOccurred())

	normalized, err := definition.Normalize()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(normalized.APIVersion).To(Equal(APIVersion))
	g.Expect(normalized.Kind).To(Equal(Kind))
	g.Expect(normalized.Metadata.Name).To(Equal("synthetic"))
	g.Expect(normalized.Spec.Manifests.Format).To(Equal(FormatKustomize))
	g.Expect(normalized.Spec.Manifests.Kustomize.Path).To(Equal("./manifests/controller"))
	g.Expect(*normalized.Spec.Controller.Deployment.Container.Name).To(Equal(DefaultMainContainerName))
	g.Expect(normalized.Spec.Controller.Deployment.Container.Env).To(HaveLen(1))
	g.Expect(*normalized.Spec.Controller.Deployment.Container.Env[0].Value).To(Equal("{{CONTROLLER_LOG_LEVEL}}"))
	g.Expect(normalized.Spec.RelatedImages).To(Equal([]string{"RELATED_IMAGE_SYNTHETIC_OPERAND"}))
}

func TestDefinitionJSONRoundTrip(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	normalized, err := validDefinition(FormatHelm).Normalize()
	g.Expect(err).NotTo(HaveOccurred())

	encoded, err := jsonv2.Marshal(normalized)
	g.Expect(err).NotTo(HaveOccurred())

	var decoded PlatformModuleDefinition
	err = jsonv2.Unmarshal(encoded, &decoded)
	g.Expect(err).NotTo(HaveOccurred())

	decodedNormalized, err := decoded.Normalize()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(decodedNormalized).To(Equal(normalized))
}

func TestNewDefinitionErrorPreservesCause(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := NewDefinitionError(context.Canceled)

	g.Expect(errors.Is(err, ErrInvalidDefinition)).To(BeTrue())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
}
