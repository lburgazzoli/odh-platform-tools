package linter

import (
	"errors"
	"fmt"
)

// ErrInvalidOptions identifies invalid per-call lint action options,
// separate from definition, source, and render error categories.
var ErrInvalidOptions = errors.New("invalid lint options")

// Options are the plain per-call inputs of one lint invocation. They
// carry no Cobra command, no output-format choice, and no environment
// source: the action only consumes the explicit definition file.
type Options struct {
	// DefinitionFile is the module definition file to lint. It is
	// required and must name an explicit file; there is no default
	// discovery or fallback.
	DefinitionFile string
}

// validate reports whether the options are usable.
func (o Options) validate() error {
	if o.DefinitionFile == "" {
		return fmt.Errorf("%w: DefinitionFile is required", ErrInvalidOptions)
	}

	return nil
}
