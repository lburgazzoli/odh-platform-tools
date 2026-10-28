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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
)

// Requests defer fallible mutations until Customize; options only collect them.
type customizationRequest func(d *appsv1.Deployment) error

func containerImage(name string, image string) customizationRequest {
	return func(d *appsv1.Deployment) error {
		idx, ok := k8s.FindContainer(d.Spec.Template.Spec.Containers, name)
		if !ok {
			return fmt.Errorf("container %q not found in Deployment %s", name, d.Name)
		}

		d.Spec.Template.Spec.Containers[idx].Image = image

		return nil
	}
}

func initContainerImage(name string, image string) customizationRequest {
	return func(d *appsv1.Deployment) error {
		idx, ok := k8s.FindContainer(d.Spec.Template.Spec.InitContainers, name)
		if !ok {
			return fmt.Errorf("init container %q not found in Deployment %s", name, d.Name)
		}

		d.Spec.Template.Spec.InitContainers[idx].Image = image

		return nil
	}
}

func containerEnv(name string, envs []corev1.EnvVar) customizationRequest {
	return func(d *appsv1.Deployment) error {
		idx, ok := k8s.FindContainer(d.Spec.Template.Spec.Containers, name)
		if !ok {
			return fmt.Errorf("container %q not found in Deployment %s", name, d.Name)
		}

		mergeContainerEnv(&d.Spec.Template.Spec.Containers[idx], envs)

		return nil
	}
}

func initContainerEnv(name string, envs []corev1.EnvVar) customizationRequest {
	return func(d *appsv1.Deployment) error {
		idx, ok := k8s.FindContainer(d.Spec.Template.Spec.InitContainers, name)
		if !ok {
			return fmt.Errorf("init container %q not found in Deployment %s", name, d.Name)
		}

		mergeContainerEnv(&d.Spec.Template.Spec.InitContainers[idx], envs)

		return nil
	}
}

func mergeContainerEnv(container *corev1.Container, envs []corev1.EnvVar) {
	for _, env := range envs {
		container.Env = k8s.MergeEnv(container.Env, *env.DeepCopy())
	}
}
