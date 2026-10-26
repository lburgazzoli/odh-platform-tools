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
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	fwgvk "github.com/opendatahub-io/odh-platform-utilities/framework/cluster/gvk"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// ErrReservedResource is the sentinel for rendered objects a module
// run must never deploy: the configured module type's own instances,
// PlatformModuleDefinition objects, and the persisted definition
// custom resource definition.
var ErrReservedResource = errors.New("reserved rendered resource")

// CheckReserved is the shared payload guard every render path — lint
// and run alike — applies to its own output: it rejects the objects a
// module run must never deploy, whatever backend rendered them.
//
// Reserved are instances of the definition's configured module type
// (any version), any config.opendatahub.io PlatformModuleDefinition,
// and the persisted definition custom resource definition under its
// reserved name. A rejected object fails the render itself; it is
// never silently dropped, so no caller can deploy it by accident.
// All other rendered resources pass through unexamined.
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
			return fmt.Errorf("%w: module instance %s %s", ErrReservedResource, gvk, res.GetName())
		case gvk.Group == toolApi.GroupVersion.Group && gvk.Kind == toolApi.Kind:
			return fmt.Errorf("%w: definition object %s %s", ErrReservedResource, gvk, res.GetName())
		case gvk == fwgvk.CustomResourceDefinition && res.GetName() == toolApi.CRDName:
			return fmt.Errorf("%w: definition custom resource definition %s", ErrReservedResource, res.GetName())
		}
	}

	return nil
}
