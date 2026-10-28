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

package renderer

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	fwgvk "github.com/opendatahub-io/odh-platform-utilities/framework/cluster/gvk"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// CheckReserved rejects instances of the configured module type (any version),
// PlatformModuleDefinitions, and the definition CRD with ErrReservedResource.
func CheckReserved(
	definition toolApi.PlatformModuleDefinition,
	resources []unstructured.Unstructured,
) error {
	module := definition.Spec.Instance.Type

	for i := range resources {
		res := &resources[i]
		gvk := res.GroupVersionKind()

		switch {
		case gvk.Group == module.Group && gvk.Kind == module.Kind:
			return moduleerrors.New(moduleerrors.ErrReservedResource,
				fmt.Sprintf("reserved rendered resource: module instance %s %s", gvk, res.GetName()))
		case gvk.Group == toolApi.GroupVersion.Group && gvk.Kind == toolApi.Kind:
			return moduleerrors.New(moduleerrors.ErrReservedResource,
				fmt.Sprintf("reserved rendered resource: definition object %s %s", gvk, res.GetName()))
		case gvk == fwgvk.CustomResourceDefinition && res.GetName() == toolApi.CRDName:
			return moduleerrors.New(moduleerrors.ErrReservedResource,
				fmt.Sprintf("reserved rendered resource: definition custom resource definition %s", res.GetName()))
		}
	}

	return nil
}
