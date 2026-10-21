// Package loader reads module definitions from explicit local files
// into config documents. It is a generic module service: it performs no
// environment lookup, rendering, linting, or cluster access.
package loader

import (
	"context"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// Loader loads module definitions from the local filesystem.
type Loader struct{}

// New returns a loader over the local filesystem.
func New() *Loader {
	return &Loader{}
}

// Load reads, strictly decodes, and validates the module definition at
// path, resolves its manifest backend entry against the descriptor
// directory, and returns the resulting document.
//
// Every failure except cancellation is a configuration error matching
// errors.Is(err, config.ErrInvalidDefinition). Cancellation and
// deadline errors are returned unwrapped so they keep their identity
// and precedence over configuration classification.
func (l *Loader) Load(ctx context.Context, path string) (config.Document, error) {
	if err := ctx.Err(); err != nil {
		return config.Document{}, err
	}

	file, data, err := readDefinitionFile(path)
	if err != nil {
		return config.Document{}, err
	}

	return buildDocument(file, data)
}
