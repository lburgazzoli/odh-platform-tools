/*
Copyright 2026 Luca Burgazzoli

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// See groupversion_info.go for the package documentation.

package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"
)

// Constants describing the definition envelope.
const (
	// APIVersion is the definition API version. It is the string form
	// of GroupVersion.
	APIVersion = "config.opendatahub.io/v1alpha1"

	// Kind is the definition kind.
	Kind = "PlatformModuleDefinition"

	// DefaultMainContainerName is the controller main container name
	// applied when the definition omits it.
	DefaultMainContainerName = "manager"
)

// Format identifies the manifest backend a definition selects.
// +kubebuilder:validation:Type=string
type Format string

// The supported manifest backends.
const (
	// FormatHelm renders the module from a local Helm chart.
	FormatHelm Format = "helm"

	// FormatKustomize renders the module from a local Kustomize tree.
	FormatKustomize Format = "kustomize"
)

// PlatformModuleDefinition describes a module: its custom resource
// instance identity, its manifest source, and its controller
// deployment. Authored as a file, it is also the persisted,
// cluster-scoped deployment owner once installed.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:storageversion
// +genclient
// +genclient:nonNamespaced
type PlatformModuleDefinition struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec PlatformModuleDefinitionSpec `json:"spec"`

	// +optional
	Status PlatformModuleDefinitionStatus `json:"status,omitzero"`
}

// PlatformModuleDefinitionStatus is the observed state of a module
// definition.
type PlatformModuleDefinitionStatus struct {
	api.Status `json:",inline"`
}

// PlatformModuleDefinitionList holds a list of module definitions.
//
// +kubebuilder:object:root=true
type PlatformModuleDefinitionList struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	// +required
	Items []PlatformModuleDefinition `json:"items"`
}

// PlatformModuleDefinitionSpec is the desired module state: the
// custom resource instance the controller owns, the manifest backend
// that renders it, and the controller deployment.
type PlatformModuleDefinitionSpec struct {
	// +required
	Instance Instance `json:"instance"`

	// +required
	Manifests Manifests `json:"manifests"`

	// +optional
	RelatedImages []string `json:"relatedImages,omitzero"`

	// +required
	Controller Controller `json:"controller"`
}

// Instance identifies the module's custom resource: the resource type
// the module controller owns and the instance name that custom
// resource would carry. Both are descriptive identity: nothing in
// this tooling creates or looks up the custom resource, and rendering
// never uses them — the Helm release name comes from metadata.name.
type Instance struct {
	// Name is the module instance name. It is empty when the
	// definition omits it or authors an explicit empty string; both
	// are equivalent here, and the loader then defaults it to
	// metadata.name.
	//
	// +optional
	Name string `json:"name,omitzero"`

	// Type is the Module custom resource type the controller owns.
	//
	// +required
	Type ModuleType `json:"type"`
}

// ModuleType identifies the Module custom resource the module
// controller owns. The wire members are lower-case group, version,
// and kind strings.
type ModuleType struct {
	// +required
	Group string `json:"group"`

	// +required
	Version string `json:"version"`

	// +required
	Kind string `json:"kind"`
}

// GroupVersionKind returns the module type as a schema.GroupVersionKind.
func (t ModuleType) GroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: t.Group, Version: t.Version, Kind: t.Kind}
}

// String returns the module type in the schema.GroupVersionKind
// format, matching the embedded GVK behavior of the previous wire
// type.
func (t ModuleType) String() string {
	return t.GroupVersionKind().String()
}

// Manifests selects a rendering backend and its single local input
// directory. The block matching format may be omitted — its entry
// path then defaults to the definition file's own directory — or
// given with an empty path for the same co-located default; the other
// block must be absent.
type Manifests struct {
	// +required
	Format Format `json:"format"`

	// +optional
	Helm *HelmManifests `json:"helm,omitzero"`

	// +optional
	Kustomize *KustomizeManifests `json:"kustomize,omitzero"`
}

// HelmManifests locates the Helm chart root, relative to the module
// root, and carries authored chart values. The chart release name is
// the definition metadata.name.
type HelmManifests struct {
	// Path is the chart root, relative to the module root. It is
	// empty when the definition omits it or authors an explicit
	// empty string; both are equivalent here, and the entry then
	// defaults to the definition file's own directory — the
	// co-located layout where Chart.yaml sits next to the definition
	// file. Existence and shape of the chart are the render engine's
	// to validate.
	//
	// +optional
	Path string `json:"path,omitzero"`

	// Values are authored literal Helm values, merged over the chart
	// defaults exactly as the operator carries its module handler
	// values: chart defaults apply first and definition values
	// override them. No interpolation, placeholder resolution, or
	// secret handling exists here — {{NAME}}-looking text stays
	// literal, matching the env contract. The canonical
	// apiextensions JSON type carries the arbitrary authored
	// content; the pinned controller-gen cannot deepcopy
	// interface{}-valued maps, and it is the only representation
	// whose CRD schema accepts every JSON value kind.
	//
	// +optional
	Values map[string]apiextensionsv1.JSON `json:"values,omitzero"`
}

// KustomizeManifests locates the directory containing
// kustomization.yaml, relative to the module root.
type KustomizeManifests struct {
	// Path is the directory containing kustomization.yaml (or
	// .yml), relative to the module root. It is empty when the
	// definition omits it or authors an explicit empty string; both
	// are equivalent here, and the entry then defaults to the
	// definition file's own directory — the co-located layout where
	// kustomization.yaml sits next to the definition file.
	// Existence and shape of the entry are the render engine's to
	// validate.
	//
	// +optional
	Path string `json:"path,omitzero"`
}

// Controller describes the module controller deployment.
type Controller struct {
	// +required
	Deployment ControllerDeployment `json:"deployment"`
}

// ControllerDeployment names the controller Deployment and its
// selected containers. The main container carries image and env
// settings; the init container is selected by name and may carry an
// image of its own.
type ControllerDeployment struct {
	// Name is the controller Deployment name. It is empty when the
	// definition omits it or authors an explicit empty string; both
	// are equivalent here, and the deploy then falls back to the
	// module's metadata.name when selecting the controller
	// Deployment.
	//
	// +optional
	Name string `json:"name,omitzero"`

	// Container is the controller main container. An omitted block is
	// materialized by the loader as a block named
	// DefaultMainContainerName carrying no image.
	//
	// +optional
	Container *ContainerDefinition `json:"container,omitzero"`

	// +optional
	InitContainer *ContainerTarget `json:"initContainer,omitzero"`
}

// ContainerDefinition describes the controller's main container. The
// image may be a literal pipeline-built value or a whole-value
// {{VARIABLE}} reference; it must resolve at run time, and it also
// overrides the init container selected by InitContainer unless that
// target carries an image of its own.
type ContainerDefinition struct {
	// Name is the main container name. It is empty when the
	// definition omits it or authors an explicit empty string; both
	// are equivalent here, and the main container then defaults to
	// DefaultMainContainerName when the loader applies its defaults.
	//
	// +optional
	Name string `json:"name,omitzero"`

	// Image is the main container image. It may be a literal or a
	// whole-value {{VARIABLE}} reference; its contents stay opaque
	// here and are resolved by the deployer, never by lint. An image
	// that does not resolve fails the render; the chart default is
	// never kept silently.
	//
	// +required
	Image string `json:"image"`

	// Env declares explicit environment variables merged into the
	// controller containers.
	//
	// +optional
	Env []EnvVar `json:"env,omitzero"`
}

// ContainerTarget selects one controller container by name and may
// carry its own image: a resolved target image overrides the named
// init container directly, and when the target carries no image the
// resolved main image overrides it instead. The injected env vars
// land on the main and init containers alike.
type ContainerTarget struct {
	// Name is the init container name. It is empty when the
	// definition omits it or authors an explicit empty string; both
	// are equivalent here, and an init container requires an explicit
	// name.
	//
	// +optional
	Name string `json:"name,omitzero"`

	// Image is the init container image, distinct from the main
	// container's. It may be a literal or a whole-value {{VARIABLE}}
	// reference; its contents stay opaque here and are resolved by the
	// deployer, never by lint. When it is empty, the main container's
	// image applies instead; the effective image must resolve, and an
	// unresolved one fails the render.
	//
	// +optional
	Image string `json:"image,omitzero"`
}

// EnvVar is one explicit main-container environment variable. Value
// is a pointer so an absent value stays distinguishable from an
// explicit empty string; the persisted schema keeps it optional and
// nullable so all three states survive serialization and admission.
type EnvVar struct {
	// +required
	Name string `json:"name"`

	// +optional
	// +nullable
	Value *string `json:"value"`
}

// CRDName is the persisted definition CRD's full name.
const CRDName = "platformmoduledefinitions.config.opendatahub.io"

// PlatformModuleDefinition satisfies the framework platform-object
// interface: it is a client object with a status block.
var _ api.PlatformObject = (*PlatformModuleDefinition)(nil)

// GetStatus returns the status block.
func (d *PlatformModuleDefinition) GetStatus() *api.Status {
	return &d.Status.Status
}

// GetConditions returns the status conditions.
func (d *PlatformModuleDefinition) GetConditions() []api.Condition {
	return d.Status.Conditions
}

// SetConditions replaces the status conditions with a copy, so later
// mutation of the argument does not change the definition.
func (d *PlatformModuleDefinition) SetConditions(conditions []api.Condition) {
	d.Status.SetConditions(conditions)
}
