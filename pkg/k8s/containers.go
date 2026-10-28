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
	corev1 "k8s.io/api/core/v1"
)

// FindContainer returns the first container index matching name.
func FindContainer(containers []corev1.Container, name string) (int, bool) {
	for i := range containers {
		if containers[i].Name == name {
			return i, true
		}
	}

	return 0, false
}

// MergeEnv replaces the first same-name entry or appends env, preserving order.
// Replacement clears any old valueFrom. Env is stored without a deep copy.
func MergeEnv(envs []corev1.EnvVar, env corev1.EnvVar) []corev1.EnvVar {
	for i := range envs {
		if envs[i].Name == env.Name {
			envs[i] = env

			return envs
		}
	}

	return append(envs, env)
}
