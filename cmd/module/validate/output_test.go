package validate

import (
	"errors"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"

	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/validator"
)

// sampleGVK identifies the observed module of the report fixtures.
var sampleGVK = schema.GroupVersionKind{
	Group:   "mock.opendatahub.io",
	Version: "v1alpha1",
	Kind:    "MockModule",
}

// sampleResult carries one pass and one finding so both render paths
// are visible.
func sampleResult() validator.Result {
	return validator.Result{
		Namespace: "sample-ns",
		Checks: []validator.CheckResult{
			{
				ID:      validator.CheckModuleAPI,
				Status:  validator.StatusPass,
				GVK:     sampleGVK,
				Message: "api is discoverable",
			},
			{
				ID:      validator.CheckModuleExists,
				Status:  validator.StatusFail,
				GVK:     sampleGVK,
				Name:    "sample",
				Field:   "status.conditions",
				Message: "condition is missing",
			},
		},
	}
}

func TestNewReport(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	failure := &Failure{Category: FailureRuntime, Message: "boom"}

	report := newReport("definition.yaml", sampleResult(), failure, false)

	g.Expect(report.Definition).To(Equal("definition.yaml"))
	g.Expect(report.Namespace).To(Equal("sample-ns"))
	g.Expect(report.Status).To(Equal(StatusFailed))
	g.Expect(report.Failure).To(Equal(failure))
	g.Expect(report.Checks).To(HaveLen(2))
	g.Expect(report.Checks[0]).To(Equal(Check{
		ID:      validator.CheckModuleAPI,
		Status:  validator.StatusPass,
		Group:   sampleGVK.Group,
		Version: sampleGVK.Version,
		Kind:    sampleGVK.Kind,
		Message: "api is discoverable",
	}))
	g.Expect(report.Checks[1]).To(Equal(Check{
		ID:      validator.CheckModuleExists,
		Status:  validator.StatusFail,
		Group:   sampleGVK.Group,
		Version: sampleGVK.Version,
		Kind:    sampleGVK.Kind,
		Name:    "sample",
		Field:   "status.conditions",
		Message: "condition is missing",
	}))

	passed := newReport("definition.yaml", sampleResult(), nil, true)
	g.Expect(passed.Status).To(Equal(StatusPass))
	g.Expect(passed.Failure).To(BeNil())
}

// TestNewReportEmptyResult checks the checks array is never null, even
// when no cycle completed.
func TestNewReportEmptyResult(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	report := newReport("definition.yaml", validator.Result{}, nil, false)

	g.Expect(report.Checks).NotTo(BeNil())
	g.Expect(report.Checks).To(BeEmpty())
}

func TestWriteTextReport(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var builder strings.Builder

	report := newReport("definition.yaml", sampleResult(),
		&Failure{Category: FailureRuntime, Message: "boom"}, false)

	g.Expect(writeTextReport(&builder, report)).To(Succeed())
	g.Expect(builder.String()).To(Equal(`module validate definition.yaml: failed
namespace: sample-ns
MODULE_API: pass - api is discoverable
MODULE_EXISTS: fail (status.conditions) - condition is missing
runtime: boom
`))
}

// TestWriteTextReportPassAndSkip checks that passes and skips are
// printed too, not only findings.
func TestWriteTextReportPassAndSkip(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result := validator.Result{
		Namespace: "sample-ns",
		Checks: []validator.CheckResult{
			{ID: validator.CheckModuleAPI, Status: validator.StatusPass, Message: "api is discoverable"},
			{ID: validator.CheckModuleExists, Status: validator.StatusSkipped, Message: "requires a usable mapping"},
		},
	}

	var builder strings.Builder

	g.Expect(writeTextReport(&builder, newReport("definition.yaml", result, nil, false))).To(Succeed())
	g.Expect(builder.String()).To(Equal(`module validate definition.yaml: failed
namespace: sample-ns
MODULE_API: pass - api is discoverable
MODULE_EXISTS: skipped - requires a usable mapping
`))
}

func TestWriteJSONReport(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var builder strings.Builder

	report := newReport("definition.yaml", sampleResult(),
		&Failure{Category: FailureTimeout, Message: "budget expired"}, false)

	g.Expect(writeJSONReport(&builder, report)).To(Succeed())

	encoded := builder.String()

	// One newline-terminated document.
	g.Expect(strings.HasSuffix(encoded, "\n")).To(BeTrue())
	g.Expect(strings.Count(encoded, "\n")).To(Equal(1))

	var decoded map[string]any

	g.Expect(jsonv2.Unmarshal([]byte(encoded), &decoded)).To(Succeed())
	g.Expect(decoded["definition"]).To(Equal("definition.yaml"))
	g.Expect(decoded["namespace"]).To(Equal("sample-ns"))
	g.Expect(decoded["status"]).To(Equal(StatusFailed))

	failure, ok := decoded["failure"].(map[string]any)
	g.Expect(ok).To(BeTrue())
	g.Expect(failure["category"]).To(Equal(FailureTimeout))
	g.Expect(failure["message"]).To(Equal("budget expired"))

	checks, ok := decoded["checks"].([]any)
	g.Expect(ok).To(BeTrue())
	g.Expect(checks).To(HaveLen(2))

	first, ok := checks[0].(map[string]any)
	g.Expect(ok).To(BeTrue())
	g.Expect(first["id"]).To(Equal(validator.CheckModuleAPI))
	g.Expect(first["status"]).To(Equal(validator.StatusPass))
	g.Expect(first["group"]).To(Equal(sampleGVK.Group))
	g.Expect(first["version"]).To(Equal(sampleGVK.Version))
	g.Expect(first["kind"]).To(Equal(sampleGVK.Kind))

	second, ok := checks[1].(map[string]any)
	g.Expect(ok).To(BeTrue())
	g.Expect(second["field"]).To(Equal("status.conditions"))
	g.Expect(second["name"]).To(Equal("sample"))
}

// TestWriteJSONReportOmitsEmptyIdentity checks that empty object
// identity and detail fields stay out of the document.
func TestWriteJSONReportOmitsEmptyIdentity(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result := validator.Result{
		Checks: []validator.CheckResult{
			{ID: validator.CheckModuleAPI, Status: validator.StatusPass, Message: "api is discoverable"},
		},
	}

	var builder strings.Builder

	g.Expect(writeJSONReport(&builder, newReport("definition.yaml", result, nil, false))).To(Succeed())

	var decoded map[string]any

	g.Expect(jsonv2.Unmarshal([]byte(builder.String()), &decoded)).To(Succeed())

	checks, ok := decoded["checks"].([]any)
	g.Expect(ok).To(BeTrue())

	first, ok := checks[0].(map[string]any)
	g.Expect(ok).To(BeTrue())

	for _, field := range []string{"group", "version", "kind", "namespace", "name", "field"} {
		g.Expect(first).NotTo(HaveKey(field))
	}

	// No failure object without an execution failure.
	g.Expect(decoded).NotTo(HaveKey("failure"))
}

// TestWriteJSONReportEmptyChecksAreAnArray checks the checks array is
// never JSON null.
func TestWriteJSONReportEmptyChecksAreAnArray(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var builder strings.Builder

	g.Expect(writeJSONReport(&builder, newReport("d", validator.Result{}, nil, false))).To(Succeed())

	g.Expect(builder.String()).To(ContainSubstring(`"checks":[]`))
}

func TestWriteReportUnsupportedFormat(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := writeReport(&strings.Builder{}, "yaml", newReport("d", validator.Result{}, nil, false))

	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("unsupported output format"))
}

// failingWriter fails every write with the configured error.
type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestWriteReportsSurfaceWriteFailures(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cause := errors.New("disk full")

	report := newReport("definition.yaml", sampleResult(), nil, false)

	for name, format := range map[string]string{
		"text": FormatText,
		"json": FormatJSON,
	} {
		err := writeReport(failingWriter{err: cause}, format, report)

		g.Expect(err).To(HaveOccurred(), name)
		g.Expect(errors.Is(err, cause)).To(BeTrue(), name)
	}
}
