package validate

import (
	"fmt"
	"io"
	"strings"

	jsonv2 "encoding/json/v2"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/validator"
)

// Report statuses of one validate invocation.
const (
	StatusPass   = "pass"
	StatusFailed = "failed"
)

// Report is the CLI's validation result.
type Report struct {
	Definition string   `json:"definition"`
	Namespace  string   `json:"namespace"`
	Status     string   `json:"status"`
	Checks     []Check  `json:"checks"`
	Failure    *Failure `json:"failure,omitzero"`
}

// Check is one check's report view: its status, observed object
// identity, and actionable observation. Empty identity and detail
// fields are omitted.
type Check struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Group     string `json:"group,omitzero"`
	Version   string `json:"version,omitzero"`
	Kind      string `json:"kind,omitzero"`
	Namespace string `json:"namespace,omitzero"`
	Name      string `json:"name,omitzero"`
	Field     string `json:"field,omitzero"`
	Message   string `json:"message,omitzero"`
}

// Failure is the classified execution failure of one validate
// invocation.
type Failure struct {
	Category string `json:"category"`
	Message  string `json:"message"`
}

// newReport projects one validation outcome. Checks is always an
// array, never null, even when no cycle completed.
func newReport(
	definition string,
	result validator.Result,
	failure *Failure,
	passed bool,
) Report {
	report := Report{
		Definition: definition,
		Namespace:  result.Namespace,
		Checks:     make([]Check, 0, len(result.Checks)),
		Failure:    failure,
	}

	if passed {
		report.Status = StatusPass
	} else {
		report.Status = StatusFailed
	}

	for i := range result.Checks {
		source := result.Checks[i]

		report.Checks = append(report.Checks, Check{
			ID:        source.ID,
			Status:    source.Status,
			Group:     source.GVK.Group,
			Version:   source.GVK.Version,
			Kind:      source.GVK.Kind,
			Namespace: source.Namespace,
			Name:      source.Name,
			Field:     source.Field,
			Message:   source.Message,
		})
	}

	return report
}

func writeReport(w io.Writer, format string, report Report) error {
	switch format {
	case FormatText:
		return writeTextReport(w, report)
	case FormatJSON:
		return writeJSONReport(w, report)
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func writeJSONReport(w io.Writer, report Report) error {
	encoded, err := jsonv2.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode json report: %w", err)
	}

	if _, err := w.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write json report: %w", err)
	}

	return nil
}

// writeTextReport prints every check, including passes, skips, and the
// checks retained by an execution failure, followed by the failure.
func writeTextReport(w io.Writer, report Report) error {
	var builder strings.Builder

	fmt.Fprintf(&builder, "module validate %s: %s\n", report.Definition, report.Status)

	if report.Namespace != "" {
		fmt.Fprintf(&builder, "namespace: %s\n", report.Namespace)
	}

	for i := range report.Checks {
		check := report.Checks[i]

		fmt.Fprintf(&builder, "%s: %s", check.ID, check.Status)

		if check.Field != "" {
			fmt.Fprintf(&builder, " (%s)", check.Field)
		}

		if check.Message != "" {
			fmt.Fprintf(&builder, " - %s", check.Message)
		}

		builder.WriteString("\n")
	}

	if report.Failure != nil {
		fmt.Fprintf(&builder, "%s: %s\n", report.Failure.Category, report.Failure.Message)
	}

	if _, err := io.WriteString(w, builder.String()); err != nil {
		return fmt.Errorf("write text report: %w", err)
	}

	return nil
}
