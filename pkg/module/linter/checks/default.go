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

package checks

import (
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/crd"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/deployments"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/types"
)

// Default composes the default manifest checks applied by the lint
// and generate commands: resource typing, controller Deployment
// shape, and custom resource definition establishment.
func Default() linter.Validation {
	return And(
		types.Validate,
		deployments.Validate,
		crd.Validate,
	)
}
