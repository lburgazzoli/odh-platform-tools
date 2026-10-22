package cli

import (
	"fmt"
	"io"
	"strings"

	jsonv2 "encoding/json/v2"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// Report statuses of one executed lint invocation.
const (
	StatusPass     = "pass"
	StatusFindings = "findings"
	StatusFailed   = "failed"
)

// Report is the stable wire form of one executed lint invocation:
// the definition it ran against, the aggregated outcome, every
// finding, and the classified failure when execution did not
// complete.
type Report struct {
	Definition  string       `json:"definition"`
	Status      string       `json:"status"`
	HasErrors   bool         `json:"hasErrors"`
	HasWarnings bool         `json:"hasWarnings"`
	Diagnostics []Diagnostic `json:"diagnostics,omitzero"`
	Failure     *Failure     `json:"failure,omitzero"`
}

// Diagnostic is the report form of one linter finding.
type Diagnostic struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Phase     string `json:"phase"`
	Group     string `json:"group,omitzero"`
	Version   string `json:"version,omitzero"`
	Kind      string `json:"kind,omitzero"`
	Namespace string `json:"namespace,omitzero"`
	Name      string `json:"name,omitzero"`
	Field     string `json:"field,omitzero"`
	Message   string `json:"message"`
}

// Failure is the classified execution failure of one lint invocation.
type Failure struct {
	Category string `json:"category"`
	Message  string `json:"message"`
}

// newReport builds the report of one lint invocation from the
// validator result and the classified failure, if execution did not
// complete.
func newReport(definition string, result linter.Result, failure *Failure) Report {
	report := Report{
		Definition:  definition,
		HasErrors:   result.HasErrors(),
		HasWarnings: result.HasWarnings(),
		Failure:     failure,
	}

	switch {
	case failure != nil:
		report.Status = StatusFailed
	case len(result.Diagnostics) > 0:
		report.Status = StatusFindings
	default:
		report.Status = StatusPass
	}

	// The diagnostics slice stays nil when empty so the JSON form
	// omits it entirely.
	if len(result.Diagnostics) > 0 {
		report.Diagnostics = make([]Diagnostic, 0, len(result.Diagnostics))

		for _, finding := range result.Diagnostics {
			report.Diagnostics = append(report.Diagnostics, Diagnostic{
				ID:        finding.ID,
				Severity:  string(finding.Severity),
				Phase:     string(finding.Phase),
				Group:     finding.GVK.Group,
				Version:   finding.GVK.Version,
				Kind:      finding.GVK.Kind,
				Namespace: finding.Namespace,
				Name:      finding.Name,
				Field:     finding.Field,
				Message:   finding.Message,
			})
		}
	}

	return report
}

// writeReport writes one report in the selected format.
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

// writeJSONReport writes the report as one parseable JSON document.
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

// writeTextReport writes the report as stable human-readable lines.
func writeTextReport(w io.Writer, report Report) error {
	var builder strings.Builder

	if report.Failure != nil {
		fmt.Fprintf(&builder, "module lint %s: failed\n", report.Definition)
		fmt.Fprintf(&builder, "%s: %s\n", report.Failure.Category, report.Failure.Message)
	} else {
		fmt.Fprintf(&builder, "module lint %s: %s\n", report.Definition, report.Status)

		for _, diagnostic := range report.Diagnostics {
			fmt.Fprintf(&builder, "%s\n", diagnostic.String())
		}
	}

	if _, err := io.WriteString(w, builder.String()); err != nil {
		return fmt.Errorf("write text report: %w", err)
	}

	return nil
}

// String renders the diagnostic as one report line, mirroring the
// linter's own line form: severity, check ID, object path, message.
func (d Diagnostic) String() string {
	if path := d.path(); path != "" {
		return fmt.Sprintf("%s %s %s: %s", d.Severity, d.ID, path, d.Message)
	}

	return fmt.Sprintf("%s %s: %s", d.Severity, d.ID, d.Message)
}

// path returns the object path the diagnostic refers to, such as
// "apps/v1, Kind=Deployment/default/synthetic-controller", or an
// empty string for findings not attached to one object.
func (d Diagnostic) path() string {
	var parts []string

	if d.Kind != "" {
		groupVersion := d.Version
		if d.Group != "" {
			groupVersion = d.Group + "/" + d.Version
		}

		parts = append(parts, groupVersion+", Kind="+d.Kind)
	}

	for _, part := range []string{d.Namespace, d.Name, d.Field} {
		if part != "" {
			parts = append(parts, part)
		}
	}

	return strings.Join(parts, "/")
}
