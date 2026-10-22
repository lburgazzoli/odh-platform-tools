// Package config defines the module definition document that describes
// an OpenDataHub module: its custom resource instance identity, its
// manifest source, and its controller deployment.
//
// The document is plain data with JSON tags. Loading it from a file is
// the loader package's job; this package models the document shape,
// applies placement defaults, and validates structure. It performs no
// filesystem, environment, renderer, or cluster access.
package config

import "k8s.io/apimachinery/pkg/runtime/schema"

// Constants describing the only supported definition envelope.
const (
	// APIVersion is the only supported definition API version.
	APIVersion = "config.opendatahub.io/v1"

	// Kind is the only supported definition kind.
	Kind = "PlatformModuleDefinition"

	// DefaultMainContainerName is the controller main container name
	// applied when the definition omits it.
	DefaultMainContainerName = "manager"
)

// Format identifies the manifest backend a definition selects.
type Format string

// The supported manifest backends.
const (
	// FormatHelm renders the module from a local Helm chart.
	FormatHelm Format = "helm"

	// FormatKustomize renders the module from a local Kustomize tree.
	FormatKustomize Format = "kustomize"
)

// PlatformModuleDefinition describes a module. It is a file-only
// document, not a custom resource: there is no Scheme, no generated
// deepcopy, and no cluster API submission.
type PlatformModuleDefinition struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}

// Metadata carries the definition identity. Only name is supported.
type Metadata struct {
	Name string `json:"name"`
}

// Spec is the module description: the custom resource instance the
// controller owns, the manifest backend that renders it, and the
// controller deployment.
type Spec struct {
	Instance      Instance   `json:"instance"`
	Manifests     Manifests  `json:"manifests"`
	RelatedImages []string   `json:"relatedImages,omitzero"`
	Controller    Controller `json:"controller"`
}

// Instance identifies the module's custom resource: the resource type
// the controller owns and the instance name a future runner will use
// to create and look up the resource. Neither is a rendering input:
// the renderer names the in-memory module instance and the Helm
// release after metadata.name.
type Instance struct {
	// Name is nil when the definition omits it; Normalize then
	// defaults it to metadata.name. An explicit null or empty name is
	// invalid, exactly like an empty container name. After Normalize
	// the name is always set.
	Name *string `json:"name,omitzero"`

	// Type is the Module custom resource type the controller owns.
	Type Type `json:"type"`
}

// Type identifies the Module custom resource the module controller
// owns. It embeds schema.GroupVersionKind, so its fields and the
// GroupKind, String, and Empty methods are available directly and the
// embedded value needs no conversion. Only the wire format is defined
// elsewhere, because apimachinery stopped tagging those fields and
// the loader's JSON decoding is case-sensitive.
type Type struct {
	schema.GroupVersionKind
}

// Manifests selects a rendering backend and its single local input
// directory. The block matching format is required; the other block
// must be absent.
type Manifests struct {
	Format    Format              `json:"format"`
	Helm      *HelmManifests      `json:"helm,omitzero"`
	Kustomize *KustomizeManifests `json:"kustomize,omitzero"`
}

// HelmManifests locates the Helm chart root, relative to the module
// root. The chart release name is the definition metadata.name.
type HelmManifests struct {
	Path string `json:"path"`
}

// KustomizeManifests locates the directory containing
// kustomization.yaml, relative to the module root.
type KustomizeManifests struct {
	Path string `json:"path"`
}

// Controller describes the module controller deployment.
type Controller struct {
	Deployment Deployment `json:"deployment"`
}

// Deployment names the controller Deployment and its selected
// containers. Container and InitContainer share ContainerDefinition;
// only the name defaulting differs by placement.
type Deployment struct {
	Name          string               `json:"name"`
	Container     *ContainerDefinition `json:"container,omitzero"`
	InitContainer *ContainerDefinition `json:"initContainer,omitzero"`
}

// ContainerDefinition describes one controller container: the main
// manager container or an init container. There is no inheritance
// between the two placements.
type ContainerDefinition struct {
	// Name is nil when the definition omits it; the main container
	// then defaults to DefaultMainContainerName during Normalize,
	// while an init container requires an explicit name. An explicit
	// null or empty name is invalid for both placements. After
	// Normalize the name is always set.
	Name *string `json:"name,omitzero"`

	// Image is the required container image. It may be a literal or a
	// whole-value {{VARIABLE}} reference; its contents stay opaque
	// here and are resolved by a future runner, never by lint.
	Image string `json:"image"`

	// Env declares explicit environment variables. Value is a
	// required, non-null string; an explicit empty string is valid.
	Env []EnvVar `json:"env,omitzero"`
}

// EnvVar is one explicit container environment variable. Value is a
// pointer so validation can distinguish an absent value from an
// explicit empty string.
type EnvVar struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
}
