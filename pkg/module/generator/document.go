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
	jsonv2 "encoding/json/v2"
	"fmt"
	"path/filepath"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sigsyaml "sigs.k8s.io/yaml"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// syntheticDefinitionName names the definition file the render
// boundary resolves source paths against. It never needs to exist:
// only its directory, the module source root, is read.
const syntheticDefinitionName = "platform-module-definition.yaml"

// renderDefinition builds the definition passed to the render
// boundary for one generation pass. It carries the release name and,
// from the final pass on, the discovered module type.
func renderDefinition(
	name string,
	moduleType toolApi.ModuleType,
	format toolApi.Format,
	values map[string]apiextensionsv1.JSON,
) toolApi.PlatformModuleDefinition {
	definition := toolApi.PlatformModuleDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: toolApi.APIVersion, Kind: toolApi.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: toolApi.PlatformModuleDefinitionSpec{
			Instance:  toolApi.Instance{Type: moduleType},
			Manifests: toolApi.Manifests{Format: format},
		},
	}

	if format == toolApi.FormatHelm && len(values) > 0 {
		definition.Spec.Manifests.Helm = &toolApi.HelmManifests{Values: values}
	}

	return definition
}

// effectiveDefinition builds the generated definition with
// loader-equivalent defaults applied, for reserved-resource checks
// and the injected lint composition.
func effectiveDefinition(
	name string,
	moduleType toolApi.ModuleType,
	format toolApi.Format,
	manifestsPath string,
	values map[string]apiextensionsv1.JSON,
	relatedImages []string,
	selection controllerSelection,
) toolApi.PlatformModuleDefinition {
	definition := renderDefinition(name, moduleType, format, values)

	definition.Spec.Instance.Name = name

	switch format {
	case toolApi.FormatHelm:
		definition.Spec.Manifests.Helm = &toolApi.HelmManifests{
			Path:   manifestsPath,
			Values: values,
		}
	case toolApi.FormatKustomize:
		definition.Spec.Manifests.Kustomize = &toolApi.KustomizeManifests{
			Path: manifestsPath,
		}
	}

	if len(relatedImages) > 0 {
		definition.Spec.RelatedImages = relatedImages
	}

	deployment := toolApi.ControllerDeployment{
		Container: &toolApi.ContainerDefinition{
			Name:  selection.containerName,
			Image: selection.containerImage,
		},
	}

	// Mirror the minimal document: a deployment matching the module
	// name is omitted and resolved through the computed fallback.
	if selection.deploymentName != name {
		deployment.Name = selection.deploymentName
	}

	if selection.initName != "" {
		init := toolApi.ContainerTarget{Name: selection.initName}

		if selection.initImage != selection.containerImage {
			init.Image = selection.initImage
		}

		deployment.InitContainer = &init
	}

	definition.Spec.Controller.Deployment = deployment

	return definition
}

// minimalDocument builds the wire document: exactly the fields the
// loader cannot default — identity, manifests, applicable values and
// related images, and nondefault controller selections.
func minimalDocument(
	name string,
	moduleType toolApi.ModuleType,
	format toolApi.Format,
	manifestsPath string,
	values map[string]apiextensionsv1.JSON,
	relatedImages []string,
	selection controllerSelection,
) map[string]any {
	manifests := map[string]any{
		"format": string(format),
	}

	switch format {
	case toolApi.FormatHelm:
		helm := map[string]any{}
		if manifestsPath != "" {
			helm["path"] = manifestsPath
		}
		if len(values) > 0 {
			helm["values"] = values
		}
		if len(helm) > 0 {
			manifests["helm"] = helm
		}
	case toolApi.FormatKustomize:
		if manifestsPath != "" {
			manifests["kustomize"] = map[string]any{"path": manifestsPath}
		}
	}

	deployment := map[string]any{}
	if selection.deploymentName != name {
		deployment["name"] = selection.deploymentName
	}

	container := map[string]any{"image": selection.containerImage}
	if selection.containerName != toolApi.DefaultMainContainerName {
		container["name"] = selection.containerName
	}
	deployment["container"] = container

	if selection.initName != "" {
		init := map[string]any{"name": selection.initName}
		if selection.initImage != selection.containerImage {
			init["image"] = selection.initImage
		}
		deployment["initContainer"] = init
	}

	spec := map[string]any{
		"instance": map[string]any{
			"type": map[string]any{
				"group":   moduleType.Group,
				"version": moduleType.Version,
				"kind":    moduleType.Kind,
			},
		},
		"manifests": manifests,
		"controller": map[string]any{
			"deployment": deployment,
		},
	}

	if len(relatedImages) > 0 {
		spec["relatedImages"] = relatedImages
	}

	return map[string]any{
		"apiVersion": toolApi.APIVersion,
		"kind":       toolApi.Kind,
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}
}

// encodeDocument serializes the minimal document deterministically:
// values and related-image ordering are stable, and 64-bit integers
// survive exactly through the YAML number types.
func encodeDocument(document map[string]any) ([]byte, error) {
	encoded, err := jsonv2.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode generated definition: %w", err)
	}

	serialized, err := sigsyaml.JSONToYAML(encoded)
	if err != nil {
		return nil, fmt.Errorf("serialize generated definition: %w", err)
	}

	return serialized, nil
}

// manifestPath computes the authored manifests path relative to the
// definition directory, omitting it when source and destination
// coincide. Absolute fallback keeps absolute sources usable.
func manifestPath(sourceDir string, definitionDir string) string {
	if sourceDir == definitionDir {
		return ""
	}

	relative, err := filepath.Rel(definitionDir, sourceDir)
	if err != nil {
		return sourceDir
	}

	return relative
}
