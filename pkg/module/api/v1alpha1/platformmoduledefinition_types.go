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
	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"
)

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

// PlatformModuleDefinition describes a module and becomes its cluster-scoped
// deployment owner when persisted.
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

// ControllerDeploymentName returns the configured controller name,
// falling back to the module name.
func (d PlatformModuleDefinition) ControllerDeploymentName() string {
	if name := d.Spec.Controller.Deployment.Name; name != "" {
		return name
	}
	return d.Name
}

// IsControllerDeployment applies the shared controller selection rule.
func (d PlatformModuleDefinition) IsControllerDeployment(gvk schema.GroupVersionKind, name string) bool {
	return gvk == appsv1.SchemeGroupVersion.WithKind("Deployment") && name == d.ControllerDeploymentName()
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

// PlatformModuleDefinitionSpec configures the module identity, manifests, and controller.
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

// Instance describes the module CR identity. This tool does not create the CR.
type Instance struct {
	// Name defaults to metadata.name when empty.
	//
	// +optional
	Name string `json:"name,omitzero"`

	// Type is the Module custom resource type the controller owns.
	//
	// +required
	Type ModuleType `json:"type"`
}

// ModuleType identifies the module CR's group, version, and kind.
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

// String formats the module type as a GVK.
func (t ModuleType) String() string {
	return t.GroupVersionKind().String()
}

// Manifests selects one local rendering backend. An omitted or empty path
// uses the definition file's directory; the other backend block must be absent.
type Manifests struct {
	// +required
	Format Format `json:"format"`

	// +optional
	Helm *HelmManifests `json:"helm,omitzero"`

	// +optional
	Kustomize *KustomizeManifests `json:"kustomize,omitzero"`
}

// HelmManifests locates a chart and supplies literal values.
type HelmManifests struct {
	// Path is the chart root relative to the definition file; empty uses its directory.
	//
	// +optional
	Path string `json:"path,omitzero"`

	// Values override chart defaults without interpolation.
	// JSON supports arbitrary values and controller-gen deepcopy generation.
	//
	// +optional
	Values map[string]apiextensionsv1.JSON `json:"values,omitzero"`
}

// KustomizeManifests locates the directory containing
// kustomization.yaml, relative to the module root.
type KustomizeManifests struct {
	// Path locates the kustomization relative to the definition file;
	// empty uses its directory.
	//
	// +optional
	Path string `json:"path,omitzero"`
}

// Controller describes the module controller deployment.
type Controller struct {
	// +required
	Deployment ControllerDeployment `json:"deployment"`
}

// ControllerDeployment selects the controller Deployment and its containers.
type ControllerDeployment struct {
	// Name defaults to the module's metadata.name when empty.
	//
	// +optional
	Name string `json:"name,omitzero"`

	// Container defaults to a block named DefaultMainContainerName with no image.
	//
	// +optional
	Container *ContainerDefinition `json:"container,omitzero"`

	// +optional
	InitContainer *ContainerTarget `json:"initContainer,omitzero"`
}

// ContainerDefinition configures the main controller container.
type ContainerDefinition struct {
	// Name defaults to DefaultMainContainerName when empty.
	//
	// +optional
	Name string `json:"name,omitzero"`

	// Image is a literal or whole {{VARIABLE}} token. It must resolve during deploy;
	// lint does not resolve image sources.
	//
	// +required
	Image string `json:"image"`

	// Env is merged into the main and configured init containers.
	//
	// +optional
	Env []EnvVar `json:"env,omitzero"`
}

// ContainerTarget selects an init container. Its image defaults to the main
// image, and it receives the same injected environment variables.
type ContainerTarget struct {
	// Name is required when an init container is configured.
	//
	// +optional
	Name string `json:"name,omitzero"`

	// Image is a literal or whole {{VARIABLE}} token; empty uses the main image.
	// The effective image must resolve during deploy.
	//
	// +optional
	Image string `json:"image,omitzero"`
}

// EnvVar is an injected variable. A nil Value preserves absent/null separately
// from an explicitly empty string.
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
