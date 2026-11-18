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
	"fmt"
	"regexp"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	gvk "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// crdKindPattern is the kind shape Kubernetes requires of a custom
// resource: alphanumeric, starting with an uppercase letter.
var crdKindPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

// moduleDiscovery is the module identity inferred from a rendered
// bundle.
type moduleDiscovery struct {
	moduleType toolApi.ModuleType
}

// discoverModule infers the module identity from a bootstrap render:
// exactly one CRD and one Deployment with the supported GVKs, a
// usable CRD group and kind, and exactly one served storage version.
func discoverModule(resources []unstructured.Unstructured) (moduleDiscovery, error) {
	crd, err := singleResource(resources, gvk.CustomResourceDefinition)
	if err != nil {
		return moduleDiscovery{}, err
	}

	if _, err := singleResource(resources, gvk.Deployment); err != nil {
		return moduleDiscovery{}, err
	}

	moduleType, err := crdModuleType(crd)
	if err != nil {
		return moduleDiscovery{}, err
	}

	return moduleDiscovery{moduleType: moduleType}, nil
}

// confirmModule rechecks the bundle after the final render and
// decodes its single Deployment for controller discovery. Cardinality
// or CRD identity drift from the bootstrap discovery is a definition
// failure; the deployment itself may legitimately change shape between
// renders, so only its cardinality is rechecked here.
func confirmModule(
	resources []unstructured.Unstructured,
	discovery moduleDiscovery,
) (*appsv1.Deployment, error) {
	crd, err := singleResource(resources, gvk.CustomResourceDefinition)
	if err != nil {
		return nil, err
	}

	moduleType, err := crdModuleType(crd)
	if err != nil {
		return nil, err
	}

	if moduleType != discovery.moduleType {
		return nil, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			fmt.Sprintf("rendered CustomResourceDefinition identity changed from %s to %s between renders",
				discovery.moduleType.String(), moduleType.String()),
		)
	}

	object, err := singleResource(resources, gvk.Deployment)
	if err != nil {
		return nil, err
	}

	var deployment appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &deployment); err != nil {
		return nil, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"decode rendered Deployment "+object.GetName(),
			moduleerrors.WithCause(err),
		)
	}

	return &deployment, nil
}

// singleResource returns the bundle's only resource of the target
// kind, requiring the target's exact version.
func singleResource(
	resources []unstructured.Unstructured,
	target schema.GroupVersionKind,
) (*unstructured.Unstructured, error) {
	var matches []*unstructured.Unstructured

	for i := range resources {
		if resources[i].GroupVersionKind().GroupKind() == target.GroupKind() {
			matches = append(matches, &resources[i])
		}
	}

	if len(matches) != 1 {
		return nil, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			fmt.Sprintf("module source must render exactly one %s, found %d", target.Kind, len(matches)),
		)
	}

	found := matches[0].GroupVersionKind()
	if found != target {
		return nil, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			fmt.Sprintf("module source must render %s as %s, found %s",
				target.Kind, target.GroupVersion(), found.GroupVersion()),
		)
	}

	return matches[0], nil
}

// crdModuleType infers the module GVK from the rendered CRD: a usable
// group and kind, and exactly one storage version, which must be
// served.
func crdModuleType(crd *unstructured.Unstructured) (toolApi.ModuleType, error) {
	var decoded apiextensionsv1.CustomResourceDefinition
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(crd.Object, &decoded); err != nil {
		return toolApi.ModuleType{}, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"decode rendered CustomResourceDefinition "+crd.GetName(),
			moduleerrors.WithCause(err),
		)
	}

	group := decoded.Spec.Group
	if len(validation.IsDNS1123Subdomain(group)) != 0 {
		return toolApi.ModuleType{}, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"rendered CustomResourceDefinition "+decoded.Name+" declares an unusable group",
		)
	}

	kind := decoded.Spec.Names.Kind
	if !crdKindPattern.MatchString(kind) {
		return toolApi.ModuleType{}, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"rendered CustomResourceDefinition "+decoded.Name+" declares an unusable kind",
		)
	}

	storage := make([]string, 0, 1)
	for _, version := range decoded.Spec.Versions {
		if version.Storage {
			storage = append(storage, version.Name)
		}
	}

	if len(storage) != 1 {
		return toolApi.ModuleType{}, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			fmt.Sprintf("rendered CustomResourceDefinition %s must declare exactly one storage version, found %d",
				decoded.Name, len(storage)),
		)
	}

	version := storage[0]
	served := slices.ContainsFunc(decoded.Spec.Versions, func(candidate apiextensionsv1.CustomResourceDefinitionVersion) bool {
		return candidate.Name == version && candidate.Served
	})

	if !served {
		return toolApi.ModuleType{}, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			fmt.Sprintf("storage version %s of rendered CustomResourceDefinition %s is not served",
				version, decoded.Name),
		)
	}

	return toolApi.ModuleType{Group: group, Version: version, Kind: kind}, nil
}

// controllerSelection is the controller identity inferred from the
// final rendered Deployment.
type controllerSelection struct {
	deploymentName string
	containerName  string
	containerImage string
	initName       string
	initImage      string
}

// discoverController selects the main and init containers of the
// final rendered Deployment. The main container prefers a unique
// manager, otherwise the only regular container; an explicit init
// selection is mandatory whenever init containers exist.
func discoverController(
	deployment *appsv1.Deployment,
	initSelection string,
) (controllerSelection, error) {
	selection := controllerSelection{deploymentName: deployment.Name}

	podSpec := deployment.Spec.Template.Spec

	main, err := selectMainContainer(deployment, podSpec.Containers)
	if err != nil {
		return selection, err
	}

	selection.containerName = main.Name
	selection.containerImage = main.Image

	if main.Image == "" {
		return selection, imageFailure(deployment, "container", main.Name)
	}

	inits := podSpec.InitContainers

	switch {
	case len(inits) == 0 && initSelection == "":
		return selection, nil
	case len(inits) == 0:
		return selection, moduleerrors.New(
			moduleerrors.ErrInvalidGenerateOptions,
			fmt.Sprintf("controller deployment %s has no init containers, but init container %q was requested",
				deployment.Name, initSelection),
		)
	case initSelection == "":
		return selection, moduleerrors.New(
			moduleerrors.ErrInvalidGenerateOptions,
			fmt.Sprintf("controller deployment %s has %d init containers; an explicit init container selection is required",
				deployment.Name, len(inits)),
		)
	}

	init, err := selectInitContainer(deployment, inits, initSelection)
	if err != nil {
		return selection, err
	}

	selection.initName = init.Name
	selection.initImage = init.Image

	if init.Image == "" {
		return selection, imageFailure(deployment, "init container", init.Name)
	}

	return selection, nil
}

// selectMainContainer picks the main container: a unique container
// named manager when several exist, otherwise the only regular
// container.
func selectMainContainer(
	deployment *appsv1.Deployment,
	containers []corev1.Container,
) (*corev1.Container, error) {
	if err := checkContainerNames(deployment, containers, "container"); err != nil {
		return nil, err
	}

	switch len(containers) {
	case 0:
		return nil, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"controller deployment "+deployment.Name+" has no containers",
		)
	case 1:
		return &containers[0], nil
	}

	managers := make([]*corev1.Container, 0, 1)
	for i := range containers {
		if containers[i].Name == toolApi.DefaultMainContainerName {
			managers = append(managers, &containers[i])
		}
	}

	if len(managers) != 1 {
		return nil, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			fmt.Sprintf("controller deployment %s has %d containers without a unique container named %q",
				deployment.Name, len(containers), toolApi.DefaultMainContainerName),
		)
	}

	return managers[0], nil
}

// selectInitContainer picks the explicitly selected init container.
func selectInitContainer(
	deployment *appsv1.Deployment,
	inits []corev1.Container,
	initSelection string,
) (*corev1.Container, error) {
	if err := checkContainerNames(deployment, inits, "init container"); err != nil {
		return nil, err
	}

	for i := range inits {
		if inits[i].Name == initSelection {
			return &inits[i], nil
		}
	}

	return nil, moduleerrors.New(
		moduleerrors.ErrInvalidGenerateOptions,
		fmt.Sprintf("init container %q matches no init container of controller deployment %s",
			initSelection, deployment.Name),
	)
}

// checkContainerNames rejects empty and duplicate container names.
func checkContainerNames(
	deployment *appsv1.Deployment,
	containers []corev1.Container,
	role string,
) error {
	seen := make(map[string]struct{}, len(containers))

	for i := range containers {
		name := containers[i].Name

		if name == "" {
			return moduleerrors.New(
				moduleerrors.ErrInvalidDefinition,
				fmt.Sprintf("controller deployment %s has a %s without a name", deployment.Name, role),
			)
		}

		if _, exists := seen[name]; exists {
			return moduleerrors.New(
				moduleerrors.ErrInvalidDefinition,
				fmt.Sprintf("controller deployment %s has duplicate %s names %q", deployment.Name, role, name),
			)
		}

		seen[name] = struct{}{}
	}

	return nil
}

// imageFailure reports a selected container whose image is empty.
func imageFailure(
	deployment *appsv1.Deployment,
	role string,
	name string,
) error {
	return moduleerrors.New(
		moduleerrors.ErrInvalidDefinition,
		fmt.Sprintf("%s %q of controller deployment %s has an empty image", role, name, deployment.Name),
	)
}
