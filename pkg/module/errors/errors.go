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

// Package errors carries the module definition error taxonomy shared
// by the loader, the linter, and the command layer. It is generic
// module support code: the API package stays free of file-loading
// concerns.
package errors

import (
	stderrors "errors"
	"fmt"
)

// ErrInvalidDefinition identifies configuration-level module definition
// failures: unreadable files, malformed documents, and structural
// violations. Errors returned by the loader and its callers wrap it,
// so callers classify them with errors.Is instead of matching message
// text.
var ErrInvalidDefinition = stderrors.New("invalid module definition")

// NewDefinitionError wraps err as a definition configuration failure.
// The cause stays reachable through errors.Is and errors.As, so
// cancellation and other sentinel identities survive wrapping.
func NewDefinitionError(err error) error {
	return fmt.Errorf("%w: %w", ErrInvalidDefinition, err)
}

// NewDefinitionErrorf is NewDefinitionError with fmt.Errorf-style
// formatting.
func NewDefinitionErrorf(format string, args ...any) error {
	return NewDefinitionError(fmt.Errorf(format, args...))
}
