package errors

import (
	"context"
	"errors"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
)

func TestNewDefinitionErrorPreservesCause(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := NewDefinitionError(context.Canceled)

	g.Expect(errors.Is(err, ErrInvalidDefinition)).To(BeTrue())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
}

func TestNewDefinitionErrorfPreservesCause(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cause := errors.New("boom")
	err := NewDefinitionErrorf("read %s: %w", "definition.yaml", cause)

	g.Expect(errors.Is(err, ErrInvalidDefinition)).To(BeTrue())
	g.Expect(errors.Is(err, cause)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("definition.yaml"))
}

// allCategories lists every central category sentinel, in the order
// they are declared in the package.
var allCategories = []error{
	ErrInvalidDefinition,
	ErrInvalidLintOptions,
	ErrInvalidRunOptions,
	ErrStageSequence,
	ErrWaitTargetMissing,
	ErrReservedResource,
	ErrImageSource,
	ErrOwnerPreparation,
	ErrDeployPrepare,
	ErrDeployApply,
	ErrRolloutWait,
}

// secretMarker is a typed cause whose text must never reach a public
// message.
type secretMarker struct {
	secret string
}

func (e *secretMarker) Error() string { return e.secret }

// TestModuleErrorCategoryIdentity proves each category, wrapped over
// an unclassified cause, matches only its own sentinel.
func TestModuleErrorCategoryIdentity(t *testing.T) {
	t.Parallel()

	for _, category := range allCategories {
		t.Run(category.Error(), func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			err := New(category, "public message", WithCause(errors.New("cause")))

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, category)).To(BeTrue())
			for _, other := range allCategories {
				if other != category {
					g.Expect(errors.Is(err, other)).To(BeFalse(),
						"category %q must not match %q", category, other)
				}
			}
		})
	}
}

// TestModuleErrorRejectsEqualTextSentinels proves category matching is
// identity, not text: a separately allocated sentinel carrying the
// same string never matches.
func TestModuleErrorRejectsEqualTextSentinels(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := New(ErrStageSequence, "Run requires a fresh runner")
	impostor := errors.New(ErrStageSequence.Error())

	g.Expect(errors.Is(err, impostor)).To(BeFalse())
	g.Expect(errors.Is(impostor, ErrStageSequence)).To(BeFalse())
}

// TestSeparateModuleErrorsAreNotEquivalent proves two independently
// constructed errors are distinct even when their categories match.
func TestSeparateModuleErrorsAreNotEquivalent(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	first := New(ErrImageSource, "one")
	second := New(ErrImageSource, "two")

	g.Expect(errors.Is(first, second)).To(BeFalse())
	g.Expect(errors.Is(second, first)).To(BeFalse())
}

// TestModuleErrorMessageAndCause proves the public message is exact,
// hides the cause's text, exposes the wrapper through errors.As, and
// keeps the typed raw cause reachable with its pointer identity
// through a single Unwrap.
func TestModuleErrorMessageAndCause(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cause := &secretMarker{secret: "dotenv-secret-detail"}
	err := New(ErrImageSource, "invalid related images file images.env",
		WithCause(cause))

	g.Expect(err.Error()).To(Equal("invalid related images file images.env"))
	g.Expect(err.Error()).NotTo(ContainSubstring("dotenv-secret-detail"))

	var wrapper *ModuleError
	g.Expect(errors.As(err, &wrapper)).To(BeTrue())
	g.Expect(wrapper).To(BeIdenticalTo(err))

	unwrapped := errors.Unwrap(err)
	g.Expect(unwrapped).To(BeIdenticalTo(cause))

	var typed *secretMarker
	g.Expect(errors.As(unwrapped, &typed)).To(BeTrue())
	g.Expect(typed).To(BeIdenticalTo(cause))
}

// TestModuleErrorWithoutCauseIsStillError proves an error constructed
// with no cause option is a real error whose category still matches
// and whose Unwrap is nil.
func TestModuleErrorWithoutCauseIsStillError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := New(ErrStageSequence, "Run requires a fresh runner")

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Unwrap(err)).To(BeNil())
	g.Expect(errors.Is(err, ErrStageSequence)).To(BeTrue())
}

// TestModuleErrorOptions proves the option machinery: a nil option is
// ignored, a whole ModuleErrorOptions preset applies like WithCause,
// and WithCause(nil) leaves the error without a cause.
func TestModuleErrorOptions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cause := errors.New("boom")

	err := New(ErrImageSource, "invalid related images file images.env",
		nil,
		ModuleErrorOptions{Cause: cause})
	g.Expect(errors.Unwrap(err)).To(BeIdenticalTo(cause))

	err = New(ErrImageSource, "invalid related images file images.env",
		WithCause(nil))
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Unwrap(err)).To(BeNil())
}

// TestModuleErrorTraversal proves category and cause identities
// survive contextual wrapping, nested common wrappers, and joins:
// errors.Is walks the whole graph while the wrapper's own Is stays
// shallow, and a single Unwrap returns the exact cause.
func TestModuleErrorTraversal(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	inner := New(ErrInvalidDefinition, "read definition.yaml failed",
		WithCause(context.Canceled))
	outer := New(ErrRolloutWait, "waiting for rollout failed", WithCause(inner))
	wrapped := fmt.Errorf("run: %w", outer)
	joined := errors.Join(wrapped, context.DeadlineExceeded)

	// The wrapper's own Is is shallow: the outer category only.
	g.Expect(outer.Is(ErrRolloutWait)).To(BeTrue())
	g.Expect(outer.Is(ErrInvalidDefinition)).To(BeFalse())

	// The standard errors.Is walks the whole chain.
	g.Expect(errors.Is(outer, ErrRolloutWait)).To(BeTrue())
	g.Expect(errors.Is(outer, ErrInvalidDefinition)).To(BeTrue())
	g.Expect(errors.Is(outer, context.Canceled)).To(BeTrue())
	g.Expect(errors.Is(wrapped, ErrRolloutWait)).To(BeTrue())
	g.Expect(errors.Is(wrapped, context.Canceled)).To(BeTrue())
	g.Expect(errors.Is(joined, ErrRolloutWait)).To(BeTrue())
	g.Expect(errors.Is(joined, ErrInvalidDefinition)).To(BeTrue())
	g.Expect(errors.Is(joined, context.Canceled)).To(BeTrue())
	g.Expect(errors.Is(joined, context.DeadlineExceeded)).To(BeTrue())

	// errors.As finds the outermost wrapper; one Unwrap returns the
	// exact cause.
	var wrapper *ModuleError
	g.Expect(errors.As(outer, &wrapper)).To(BeTrue())
	g.Expect(wrapper).To(BeIdenticalTo(outer))
	g.Expect(errors.Unwrap(outer)).To(BeIdenticalTo(inner))
	g.Expect(errors.Unwrap(inner)).To(BeIdenticalTo(context.Canceled))
}

// TestModuleErrorDeadlineCause proves a deadline cause keeps its
// identity through the wrapper.
func TestModuleErrorDeadlineCause(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := New(ErrRolloutWait, "the wait budget expired",
		WithCause(context.DeadlineExceeded))

	g.Expect(errors.Is(err, ErrRolloutWait)).To(BeTrue())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
}
