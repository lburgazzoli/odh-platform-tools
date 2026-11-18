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

package generator

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// crdVersionSpec describes one CRD version entry.
type crdVersionSpec struct {
	name    string
	served  bool
	storage bool
}

// containerSpec describes one pod container entry.
type containerSpec struct {
	name  string
	image string
}

// moduleCRD builds a CustomResourceDefinition as an unstructured
// object with the given identity and versions.
func moduleCRD(
	t *testing.T,
	group string,
	kind string,
	versions ...crdVersionSpec,
) unstructured.Unstructured {
	t.Helper()

	crdVersions := make([]apiextensionsv1.CustomResourceDefinitionVersion, 0, len(versions))
	for _, version := range versions {
		crdVersions = append(crdVersions, apiextensionsv1.CustomResourceDefinitionVersion{
			Name:    version.name,
			Served:  version.served,
			Storage: version.storage,
		})
	}

	plural := strings.ToLower(kind) + "s"

	crd := apiextensionsv1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apiextensions.k8s.io/v1",
			Kind:       "CustomResourceDefinition",
		},
		ObjectMeta: metav1.ObjectMeta{Name: plural + "." + group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:   kind,
				Plural: plural,
			},
			Versions: crdVersions,
		},
	}

	return unstructuredFrom(t, &crd)
}

// betaCRD builds a CustomResourceDefinition carrying an unsupported
// apiVersion.
func betaCRD(t *testing.T) unstructured.Unstructured {
	t.Helper()

	crd := moduleCRD(t, "example.com", "FooBar", crdVersionSpec{name: "v1alpha1", served: true, storage: true})
	crd.Object["apiVersion"] = "apiextensions.k8s.io/v1beta1"

	return crd
}

// controllerDeployment builds an apps/v1 Deployment as an
// unstructured object with the given containers.
func controllerDeployment(
	t *testing.T,
	name string,
	containers []containerSpec,
	inits []containerSpec,
) unstructured.Unstructured {
	t.Helper()

	podContainers := make([]corev1.Container, 0, len(containers))
	for _, container := range containers {
		podContainers = append(podContainers, corev1.Container{
			Name:  container.name,
			Image: container.image,
		})
	}

	podInits := make([]corev1.Container, 0, len(inits))
	for _, init := range inits {
		podInits = append(podInits, corev1.Container{
			Name:  init.name,
			Image: init.image,
		})
	}

	deployment := appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers:     podContainers,
					InitContainers: podInits,
				},
			},
		},
	}

	return unstructuredFrom(t, &deployment)
}

// typedDeployment decodes an unstructured Deployment for controller
// discovery.
func typedDeployment(t *testing.T, object unstructured.Unstructured) *appsv1.Deployment {
	t.Helper()

	var deployment appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &deployment); err != nil {
		t.Fatalf("decode deployment: %v", err)
	}

	return &deployment
}

// moduleInstance builds an instance of the module's own custom
// resource, which generation must never emit.
func moduleInstance(moduleType toolApi.ModuleType, name string) unstructured.Unstructured {
	return unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": moduleType.Group + "/" + moduleType.Version,
			"kind":       moduleType.Kind,
			"metadata":   map[string]any{"name": name},
		},
	}
}

// validModuleResources builds a minimal renderable module bundle: one
// CRD and one Deployment with a unique manager container.
func validModuleResources(t *testing.T) []unstructured.Unstructured {
	t.Helper()

	return []unstructured.Unstructured{
		moduleCRD(t, "example.com", "FooBar",
			crdVersionSpec{name: "v1alpha1", served: true, storage: true},
			crdVersionSpec{name: "v1beta1", served: true, storage: false}),
		controllerDeployment(t, "foo-controller", []containerSpec{
			{name: "manager", image: "quay.io/example/controller:v1"},
			{name: "sidecar", image: "quay.io/example/sidecar:v1"},
		}, nil),
	}
}

// fooBarType is the module type the valid fixtures infer.
func fooBarType() toolApi.ModuleType {
	return toolApi.ModuleType{Group: "example.com", Version: "v1alpha1", Kind: "FooBar"}
}

// unstructuredFrom converts a typed object, failing the test on
// conversion errors.
func unstructuredFrom(t *testing.T, object any) unstructured.Unstructured {
	t.Helper()

	converted, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		t.Fatalf("convert typed fixture: %v", err)
	}

	return unstructured.Unstructured{Object: converted}
}

// toUnstructureds bundles fixture objects into a render bundle.
func toUnstructureds(t *testing.T, items ...any) []unstructured.Unstructured {
	t.Helper()

	resources := make([]unstructured.Unstructured, 0, len(items))
	for _, item := range items {
		resource, ok := item.(unstructured.Unstructured)
		if !ok {
			t.Fatalf("unsupported fixture type %T", item)
		}

		resources = append(resources, resource)
	}

	return resources
}
