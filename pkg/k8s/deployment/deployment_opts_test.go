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

	corev1 "k8s.io/api/core/v1"

	. "github.com/onsi/gomega"
)

// TestWithContainerEnvSnapshotsInput pins that the option snapshots
// the caller's env list at construction: later caller mutations of
// the slice or of nested source data do not reach the request.
func TestWithContainerEnvSnapshotsInput(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	envs := []corev1.EnvVar{
		configMapEnvVar("SOURCED", "operator-cm", "key"),
		envVar("LITERAL", "value"),
	}

	opt := WithContainerEnv("manager", envs)

	// Mutate both the slice structure and the nested source after
	// constructing the option.
	source := envs[0].ValueFrom
	envs[0] = envVar("REPLACED", "caller-mutation")
	source.ConfigMapKeyRef.Key = "caller-mutation"

	d := newTestDeployment()
	g.Expect(Customize(d, opt)).To(Succeed())

	g.Expect(d.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{
		configMapEnvVar("SOURCED", "operator-cm", "key"),
		envVar("LITERAL", "value"),
	}))
}

// TestOptionReuseAcrossDeployments pins that one option applied to
// two Deployments never shares writable nested state: mutating the
// first result's valueFrom leaves the second result and the original
// input untouched.
func TestOptionReuseAcrossDeployments(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	envs := []corev1.EnvVar{configMapEnvVar("SOURCED", "operator-cm", "key")}
	opt := WithContainerEnv("manager", envs)

	first := newTestDeployment()
	second := newTestDeployment()

	g.Expect(Customize(first, opt)).To(Succeed())
	g.Expect(Customize(second, opt)).To(Succeed())

	// Corrupt the first result's nested source.
	first.Spec.Template.Spec.Containers[0].Env[0].ValueFrom.ConfigMapKeyRef.Key = "corrupted"

	g.Expect(second.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{
		configMapEnvVar("SOURCED", "operator-cm", "key"),
	}))
	g.Expect(envs).To(Equal([]corev1.EnvVar{
		configMapEnvVar("SOURCED", "operator-cm", "key"),
	}))

	// The corrupted result itself keeps its corruption, proving the
	// two applications did not alias one shared snapshot.
	g.Expect(first.Spec.Template.Spec.Containers[0].Env[0].ValueFrom.ConfigMapKeyRef.Key).To(Equal("corrupted"))
}

// TestWithContainerOptionsCollectOrderedRequests pins that repeated
// options accumulate requests in order inside one CustomizeOptions
// target, so a whole options list applies as one bundled pass.
func TestWithContainerOptionsCollectOrderedRequests(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var options CustomizeOptions

	opts := []CustomizeOption{
		WithContainerImage("manager", "img:main"),
		WithInitContainerImage("copy-manifests", "img:init"),
		WithContainerEnv("manager", []corev1.EnvVar{envVar("LOG_LEVEL", "debug")}),
	}

	for _, opt := range opts {
		opt.ApplyTo(&options)
	}

	d := newTestDeployment()

	for _, request := range options.requests {
		g.Expect(request(d)).To(Succeed())
	}

	main := container(t, d.Spec.Template.Spec.Containers, "manager")
	g.Expect(main.Image).To(Equal("img:main"))
	g.Expect(main.Env).To(Equal([]corev1.EnvVar{envVar("LOG_LEVEL", "debug")}))

	g.Expect(container(t, d.Spec.Template.Spec.InitContainers, "copy-manifests").Image).To(Equal("img:init"))
}
