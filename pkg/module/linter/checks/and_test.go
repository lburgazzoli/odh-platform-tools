package checks_test

import (
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks"
)

// finding returns a validation that reports one diagnostic with the
// given ID.
func finding(id string) linter.Validation {
	return func(
		_ config.PlatformModuleDefinition,
		_ []unstructured.Unstructured,
	) ([]linter.Diagnostic, error) {
		return []linter.Diagnostic{{ID: id, Severity: linter.SeverityError}}, nil
	}
}

// failing returns a validation that fails to evaluate.
func failing(err error) linter.Validation {
	return func(
		_ config.PlatformModuleDefinition,
		_ []unstructured.Unstructured,
	) ([]linter.Diagnostic, error) {
		return nil, err
	}
}

func TestAnd(t *testing.T) {
	t.Parallel()

	definition := config.PlatformModuleDefinition{}
	var resources []unstructured.Unstructured

	t.Run("runs every validation and concatenates the findings in order", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := checks.And(finding("FIRST"), finding("SECOND"))(definition, resources)

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(2))
		g.Expect(diagnostics[0].ID).To(Equal("FIRST"))
		g.Expect(diagnostics[1].ID).To(Equal("SECOND"))
	})

	t.Run("aborts with the first evaluation error", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		evaluation := errors.New("evaluation failed")

		diagnostics, err := checks.And(finding("FIRST"), failing(evaluation), finding("THIRD"))(definition, resources)

		g.Expect(err).To(MatchError(evaluation))
		g.Expect(diagnostics).To(BeNil())
	})

	t.Run("composing no validation yields no findings", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := checks.And()(definition, resources)

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(BeEmpty())
	})
}
