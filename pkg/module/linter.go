// Package module wires the reusable module services.
package module

import (
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/crd"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/deployments"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/types"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// NewLinter supplies local loading, rendering, and the default manifest checks.
func NewLinter() *linter.Action {
	return linter.NewAction(
		linter.DefinitionLoad(loader.LoadFromPath),
		linter.ManifestRender(renderer.Render),
		checks.And(
			types.Validate,
			deployments.Validate,
			crd.Validate,
		),
	)
}
