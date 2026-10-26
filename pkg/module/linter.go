// Package module composes the reusable module services into the
// public lint action. The services stay independently callable: this
// facade only wires them, it does not embed or rename them.
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

// NewLinter returns the Cobra-free module lint action wired with the
// concrete reusable loader, the centralized rendering service, and
// the operator-parity checks. Callers pass a plain linter.Options
// struct and a context; the action itself performs no flag parsing,
// printing, or environment resolution.
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
