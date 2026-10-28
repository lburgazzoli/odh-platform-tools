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

// Package v1alpha1 defines module files and their persisted deployment owners.
//
// +kubebuilder:object:generate=true
// +groupName=config.opendatahub.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group version of the module definition API.
	GroupVersion = schema.GroupVersion{Group: "config.opendatahub.io", Version: "v1alpha1"}

	// SchemeBuilder registers the module definition types.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the module definition types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// addKnownTypes registers the module definition types with a scheme.
func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &PlatformModuleDefinition{}, &PlatformModuleDefinitionList{})
	metav1.AddToGroupVersion(s, GroupVersion)

	return nil
}
