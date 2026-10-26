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

package deployment

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// newTestDeployment builds a Deployment whose regular, sidecar, and
// init containers are distinguishable by name and image.
func newTestDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic-controller"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "manager", Image: "chart:main"},
						{Name: "sidecar", Image: "chart:sidecar"},
					},
					InitContainers: []corev1.Container{
						{Name: "copy-manifests", Image: "chart:init"},
					},
				},
			},
		},
	}
}

// container returns a deep copy of the named container from the given
// slice, failing the test when the name is absent.
func container(
	t *testing.T,
	containers []corev1.Container,
	name string,
) corev1.Container {
	t.Helper()

	idx, ok := k8s.FindContainer(containers, name)
	if !ok {
		t.Fatalf("container %q not found", name)
	}

	return *containers[idx].DeepCopy()
}

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

// TestCustomizeNilDeploymentErrors pins that a nil Deployment is
// rejected instead of panicked on.
func TestCustomizeNilDeploymentErrors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := Customize(nil, WithContainerImage("manager", "img:1"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("nil Deployment"))
}

// TestCustomizeNoOptionsAndNilOptions pins that a non-nil Deployment
// without options succeeds unchanged, and that nil options — a nil
// interface value or a nil functional adapter — are ignored rather
// than applied.
func TestCustomizeNoOptionsAndNilOptions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	d := newTestDeployment()

	g.Expect(Customize(d)).To(Succeed())

	var nilTyped CustomizeOption = option.FunctionalOption[CustomizeOptions](nil)
	g.Expect(Customize(d, nil, nilTyped)).To(Succeed())

	// No option ran, so the Deployment is byte-for-byte the fixture.
	g.Expect(d).To(Equal(newTestDeployment()))
}

// TestCustomizeAppliesRequestsInOrder pins that mixed option kinds and
// repeated image requests apply in their original order: the later
// image request wins, and the env merge sees the container the image
// requests already retargeted by name.
func TestCustomizeAppliesRequestsInOrder(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	d := newTestDeployment()

	err := Customize(d,
		WithContainerImage("manager", "img:first"),
		WithContainerEnv("manager", []corev1.EnvVar{envVar("LOG_LEVEL", "debug")}),
		WithContainerImage("manager", "img:second"),
		WithInitContainerImage("copy-manifests", "img:init"),
	)
	g.Expect(err).To(Succeed())

	main := container(t, d.Spec.Template.Spec.Containers, "manager")
	g.Expect(main.Image).To(Equal("img:second"))
	g.Expect(main.Env).To(Equal([]corev1.EnvVar{envVar("LOG_LEVEL", "debug")}))

	init := container(t, d.Spec.Template.Spec.InitContainers, "copy-manifests")
	g.Expect(init.Image).To(Equal("img:init"))
}

// TestCustomizeMissingContainerErrors pins the strict missing-target
// contract for regular containers: the error names the Deployment, the
// missing container, and the regular role, on both nonempty and empty
// slices, for every request kind.
func TestCustomizeMissingContainerErrors(t *testing.T) {
	t.Parallel()

	t.Run("image on nonempty slice", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		d := newTestDeployment()
		err := Customize(d, WithContainerImage("absent", "img:1"))
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`container "absent" not found in Deployment synthetic-controller`))
	})

	t.Run("image on empty slice", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		d := newTestDeployment()
		d.Spec.Template.Spec.Containers = nil

		err := Customize(d, WithContainerImage("manager", "img:1"))
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`container "manager" not found in Deployment synthetic-controller`))
	})

	t.Run("env on empty slice", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		d := newTestDeployment()
		d.Spec.Template.Spec.Containers = nil

		// An empty env list still resolves the named container, so the
		// missing name is an error rather than a silent no-op.
		err := Customize(d, WithContainerEnv("manager", nil))
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`container "manager" not found in Deployment synthetic-controller`))
	})
}

// TestCustomizeMissingInitContainerErrors pins the strict
// missing-target contract for init containers: the error names the
// Deployment, the missing container, and the init role, on both
// nonempty and empty slices.
func TestCustomizeMissingInitContainerErrors(t *testing.T) {
	t.Parallel()

	t.Run("nonempty slice", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		d := newTestDeployment()
		err := Customize(d, WithInitContainerImage("absent", "img:1"))
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "absent" not found in Deployment synthetic-controller`))
	})

	t.Run("empty slice", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		d := newTestDeployment()
		d.Spec.Template.Spec.InitContainers = nil

		err := Customize(d, WithInitContainerImage("copy-manifests", "img:1"))
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`init container "copy-manifests" not found in Deployment synthetic-controller`))
	})
}

// TestCustomizeEmptyImageIsLiteralAssignment pins that an empty image
// overwrites the existing image rather than being treated as unset.
func TestCustomizeEmptyImageIsLiteralAssignment(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	d := newTestDeployment()

	g.Expect(Customize(d,
		WithContainerImage("manager", ""),
		WithInitContainerImage("copy-manifests", ""),
	)).To(Succeed())

	g.Expect(container(t, d.Spec.Template.Spec.Containers, "manager").Image).To(BeEmpty())
	g.Expect(container(t, d.Spec.Template.Spec.InitContainers, "copy-manifests").Image).To(BeEmpty())
}

// TestCustomizeStopsAtFirstErrorWithoutRollback pins that the pass
// stops at the first failing request, returns its error, and keeps
// the mutations earlier requests already made.
func TestCustomizeStopsAtFirstErrorWithoutRollback(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	d := newTestDeployment()

	err := Customize(d,
		WithContainerImage("manager", "img:resolved"),
		WithContainerImage("absent", "img:never"),
		WithContainerImage("sidecar", "img:unreached"),
	)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(`container "absent" not found`))

	main := container(t, d.Spec.Template.Spec.Containers, "manager")
	g.Expect(main.Image).To(Equal("img:resolved"))

	// The request after the failure never ran.
	g.Expect(container(t, d.Spec.Template.Spec.Containers, "sidecar").Image).To(Equal("chart:sidecar"))
}

// TestCustomizeIsolatesContainerSlicesAndSiblings pins that regular
// image and env requests never touch init containers or sibling
// regular containers, and init image requests never touch regular
// containers.
func TestCustomizeIsolatesContainerSlicesAndSiblings(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	d := newTestDeployment()

	g.Expect(Customize(d,
		WithContainerImage("manager", "img:resolved"),
		WithContainerEnv("manager", []corev1.EnvVar{envVar("LOG_LEVEL", "debug")}),
		WithInitContainerImage("copy-manifests", "img:init"),
	)).To(Succeed())

	g.Expect(container(t, d.Spec.Template.Spec.Containers, "sidecar")).To(Equal(corev1.Container{
		Name:  "sidecar",
		Image: "chart:sidecar",
	}))

	init := container(t, d.Spec.Template.Spec.InitContainers, "copy-manifests")
	g.Expect(init.Image).To(Equal("img:init"))
	g.Expect(init.Env).To(BeEmpty())

	main := container(t, d.Spec.Template.Spec.Containers, "manager")
	g.Expect(main.Image).To(Equal("img:resolved"))
	g.Expect(main.Env).To(Equal([]corev1.EnvVar{envVar("LOG_LEVEL", "debug")}))
}

// TestCustomizeEnvMerge pins the merge mechanics on an existing env
// list: the first same-name entry is replaced in place by the full
// incoming EnvVar, later pre-existing duplicates are untouched, new
// names append in incoming order, later incoming duplicates win, and
// repeated requests merge over the earlier result.
func TestCustomizeEnvMerge(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	d := newTestDeployment()
	d.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
		envVar("FIRST", "old-first"),
		envVar("KEEP", "keep-value"),
		envVar("FIRST", "old-first-duplicate"),
	}

	g.Expect(Customize(d,
		WithContainerEnv("manager", []corev1.EnvVar{
			envVar("FIRST", "new-first"),
			envVar("APPENDED", "appended-value"),
			envVar("FIRST", "newest-first"),
		}),
		WithContainerEnv("manager", []corev1.EnvVar{envVar("REPEATED", "repeated-value")}),
	)).To(Succeed())

	// The first FIRST is replaced by the last incoming FIRST, KEEP
	// keeps its position, the duplicate FIRST after it is untouched,
	// and new names append in incoming order across both requests.
	g.Expect(d.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{
		envVar("FIRST", "newest-first"),
		envVar("KEEP", "keep-value"),
		envVar("FIRST", "old-first-duplicate"),
		envVar("APPENDED", "appended-value"),
		envVar("REPEATED", "repeated-value"),
	}))
}

// TestCustomizeEnvSourceReplacement pins that the full incoming
// EnvVar replaces the old entry in both directions: a literal override
// clears an old valueFrom, and an incoming source replaces an old
// literal without being flattened.
func TestCustomizeEnvSourceReplacement(t *testing.T) {
	t.Parallel()

	t.Run("literal clears source", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		d := newTestDeployment()
		d.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
			configMapEnvVar("SOURCED", "chart-cm", "key"),
		}

		g.Expect(Customize(d,
			WithContainerEnv("manager", []corev1.EnvVar{envVar("SOURCED", "literal")}),
		)).To(Succeed())

		g.Expect(d.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{
			envVar("SOURCED", "literal"),
		}))
	})

	t.Run("source replaces literal", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		d := newTestDeployment()
		d.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
			envVar("SOURCED", "literal"),
		}

		incoming := configMapEnvVar("SOURCED", "operator-cm", "other-key")

		g.Expect(Customize(d,
			WithContainerEnv("manager", []corev1.EnvVar{incoming}),
		)).To(Succeed())

		g.Expect(d.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{incoming}))
	})
}

// TestCustomizeEnvEmptyValueStaysLiteral pins that an empty-value
// entry replaces an existing source entry with a literal empty value
// and no valueFrom.
func TestCustomizeEnvEmptyValueStaysLiteral(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	d := newTestDeployment()
	d.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
		configMapEnvVar("EMPTY", "chart-cm", "key"),
	}

	g.Expect(Customize(d,
		WithContainerEnv("manager", []corev1.EnvVar{envVar("EMPTY", "")}),
	)).To(Succeed())

	g.Expect(d.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{
		{Name: "EMPTY"},
	}))
}
