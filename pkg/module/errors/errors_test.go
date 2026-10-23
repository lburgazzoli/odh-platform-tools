package errors

import (
	"context"
	"errors"
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
