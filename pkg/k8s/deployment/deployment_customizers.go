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

// customizationRequest is one ordered Deployment mutation, deferred
// from option collection to Customize's apply pass. It is deliberately
// not another option.Option: ApplyTo mutates a configuration struct
// and cannot report failure, while a request mutates the Deployment
// itself and returns the first error.
type customizationRequest func(d *appsv1.Deployment) error

// containerImage returns the request assigning a literal image to the
// first regular container with the exact name.
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

// initContainerImage returns the request assigning a literal image to
// the first init container with the exact name.
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

// containerEnv returns the request merging env vars into the first
// regular container with the exact name; the container must exist even
// for an empty env list.
func containerEnv(name string, envs []corev1.EnvVar) customizationRequest {
	return func(d *appsv1.Deployment) error {
		idx, ok := k8s.FindContainer(d.Spec.Template.Spec.Containers, name)
		if !ok {
			return fmt.Errorf("container %q not found in Deployment %s", name, d.Name)
		}

		container := &d.Spec.Template.Spec.Containers[idx]

		for _, env := range envs {
			container.Env = k8s.MergeEnv(container.Env, *env.DeepCopy())
		}

		return nil
	}
}
