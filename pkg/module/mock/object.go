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

// Package mock implements the platform module status simulator.
package mock

import (
	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type moduleStatus struct {
	frameworkapi.Status                 `json:",inline"`
	frameworkapi.ComponentReleaseStatus `json:",inline"`
}

type moduleObject struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`
	Status            moduleStatus `json:"status,omitzero"`
}

type moduleObjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []moduleObject `json:"items"`
}

var _ frameworkapi.PlatformObject = (*moduleObject)(nil)
var _ frameworkapi.WithReleases = (*moduleObject)(nil)

func newModuleScheme(gvk schema.GroupVersionKind) *runtime.Scheme {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(gvk, &moduleObject{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &moduleObjectList{})

	return scheme
}

func (obj *moduleObject) GetStatus() *frameworkapi.Status {
	return &obj.Status.Status
}

func (obj *moduleObject) GetConditions() []frameworkapi.Condition {
	return obj.Status.GetConditions()
}

func (obj *moduleObject) SetConditions(conditions []frameworkapi.Condition) {
	obj.Status.SetConditions(conditions)
}

func (obj *moduleObject) GetReleaseStatus() *frameworkapi.ComponentReleaseStatus {
	return &obj.Status.ComponentReleaseStatus
}

func (obj *moduleObject) SetReleaseStatus(status frameworkapi.ComponentReleaseStatus) {
	status.DeepCopyInto(&obj.Status.ComponentReleaseStatus)
}

func (obj *moduleObject) DeepCopyObject() runtime.Object {
	if obj == nil {
		return nil
	}

	out := new(moduleObject)
	obj.deepCopyInto(out)

	return out
}

func (obj *moduleObject) deepCopyInto(out *moduleObject) {
	*out = *obj
	obj.DeepCopyInto(&out.ObjectMeta)
	obj.Status.Status.DeepCopyInto(&out.Status.Status)
	obj.Status.ComponentReleaseStatus.DeepCopyInto(&out.Status.ComponentReleaseStatus)
}

func (list *moduleObjectList) DeepCopyObject() runtime.Object {
	if list == nil {
		return nil
	}

	out := new(moduleObjectList)
	*out = *list
	list.DeepCopyInto(&out.ListMeta)
	if list.Items != nil {
		out.Items = make([]moduleObject, len(list.Items))
		for i := range list.Items {
			list.Items[i].deepCopyInto(&out.Items[i])
		}
	}

	return out
}
