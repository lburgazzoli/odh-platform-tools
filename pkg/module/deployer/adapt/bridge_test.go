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

package adapt

import (
	"testing"

	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestControllerBridgeMatchesOperatorOwnership proves the bridge
// carries the operator's modules-controller ownership policy: no
// static ownership, dynamic ownership enabled, core/v1 Namespaces and
// CRDs excluded, and Owns answering from the dynamic-owned set the
// action populates.
func TestControllerBridgeMatchesOperatorOwnership(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	bridge := NewControllerBridge(nil)

	module := schema.GroupVersionKind{
		Group:   "components.platform.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "SyntheticModule",
	}

	g.Expect(bridge.Owns(module)).To(BeFalse())
	g.Expect(bridge.IsDynamicOwnershipEnabled()).To(BeTrue())

	bridge.AddDynamicOwnedType(module)
	g.Expect(bridge.Owns(module)).To(BeTrue())

	namespace := schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}
	crd := apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")
	deployment := appsv1.SchemeGroupVersion.WithKind("Deployment")
	g.Expect(bridge.IsExcludedFromDynamicOwnership(namespace)).To(BeTrue())
	g.Expect(bridge.IsExcludedFromDynamicOwnership(crd)).To(BeTrue())
	g.Expect(bridge.IsExcludedFromDynamicOwnership(deployment)).To(BeFalse())
}
