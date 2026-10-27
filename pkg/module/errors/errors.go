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

// Package errors carries the module domain error taxonomy shared by
// the module services and the command layer: the central category
// sentinels below, and ModuleError, the one wrapper that pairs a
// category with an explicitly composed public message and an optional
// raw cause. It is generic module support code: the API package stays
// free of file-loading concerns, and this package depends only on the
// standard library and the shared generic option adapter.
package errors

import (
	stderrors "errors"

	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// ErrInvalidDefinition identifies configuration-level module definition
// failures: unreadable files, malformed documents, and structural
// violations. Errors returned by the loader and its callers wrap it,
// so callers classify them with errors.Is instead of matching message
// text.
var ErrInvalidDefinition = stderrors.New(
	"invalid module definition",
)

// ErrInvalidLintOptions identifies invalid per-call lint action
// options, distinct from definition, source, and render failures.
var ErrInvalidLintOptions = stderrors.New(
	"invalid lint options",
)

// ErrInvalidDeployOptions identifies invalid module deploy invocation
// options, distinct from the linter's option category.
var ErrInvalidDeployOptions = stderrors.New(
	"invalid deploy options",
)

// ErrStageSequence identifies an invalid stage call on a deploy:
// out-of-order, repeated, or issued after a failed stage.
var ErrStageSequence = stderrors.New(
	"invalid deployer stage sequence",
)

// ErrWaitTargetMissing identifies a requested wait whose rendered
// payload contains no controller Deployment matching the configured
// target.
var ErrWaitTargetMissing = stderrors.New(
	"no rendered controller deployment matched the wait target",
)

// ErrReservedResource identifies rendered objects a module deploy must
// never deploy.
var ErrReservedResource = stderrors.New(
	"reserved rendered resource",
)

// ErrImageSource identifies an unreadable or malformed related images
// file. Messages in this category name the file, never its contents.
var ErrImageSource = stderrors.New(
	"invalid related images file",
)

// ErrOwnerPreparation identifies a failure preparing the module's
// persisted owner: the definition custom resource definition or the
// definition itself.
var ErrOwnerPreparation = stderrors.New(
	"module owner preparation failed",
)

// ErrDeployPrepare identifies a failure in the checks made before the
// deploy action runs.
var ErrDeployPrepare = stderrors.New(
	"preparing the module deployment failed",
)

// ErrDeployApply identifies a failure of the deploy action itself.
var ErrDeployApply = stderrors.New(
	"applying the module resources failed",
)

// ErrRolloutWait identifies a failure waiting for the module
// controller Deployment rollout.
var ErrRolloutWait = stderrors.New(
	"waiting for module controller rollout failed",
)

// ModuleError is the shared wrapper for module domain failures: one
// central category sentinel, one explicitly composed public message,
// and one optional raw underlying cause.
//
// Classification uses errors.Is: the wrapper's own Is method matches
// only its category, while the standard errors.Is also walks the
// cause chain, so nested domain errors keep every category they
// carry. Access to the wrapper itself uses errors.As to
// *ModuleError, which finds the outermost wrapper in a chain.
//
// The message is exactly what the caller supplied to New: the wrapper
// never formats, prefixes, or appends the category or the cause, so
// the caller keeps full responsibility for what its diagnostics
// expose — never dotenv contents, server bodies, or other unsafe
// detail. Unwrap returns the raw cause unchanged (nil when none was
// set), keeping cancellation, status, and sentinel identities
// reachable.
type ModuleError struct {
	category error
	message  string
	cause    error
}

// ModuleErrorOption customizes one ModuleError: options target the
// error directly and apply in order inside New; a nil option is
// ignored.
type ModuleErrorOption = option.Option[ModuleError]

// WithCause sets the raw underlying cause.
func WithCause(cause error) ModuleErrorOption {
	return option.FunctionalOption[ModuleError](
		func(err *ModuleError) {
			err.cause = cause
		})
}

// New returns a ModuleError classified by category, with message as
// its exact public text. Options carry the optional parts — the raw
// underlying cause, through WithCause; an error without one simply
// omits the option. category must be one of this package's sentinels.
func New(category error, message string, opts ...ModuleErrorOption) *ModuleError {
	err := &ModuleError{
		category: category,
		message:  message,
	}

	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(err)
		}
	}

	return err
}

// Error returns the message supplied to New, exactly.
func (e *ModuleError) Error() string {
	return e.message
}

// Unwrap returns the raw cause, or nil when none was set.
func (e *ModuleError) Unwrap() error {
	return e.cause
}

// Is reports whether target is this error's own category sentinel.
// The check is a shallow identity comparison: it never compares
// messages, never equates separately constructed ModuleError
// instances, and never recurses into causes. Use errors.Is for
// full-chain classification.
func (e *ModuleError) Is(target error) bool {
	return target == e.category
}
