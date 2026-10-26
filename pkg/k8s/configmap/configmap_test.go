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

package configmap

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/gomega"
)

// newTestConfigMap builds a ConfigMap with a two-key data block.
func newTestConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic-config"},
		Data: map[string]string{
			"KEEP": "keep-value",
			"OLD":  "old-value",
		},
	}
}

// TestCustomizeNilConfigMapErrors pins that a nil target is rejected.
func TestCustomizeNilConfigMapErrors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := Customize(nil, WithData("K", "v"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("cannot customize a nil ConfigMap"))
}

// TestCustomizeNoOptionsAndNilOptions pins that a non-nil ConfigMap
// without options succeeds unchanged and nil options are ignored.
func TestCustomizeNoOptionsAndNilOptions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cm := newTestConfigMap()
	g.Expect(Customize(cm)).To(Succeed())
	g.Expect(Customize(cm, nil, nil)).To(Succeed())
	g.Expect(cm).To(Equal(newTestConfigMap()))
}

// TestCustomizeAppliesRequestsInOrder pins the ordered application:
// a key set and later removed is gone, a key removed and later set is
// present, and untouched keys survive both.
func TestCustomizeAppliesRequestsInOrder(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cm := newTestConfigMap()
	g.Expect(Customize(cm,
		WithData("GONE", "value"),
		WithData("OLD", "new-value"),
		Remove("GONE"),
		Remove("KEEP"),
		WithData("KEEP", "back-again"),
	)).To(Succeed())

	g.Expect(cm.Data).To(Equal(map[string]string{
		"KEEP": "back-again",
		"OLD":  "new-value",
	}))
}

// TestCustomizeWithDataMaterializesNilData pins that setting a key on
// a ConfigMap without a data block creates the block with that entry
// alone.
func TestCustomizeWithDataMaterializesNilData(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "synthetic-config"}}
	g.Expect(Customize(cm, WithData("platformVersion", "2.19.0"))).To(Succeed())

	g.Expect(cm.Data).To(Equal(map[string]string{"platformVersion": "2.19.0"}))
}

// TestCustomizeRemoveIsNoOpOnMissingKeys pins that removing keys a
// missing data block — or a present one — does not carry, and that a
// remove on a ConfigMap without data does not materialize the block.
func TestCustomizeRemoveIsNoOpOnMissingKeys(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cm := newTestConfigMap()
	g.Expect(Customize(cm, Remove("ABSENT", "ALSO-ABSENT"))).To(Succeed())
	g.Expect(cm.Data).To(Equal(newTestConfigMap().Data))

	empty := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "synthetic-config"}}
	g.Expect(Customize(empty, Remove("ABSENT"))).To(Succeed())
	g.Expect(empty.Data).To(BeNil())
}

// TestOptionReuseAcrossConfigMaps pins that one option applied to two
// ConfigMaps never shares writable state: each target gets its own
// data block, so mutating the first result leaves the second intact.
func TestOptionReuseAcrossConfigMaps(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	opts := []CustomizeOption{
		WithData("SHARED", "value"),
		Remove("OLD"),
	}

	first := newTestConfigMap()
	second := newTestConfigMap()

	g.Expect(Customize(first, opts...)).To(Succeed())
	g.Expect(Customize(second, opts...)).To(Succeed())

	first.Data["SHARED"] = "mutated"

	g.Expect(second.Data).To(Equal(map[string]string{
		"KEEP":   "keep-value",
		"SHARED": "value",
	}))
}
