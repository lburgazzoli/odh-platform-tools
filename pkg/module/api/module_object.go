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

package api

import (
	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModuleStatus is the framework status reported by a mock module.
// +kubebuilder:object:generate=true
type ModuleStatus struct {
	frameworkapi.Status                 `json:",inline"`
	frameworkapi.ComponentReleaseStatus `json:",inline"`
}

// ModuleObject adapts the selected custom resource to the platform framework.
// +kubebuilder:object:root=true
type ModuleObject struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`
	Status            ModuleStatus `json:"status,omitzero"`
}

// ModuleObjectList is the list for the runtime-selected resource.
// +kubebuilder:object:root=true
type ModuleObjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ModuleObject `json:"items"`
}

var _ frameworkapi.PlatformObject = (*ModuleObject)(nil)
var _ frameworkapi.WithReleases = (*ModuleObject)(nil)

// GetStatus implements frameworkapi.WithStatus.
func (obj *ModuleObject) GetStatus() *frameworkapi.Status {
	return &obj.Status.Status
}

// GetConditions returns the module's condition observations.
func (obj *ModuleObject) GetConditions() []frameworkapi.Condition {
	return obj.Status.GetConditions()
}

// SetConditions replaces the module's condition observations.
func (obj *ModuleObject) SetConditions(conditions []frameworkapi.Condition) {
	obj.Status.SetConditions(conditions)
}

// GetReleaseStatus returns the module's release report.
func (obj *ModuleObject) GetReleaseStatus() *frameworkapi.ComponentReleaseStatus {
	return &obj.Status.ComponentReleaseStatus
}

// SetReleaseStatus copies the release report into the module's status.
func (obj *ModuleObject) SetReleaseStatus(status frameworkapi.ComponentReleaseStatus) {
	status.DeepCopyInto(&obj.Status.ComponentReleaseStatus)
}
