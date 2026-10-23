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

// Package crd embeds the generated module definition CRD manifests,
// so installing the owner works from the built binary without a
// source checkout or working-directory dependency.
package crd

import _ "embed"

// PlatformModuleDefinitionYAML is the generated structural schema of
// the config.opendatahub.io PlatformModuleDefinition CRD.
//
//go:embed bases/config.opendatahub.io_platformmoduledefinitions.yaml
var PlatformModuleDefinitionYAML []byte
