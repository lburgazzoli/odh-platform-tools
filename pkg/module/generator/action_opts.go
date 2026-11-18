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

// Package generator infers module definitions from local Helm or
// Kustomize sources with strict discovery and create-only YAML
// publication.
package generator

import (
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// Options are the plain per-call inputs of one generate invocation.
// They carry no Cobra command and no environment source: scalar
// emptiness and option-occurrence rules are enforced at the command
// boundary, so the service only rejects structurally missing inputs.
type Options struct {
	// SourceDir is the module source root to inspect. It is required
	// and must contain exactly one backend marker.
	SourceDir string

	// Name overrides the inferred module name. Empty infers the
	// lowercase CRD kind.
	Name string

	// InitContainer selects the controller init container. It is
	// mandatory whenever the rendered Deployment has init containers.
	InitContainer string

	// RelatedImagesFile optionally names a related images file whose
	// names are copied into the generated definition.
	RelatedImagesFile string

	// ValuesFile optionally names a Helm values file. Helm sources
	// only.
	ValuesFile string

	// Output is the destination: empty or "stdout" writes to stdout,
	// any other value is a file path.
	Output string

	// Values holds ordered Helm --set expressions. Helm sources only.
	Values []string
}

// validate reports whether the options are usable. A missing source
// directory is an invalid-options failure classified by
// moduleerrors.ErrInvalidGenerateOptions.
func (o Options) validate() error {
	if o.SourceDir == "" {
		return moduleerrors.New(moduleerrors.ErrInvalidGenerateOptions,
			"invalid generate options: SourceDir is required")
	}

	return nil
}
