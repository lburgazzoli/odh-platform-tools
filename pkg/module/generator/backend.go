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
	"os"
	"path/filepath"
	"strings"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// chartMarker is the Helm backend marker at a module source root.
const chartMarker = "Chart.yaml"

// kustomizeMarkers lists the Kustomize backend markers at a module
// source root, in the order Kustomize itself recognizes them.
var kustomizeMarkers = []string{
	"kustomization.yaml",
	"kustomization.yml",
	"Kustomization",
}

// resolveSourceDir returns the absolute module source directory. A
// missing or nondirectory source is a definition failure.
func resolveSourceDir(source string) (string, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return "", moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"resolve module source "+source,
			moduleerrors.WithCause(err),
		)
	}

	info, err := os.Stat(absolute)
	if err != nil {
		return "", moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"module source "+source+" is not readable",
			moduleerrors.WithCause(err),
		)
	}

	if !info.IsDir() {
		return "", moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"module source "+source+" is not a directory",
		)
	}

	return absolute, nil
}

// detectBackend returns the manifest backend marked at the source
// root. Exactly one marker family must be present, carrying at most
// one Kustomize marker; there is no recursive discovery.
func detectBackend(sourceDir string) (toolApi.Format, error) {
	chart := markerPresent(sourceDir, chartMarker)

	kustomizations := make([]string, 0, len(kustomizeMarkers))
	for _, marker := range kustomizeMarkers {
		if markerPresent(sourceDir, marker) {
			kustomizations = append(kustomizations, marker)
		}
	}

	switch {
	case chart && len(kustomizations) > 0:
		return "", moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"module source "+sourceDir+" declares both Helm and Kustomize markers",
		)
	case chart:
		return toolApi.FormatHelm, nil
	case len(kustomizations) > 1:
		return "", moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"module source "+sourceDir+" declares duplicate Kustomize markers: "+
				strings.Join(kustomizations, ", "),
		)
	case len(kustomizations) == 1:
		return toolApi.FormatKustomize, nil
	default:
		return "", moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			"module source "+sourceDir+" has no Helm chart or Kustomization at its root",
		)
	}
}

// markerPresent reports a regular marker file at the source root.
func markerPresent(sourceDir string, marker string) bool {
	info, err := os.Stat(filepath.Join(sourceDir, marker))

	return err == nil && info.Mode().IsRegular()
}
