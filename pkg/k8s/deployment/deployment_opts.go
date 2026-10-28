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
	corev1 "k8s.io/api/core/v1"

	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// CustomizeOptions collects the ordered customization requests built
// by the With... options; Customize applies them in order.
type CustomizeOptions struct {
	// requests are the ordered mutations to apply.
	requests []customizationRequest
}

// CustomizeOption customizes a typed Deployment. Options apply in the
// order they are passed; a nil option is ignored.
type CustomizeOption = option.Option[CustomizeOptions]

// WithContainerImage sets the first named container's literal image.
// A missing container fails during Customize.
func WithContainerImage(name string, image string) CustomizeOption {
	return option.FunctionalOption[CustomizeOptions](func(target *CustomizeOptions) {
		target.requests = append(target.requests, containerImage(name, image))
	})
}

// WithInitContainerImage sets the first named init container's literal image.
// A missing container fails during Customize.
func WithInitContainerImage(name string, image string) CustomizeOption {
	return option.FunctionalOption[CustomizeOptions](func(target *CustomizeOptions) {
		target.requests = append(target.requests, initContainerImage(name, image))
	})
}

// WithContainerEnv merges by name, with later values winning.
// Inputs are deep-copied so options can be reused safely across Deployments.
// A missing container fails even with an empty env list.
func WithContainerEnv(name string, envs []corev1.EnvVar) CustomizeOption {
	return withContainerEnv(containerEnv, name, envs)
}

// WithInitContainerEnv applies WithContainerEnv semantics to init containers.
func WithInitContainerEnv(name string, envs []corev1.EnvVar) CustomizeOption {
	return withContainerEnv(initContainerEnv, name, envs)
}

func withContainerEnv(
	request func(name string, envs []corev1.EnvVar) customizationRequest,
	name string,
	envs []corev1.EnvVar,
) CustomizeOption {
	snapshot := make([]corev1.EnvVar, len(envs))
	for i := range envs {
		snapshot[i] = *envs[i].DeepCopy()
	}

	return option.FunctionalOption[CustomizeOptions](func(target *CustomizeOptions) {
		target.requests = append(target.requests, request(name, snapshot))
	})
}
