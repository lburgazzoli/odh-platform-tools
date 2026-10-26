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

	. "github.com/onsi/gomega"
)

// envVar builds a literal env var.
func envVar(name string, value string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, Value: value}
}

// configMapEnvVar builds a valueFrom env var sourced from a key in a
// named ConfigMap.
func configMapEnvVar(name string, refName string, refKey string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: refName},
				Key:                  refKey,
			},
		},
	}
}

// TestFindContainer pins the first exact-name match over regular and
// init container slices, and the miss on absent names and empty lists.
func TestFindContainer(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	containers := []corev1.Container{
		{Name: "manager"},
		{Name: "sidecar"},
		{Name: "manager"},
	}

	idx, ok := FindContainer(containers, "manager")
	g.Expect(ok).To(BeTrue())
	g.Expect(idx).To(Equal(0))

	idx, ok = FindContainer(containers, "sidecar")
	g.Expect(ok).To(BeTrue())
	g.Expect(idx).To(Equal(1))

	_, ok = FindContainer(containers, "absent")
	g.Expect(ok).To(BeFalse())

	_, ok = FindContainer(nil, "manager")
	g.Expect(ok).To(BeFalse())
}

// TestMergeEnv pins the merge mechanics: the first same-name entry is
// replaced in place by the full incoming EnvVar — a literal override
// clears an old valueFrom, an incoming source replaces an old literal —
// and a new name appends while unrelated entries and later
// pre-existing duplicates keep their order.
func TestMergeEnv(t *testing.T) {
	t.Parallel()

	t.Run("replace first match, keep duplicate and order", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		envs := []corev1.EnvVar{
			envVar("FIRST", "old-first"),
			envVar("KEEP", "keep-value"),
			envVar("FIRST", "old-first-duplicate"),
		}

		merged := MergeEnv(envs, envVar("FIRST", "new-first"))

		g.Expect(merged).To(Equal([]corev1.EnvVar{
			envVar("FIRST", "new-first"),
			envVar("KEEP", "keep-value"),
			envVar("FIRST", "old-first-duplicate"),
		}))
	})

	t.Run("append new name", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		merged := MergeEnv([]corev1.EnvVar{envVar("KEEP", "keep-value")}, envVar("NEW", "new-value"))

		g.Expect(merged).To(Equal([]corev1.EnvVar{
			envVar("KEEP", "keep-value"),
			envVar("NEW", "new-value"),
		}))
	})

	t.Run("append to nil list", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		merged := MergeEnv(nil, envVar("NEW", "new-value"))

		g.Expect(merged).To(Equal([]corev1.EnvVar{envVar("NEW", "new-value")}))
	})

	t.Run("literal clears source", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		merged := MergeEnv(
			[]corev1.EnvVar{configMapEnvVar("SOURCED", "chart-cm", "key")},
			envVar("SOURCED", "literal"),
		)

		g.Expect(merged).To(Equal([]corev1.EnvVar{envVar("SOURCED", "literal")}))
	})

	t.Run("source replaces literal", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		incoming := configMapEnvVar("SOURCED", "operator-cm", "other-key")

		merged := MergeEnv([]corev1.EnvVar{envVar("SOURCED", "literal")}, incoming)

		g.Expect(merged).To(Equal([]corev1.EnvVar{incoming}))
	})
}
