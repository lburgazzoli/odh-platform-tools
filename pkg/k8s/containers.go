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

// FindContainer returns the index of the first container in the slice
// carrying the exact name, for regular and init container lists alike.
func FindContainer(containers []corev1.Container, name string) (int, bool) {
	for i := range containers {
		if containers[i].Name == name {
			return i, true
		}
	}

	return 0, false
}

// MergeEnv merges one env var into the list: the first entry with the
// same name is replaced in place by the full incoming EnvVar — a
// literal override clears an old valueFrom, an incoming source
// replaces an old literal — and a new name is appended. Unrelated
// entries and later pre-existing duplicates keep their order. The
// incoming EnvVar is stored as given; deep-copying it is the caller's
// choice.
func MergeEnv(envs []corev1.EnvVar, env corev1.EnvVar) []corev1.EnvVar {
	for i := range envs {
		if envs[i].Name == env.Name {
			envs[i] = env

			return envs
		}
	}

	return append(envs, env)
}
