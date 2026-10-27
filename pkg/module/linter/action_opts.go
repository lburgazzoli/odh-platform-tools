package linter

import (
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// Options are the plain per-call inputs of one lint invocation. They
// carry no Cobra command, no output-format choice, and no environment
// source: the action only consumes the explicit definition file.
type Options struct {
	// DefinitionFile is the module definition file to lint. It is
	// required and must name an explicit file; there is no default
	// discovery or fallback.
	DefinitionFile string
}

// validate reports whether the options are usable: a missing
// definition file is an invalid-options failure classified by
// moduleerrors.ErrInvalidLintOptions.
func (o Options) validate() error {
	if o.DefinitionFile == "" {
		return moduleerrors.New(moduleerrors.ErrInvalidLintOptions,
			"invalid lint options: DefinitionFile is required")
	}

	return nil
}
