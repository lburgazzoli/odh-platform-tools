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

// Package images resolves requested image sources from YAML and the environment.
package images

import (
	"os"
	"regexp"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/config"
)

// Load resolves requested names case-sensitively. Environment values override the
// optional YAML file, and explicitly empty environment values mask file values.
// The result contains only nonempty values.
func Load(path string, keys []string) (map[string]string, error) {
	var file api.RelatedImagesConfig
	if path != "" {
		if err := config.LoadFromPath(path, &file); err != nil {
			return nil, moduleerrors.New(
				moduleerrors.ErrImageSource,
				"invalid related images file "+path,
				moduleerrors.WithCause(err),
			)
		}
	}

	fileValues := make(map[string]string, len(file.RelatedImages))
	for _, entry := range file.RelatedImages {
		fileValues[entry.Name] = entry.Value
	}

	values := make(map[string]string, len(keys))
	for _, key := range keys {
		value, present := os.LookupEnv(key)
		if !present {
			value = fileValues[key]
		}

		if value != "" {
			values[key] = value
		}
	}

	return values, nil
}

// Snapshot resolves related images and tokens in both controller image fields.
// Only RelatedImages controls which values are injected as environment variables.
func Snapshot(path string, definition toolApi.PlatformModuleDefinition) (map[string]string, error) {
	keys := make([]string, 0, len(definition.Spec.RelatedImages)+2)
	keys = append(keys, definition.Spec.RelatedImages...)

	if container := definition.Spec.Controller.Deployment.Container; container != nil {
		if key, ok := ImageSourceKey(container.Image); ok {
			keys = append(keys, key)
		}
	}
	if init := definition.Spec.Controller.Deployment.InitContainer; init != nil {
		if key, ok := ImageSourceKey(init.Image); ok {
			keys = append(keys, key)
		}
	}

	return Load(path, keys)
}

var imageTokenPattern = regexp.MustCompile(`^\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}$`)

// ImageSourceKey extracts a portable variable name from a whole {{NAME}} token.
func ImageSourceKey(image string) (string, bool) {
	match := imageTokenPattern.FindStringSubmatch(image)
	if match == nil {
		return "", false
	}

	return match[1], true
}

// ResolveImage expands a whole {{NAME}} token once; other values stay literal.
// Missing or empty sources return an empty string.
func ResolveImage(image string, values map[string]string) string {
	key, ok := ImageSourceKey(image)
	if !ok {
		return image
	}

	return values[key]
}
