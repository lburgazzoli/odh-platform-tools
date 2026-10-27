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

package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	. "github.com/onsi/gomega"
)

// typedConfigMap builds the typed ConfigMap target — always the same
// platform config object, only its namespace varies — with its
// group/version/kind stamped, as the render-time callers do.
func typedConfigMap(namespace string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "odh-synthetic-config", Namespace: namespace},
	}
	cm.SetGroupVersionKind(ConfigMap)

	return cm
}

// renderedConfigMap builds an unstructured ConfigMap standing in for
// one rendered resource.
func renderedConfigMap(name string, namespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ConfigMap)
	u.SetName(name)
	u.SetNamespace(namespace)

	return u
}

// TestIs pins the identity Is compares: group/version/kind,
// namespace, and name.
func TestIs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	g.Expect(Is(
		renderedConfigMap("odh-synthetic-config", "opendatahub"),
		typedConfigMap("opendatahub"),
	)).To(BeTrue(), "same identity matches")

	g.Expect(Is(
		renderedConfigMap("odh-synthetic-config", "rendered-namespace"),
		typedConfigMap("opendatahub"),
	)).To(BeFalse(), "different namespace never matches")

	g.Expect(Is(
		renderedConfigMap("odh-other-config", "opendatahub"),
		typedConfigMap("opendatahub"),
	)).To(BeFalse(), "different name never matches")
}

// TestIsRequiresStampedIdentity pins that an object without a stamped
// group/version/kind — a freshly built typed object before stamping,
// or a rendered map without apiVersion/kind — never matches on either
// side of the comparison, so unstamped objects cannot silently match
// each other.
func TestIsRequiresStampedIdentity(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	unstampedTarget := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "odh-synthetic-config"}}
	g.Expect(unstampedTarget.GroupVersionKind().Empty()).To(BeTrue())

	g.Expect(Is(renderedConfigMap("odh-synthetic-config", ""), unstampedTarget)).
		To(BeFalse(), "unstamped target never matches")

	unstampedCandidate := &unstructured.Unstructured{}
	unstampedCandidate.SetName("odh-synthetic-config")

	g.Expect(Is(unstampedCandidate, typedConfigMap("opendatahub"))).
		To(BeFalse(), "unstamped candidate never matches")

	g.Expect(Is(unstampedCandidate, unstampedTarget)).
		To(BeFalse(), "two unstamped objects never match")
}

// TestIsRejectsKindAndVersionMismatches pins that the group/version
// and kind are part of the identity: same-name lookalikes of another
// kind or version are not the same object.
func TestIsRejectsKindAndVersionMismatches(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lookalike := &unstructured.Unstructured{}
	lookalike.SetGroupVersionKind(Deployment)
	lookalike.SetName("odh-synthetic-config")

	g.Expect(Is(lookalike, typedConfigMap(""))).
		To(BeFalse(), "different kind never matches")

	v2 := &unstructured.Unstructured{}
	v2.SetGroupVersionKind(schema.GroupVersionKind{Version: "v2", Kind: "ConfigMap"})
	v2.SetName("odh-synthetic-config")

	g.Expect(Is(v2, typedConfigMap(""))).
		To(BeFalse(), "different version never matches")
}

// decodeStreamYAML holds two ConfigMaps around a comment-only document
// and a kindless one, both skipped rather than failing the stream.
const decodeStreamYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: first
---
# comment-only document: skipped
---
metadata:
  name: kindless
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: second
`

// decodeUnknownGroupYAML carries a group no scheme knows, standing in
// for the unknown-GVK lookalikes the render output can hold.
const decodeUnknownGroupYAML = `apiVersion: wrongplatform.example.com/v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: opendatahub
`

// decodeMalformedYAML is not valid YAML: the flow sequence never
// closes.
const decodeMalformedYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: broken
  labels: [unterminated
`

// TestDecodeResources pins the stream semantics: documents decode in
// order, and empty or kindless documents are skipped.
func TestDecodeResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	resources, err := DecodeResources([]byte(decodeStreamYAML))
	g.Expect(err).To(Succeed())
	g.Expect(resources).To(HaveLen(2))
	g.Expect(resources[0].GetName()).To(Equal("first"))
	g.Expect(resources[1].GetName()).To(Equal("second"))
}

// TestDecodeResourcesAcceptsUnknownGroups pins that decoding targets
// unstructured values only: a group absent from every scheme decodes
// exactly like a core one.
func TestDecodeResourcesAcceptsUnknownGroups(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	resources, err := DecodeResources([]byte(decodeUnknownGroupYAML))
	g.Expect(err).To(Succeed())
	g.Expect(resources).To(HaveLen(1))
	g.Expect(resources[0].GroupVersionKind()).To(Equal(schema.GroupVersionKind{
		Group:   "wrongplatform.example.com",
		Version: "v1",
		Kind:    "ConfigMap",
	}))
}

// TestDecodeResourcesEmptyContent pins that empty input decodes to an
// empty slice without error.
func TestDecodeResourcesEmptyContent(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	resources, err := DecodeResources(nil)
	g.Expect(err).To(Succeed())
	g.Expect(resources).To(BeEmpty())
}

// TestDecodeResourcesRejectsMalformedYAML pins that a document that is
// not valid YAML fails the stream instead of being skipped.
func TestDecodeResourcesRejectsMalformedYAML(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	_, err := DecodeResources([]byte(decodeMalformedYAML))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("unable to decode"))
}
