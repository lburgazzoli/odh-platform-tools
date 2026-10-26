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

// WithContainerImage assigns the image to the first regular container
// with the exact name. The image is assigned literally, including the
// empty string; a missing name is an error at apply time.
func WithContainerImage(name string, image string) CustomizeOption {
	return option.FunctionalOption[CustomizeOptions](func(target *CustomizeOptions) {
		target.requests = append(target.requests, containerImage(name, image))
	})
}

// WithInitContainerImage assigns the image to the first init container
// with the exact name. The image is assigned literally, including the
// empty string; a missing name is an error at apply time.
func WithInitContainerImage(name string, image string) CustomizeOption {
	return option.FunctionalOption[CustomizeOptions](func(target *CustomizeOptions) {
		target.requests = append(target.requests, initContainerImage(name, image))
	})
}

// WithContainerEnv merges the env vars into the first regular
// container with the exact name: the first same-name entry is replaced
// in place or the entry is appended, so later incoming entries and
// repeated requests win. The input is snapshotted at construction and
// deep-copied again at apply time, so later caller mutations do not
// affect the request and a reused option never shares writable nested
// state across Deployments. A missing container name is an error at
// apply time, even for an empty env list.
func WithContainerEnv(name string, envs []corev1.EnvVar) CustomizeOption {
	return withContainerEnv(containerEnv, name, envs)
}

// WithInitContainerEnv merges the env vars into the first init
// container with the exact name, with the same merge, snapshot, and
// error semantics as WithContainerEnv.
func WithInitContainerEnv(name string, envs []corev1.EnvVar) CustomizeOption {
	return withContainerEnv(initContainerEnv, name, envs)
}

// withContainerEnv builds an env-merging option around the request
// constructor, snapshotting the input at construction time.
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
