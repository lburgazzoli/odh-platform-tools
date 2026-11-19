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
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// bootstrapReleaseName is the temporary release name used for the
// bootstrap render when no explicit name is supplied.
const bootstrapReleaseName = "module-discovery"

// maxReleaseNameLength is the Helm release-name length limit shared
// by Helm and the pinned renderer.
const maxReleaseNameLength = 53

// releaseNamePattern is the release-name format shared by Helm and
// the pinned renderer.
var releaseNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// inferredName derives the module name from the CRD kind by
// lowercasing alone: no pluralization, kebab-casing, truncation, or
// sanitization.
func inferredName(kind string) string {
	return strings.ToLower(kind)
}

// validateExplicitName enforces Kubernetes naming plus, for Helm
// sources, the release-name constraints of Helm and the pinned
// renderer, before any render runs.
func validateExplicitName(name string, format toolApi.Format) error {
	return validateModuleName(name, format, moduleerrors.ErrInvalidGenerateOptions)
}

// validateInferredName enforces the same constraints for a name
// inferred from the CRD kind, requesting an explicit name on failure.
func validateInferredName(name string, format toolApi.Format) error {
	if err := validateModuleName(name, format, moduleerrors.ErrInvalidDefinition); err != nil {
		return moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			err.Error()+"; pass an explicit name instead",
			moduleerrors.WithCause(err),
		)
	}

	return nil
}

// validateInstanceName enforces Kubernetes naming for the module
// instance name. Unlike the module name it never names a Helm
// release, so the release-name constraints do not apply.
func validateInstanceName(name string) error {
	if len(validation.IsDNS1123Subdomain(name)) != 0 {
		return moduleerrors.New(
			moduleerrors.ErrInvalidGenerateOptions,
			fmt.Sprintf("instance name %q is not a valid Kubernetes name", name),
		)
	}

	return nil
}

// validateModuleName checks Kubernetes naming for every backend and
// Helm release-name usability for Helm sources.
func validateModuleName(
	name string,
	format toolApi.Format,
	category error,
) error {
	if len(validation.IsDNS1123Subdomain(name)) != 0 {
		return moduleerrors.New(
			category,
			fmt.Sprintf("module name %q is not a valid Kubernetes name", name),
		)
	}

	if format == toolApi.FormatHelm &&
		(len(name) > maxReleaseNameLength || !releaseNamePattern.MatchString(name)) {
		return moduleerrors.New(
			category,
			fmt.Sprintf("module name %q is not usable as a Helm release name", name),
		)
	}

	return nil
}
