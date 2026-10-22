package lint

import (
	"bytes"
	"errors"
	"testing"

	jsonv2 "encoding/json/v2"

	"k8s.io/apimachinery/pkg/runtime/schema"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/crd"
)

// jsonUnmarshal decodes one JSON report document.
func jsonUnmarshal(data []byte, report *Report) error {
	return jsonv2.Unmarshal(data, report)
}

func TestNewReportStatuses(t *testing.T) {
	t.Parallel()

	t.Run("pass", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		report := newReport("definition.yaml", linter.Result{}, nil)

		g.Expect(report.Status).To(Equal(StatusPass))
		g.Expect(report.HasErrors).To(BeFalse())
		g.Expect(report.HasWarnings).To(BeFalse())
		g.Expect(report.Diagnostics).To(BeEmpty())
		g.Expect(report.Failure).To(BeNil())
	})

	t.Run("findings", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		result := linter.Result{Diagnostics: []linter.Diagnostic{
			{ID: "A", Severity: linter.SeverityWarning},
			{ID: "B", Severity: linter.SeverityInfo},
		}}

		report := newReport("definition.yaml", result, nil)

		g.Expect(report.Status).To(Equal(StatusFindings))
		g.Expect(report.HasErrors).To(BeFalse())
		g.Expect(report.HasWarnings).To(BeTrue())
		g.Expect(report.Diagnostics).To(HaveLen(2))
	})

	t.Run("failure", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		report := newReport("definition.yaml", linter.Result{},
			&Failure{Category: FailureRender, Message: "unsafe renderer input"})

		g.Expect(report.Status).To(Equal(StatusFailed))
		g.Expect(report.Failure.Category).To(Equal(FailureRender))
	})
}

func TestNewReportDiagnosticFields(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result := linter.Result{Diagnostics: []linter.Diagnostic{{
		ID:        "A_FINDING",
		Severity:  linter.SeverityError,
		GVK:       schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Namespace: "default",
		Name:      "synthetic-controller",
		Field:     "spec.template.spec.containers",
		Message:   "container manager not found",
	}}}

	report := newReport("definition.yaml", result, nil)

	g.Expect(report.Diagnostics).To(HaveLen(1))
	g.Expect(report.Diagnostics[0]).To(Equal(Diagnostic{
		ID:        "A_FINDING",
		Severity:  "error",
		Group:     "apps",
		Version:   "v1",
		Kind:      "Deployment",
		Namespace: "default",
		Name:      "synthetic-controller",
		Field:     "spec.template.spec.containers",
		Message:   "container manager not found",
	}))
}

func TestWriteReportFormatSelection(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	report := newReport("definition.yaml", linter.Result{}, nil)

	var text, json bytes.Buffer
	g.Expect(writeReport(&text, FormatText, report)).To(Succeed())
	g.Expect(writeReport(&json, FormatJSON, report)).To(Succeed())

	g.Expect(text.String()).To(Equal("module lint definition.yaml: pass\n"))
	g.Expect(json.String()).To(ContainSubstring(`"status":"pass"`))

	g.Expect(writeReport(&text, "yaml", report)).To(MatchError(ContainSubstring("unsupported output format")))
}

func TestWriteJSONReportIsParseable(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result := linter.Result{Diagnostics: []linter.Diagnostic{{
		ID:       crd.Missing,
		Severity: linter.SeverityError,
		Message:  "module crd not found in rendered objects",
	}}}

	report := newReport("definition.yaml", result, nil)

	var buffer bytes.Buffer
	g.Expect(writeJSONReport(&buffer, report)).To(Succeed())

	var decoded Report
	g.Expect(jsonUnmarshal(buffer.Bytes(), &decoded)).To(Succeed())

	g.Expect(decoded).To(Equal(report))
}

func TestDiagnosticPath(t *testing.T) {
	t.Parallel()

	t.Run("grouped kind", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostic := Diagnostic{
			Group:     "rbac.authorization.k8s.io",
			Version:   "v1",
			Kind:      "RoleBinding",
			Namespace: "default",
			Name:      "synthetic-binding",
		}

		g.Expect(diagnostic.path()).To(Equal(
			"rbac.authorization.k8s.io/v1, Kind=RoleBinding/default/synthetic-binding"))
	})

	t.Run("core kind", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostic := Diagnostic{Version: "v1", Kind: "ServiceAccount", Name: "synthetic-sa"}

		g.Expect(diagnostic.path()).To(Equal("v1, Kind=ServiceAccount/synthetic-sa"))
	})

	t.Run("no object", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		g.Expect(Diagnostic{}.path()).To(BeEmpty())
	})
}

func TestWriteReportWriteErrors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	report := newReport("definition.yaml", linter.Result{}, nil)

	g.Expect(writeReport(failingWriter{}, FormatText, report)).To(HaveOccurred())
	g.Expect(writeReport(failingWriter{}, FormatJSON, report)).To(HaveOccurred())
}

// failingWriter always fails.
type failingWriter struct{}

// Write fails with a fixed error.
func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failure") }

func TestOutputFormatFlag(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var format outputFormat

	g.Expect(format.String()).To(BeEmpty())
	g.Expect(format.Type()).To(Equal("format"))

	g.Expect(format.Set("text")).To(Succeed())
	g.Expect(format.String()).To(Equal(FormatText))

	g.Expect(format.Set("json")).To(Succeed())
	g.Expect(format.String()).To(Equal(FormatJSON))

	g.Expect(format.Set("yaml")).To(MatchError(`output must be "text" or "json"`))
	g.Expect(format.String()).To(Equal(FormatJSON))
}
