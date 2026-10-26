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

package kustomize

import (
	"testing"

	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestNewBuildsNativeKustomizeRenderer proves the backend constructor
// returns the configured native renderer whose Process output is the
// plain kustomize build: every authored object of the fixture is
// present, no renderer-added metadata appears, and the empty
// namespace registers no plugin at all — namespaced objects keep
// their authored (absent) namespace, exactly like the offline lint
// path. Native Process order is not guaranteed, so presence is
// asserted per object, never as a full-slice comparison.
func TestNewBuildsNativeKustomizeRenderer(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	definition := loadDefinition(t, root)

	native, err := New(definition, root+"/definition.yaml", "")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(native).NotTo(BeNil())
	g.Expect(native.Name()).NotTo(BeEmpty())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())

	// Every authored object of the fixture is present.
	deployment := findResource(t, resources, "Deployment", "module-controller")
	service := findResource(t, resources, "Service", "module-service")
	roleBinding := findResource(t, resources, "RoleBinding", "module-rolebinding")
	crd := findResource(t, resources, "CustomResourceDefinition", "syntheticmodules.components.platform.opendatahub.io")

	// No plugin ran: namespaced objects keep their authored —
	// absent — namespace, and the RoleBinding subject carries none.
	g.Expect(deployment.GetNamespace()).To(BeEmpty())
	g.Expect(service.GetNamespace()).To(BeEmpty())
	g.Expect(roleBinding.GetNamespace()).To(BeEmpty())
	g.Expect(crd.GetNamespace()).To(BeEmpty())

	// No renderer-added annotations: the explicit construction flags
	// keep content hash and source annotations off.
	g.Expect(deployment.GetAnnotations()).To(BeEmpty())

	// The overlay patch applied during the native build.
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(replicas).To(Equal(int64(2)))
}

// TestNewKustomizeNamespaceTransformsThroughNativePlugin proves a
// nonempty namespace registers the renderer's real builtin namespace
// transformer: namespaced objects, RoleBinding subjects, and
// cross-object references receive the namespace while cluster-scoped
// objects keep their scope. The transformation is the native
// renderer's own — construction only configures it.
func TestNewKustomizeNamespaceTransformsThroughNativePlugin(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	definition := loadDefinition(t, root)

	native, err := New(definition, root+"/definition.yaml", "modules")
	g.Expect(err).NotTo(HaveOccurred())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())

	deployment := findResource(t, resources, "Deployment", "module-controller")
	service := findResource(t, resources, "Service", "module-service")
	roleBinding := findResource(t, resources, "RoleBinding", "module-rolebinding")
	crd := findResource(t, resources, "CustomResourceDefinition", "syntheticmodules.components.platform.opendatahub.io")

	// Namespaced objects receive the namespace.
	g.Expect(deployment.GetNamespace()).To(Equal("modules"))
	g.Expect(service.GetNamespace()).To(Equal("modules"))
	g.Expect(roleBinding.GetNamespace()).To(Equal("modules"))

	// The RoleBinding subject without an authored namespace gets it.
	subjects, found, err := unstructured.NestedSlice(roleBinding.Object, "subjects")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(subjects).To(HaveLen(1))
	g.Expect(subjects[0].(map[string]any)["namespace"]).To(Equal("modules"))

	// Cluster-scoped objects keep their scope.
	g.Expect(crd.GetNamespace()).To(BeEmpty())
}
