package module

import (
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/generator"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/crd"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/deployments"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/types"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// NewGenerator supplies local rendering and the default manifest
// checks for module definition generation.
func NewGenerator() *generator.Action {
	return generator.NewAction(
		renderer.Render,
		checks.And(
			types.Validate,
			deployments.Validate,
			crd.Validate,
		),
	)
}
