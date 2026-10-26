package run

import (
	"fmt"
	"io"
	"strings"

	jsonv2 "encoding/json/v2"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner"
)

// Report formatting below is the stable wire and text form of one
// executed module run invocation. Reports carry only safe, confirmed
// facts — never readiness claims by default, never authored spec
// values, image sources, or API-echoed content, and never fabricated
// counts.

// Report statuses of one executed run invocation.
const (
	StatusApplied = "applied"
	StatusFailed  = "failed"
)

// Report is the stable wire form of one executed run invocation: the
// definition it ran against, the confirmed owner identity, how far
// the stages ran, apply completion, the wait state when one was
// requested, and the classified failure when execution did not
// complete.
type Report struct {
	Definition     string     `json:"definition"`
	Status         string     `json:"status"`
	Owner          *Owner     `json:"owner,omitzero"`
	Attempted      string     `json:"attemptedStage,omitzero"`
	Completed      string     `json:"completedStage,omitzero"`
	ApplyCompleted bool       `json:"applyCompleted"`
	Wait           *WaitState `json:"wait,omitzero"`
	Failure        *Failure   `json:"failure,omitzero"`
}

// Owner is the safe identity of the confirmed definition owner:
// name, server-assigned UID, and type identity only. Its absence
// means the identity is unconfirmed, never that no write occurred.
type Owner struct {
	Name       string `json:"name"`
	UID        string `json:"uid,omitzero"`
	APIVersion string `json:"apiVersion,omitzero"`
	Kind       string `json:"kind,omitzero"`
}

// WaitState is the requested wait's outcome. It appears only when
// the invocation requested the wait: an unrequested wait never
// claims readiness.
type WaitState struct {
	Requested bool     `json:"requested"`
	Completed bool     `json:"completed"`
	Targets   []Target `json:"targets,omitzero"`
}

// Target is the safe identity of one waited controller Deployment:
// namespace and name only.
type Target struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Failure is the classified execution failure of one run invocation.
type Failure struct {
	Category string `json:"category"`
	Message  string `json:"message"`
}

// newReport builds the report of one run invocation from the action
// result and the classified failure, if execution did not complete.
func newReport(definition string, result runner.Result, failure *Failure) Report {
	report := Report{
		Definition:     definition,
		ApplyCompleted: result.ApplyCompleted,
		Failure:        failure,
	}

	if failure != nil {
		report.Status = StatusFailed
	} else {
		report.Status = StatusApplied
	}

	if result.Owner != nil {
		report.Owner = &Owner{
			Name:       result.Owner.Name,
			UID:        string(result.Owner.UID),
			APIVersion: result.Owner.APIVersion,
			Kind:       result.Owner.Kind,
		}
	}

	report.Attempted = string(result.Attempted)
	report.Completed = string(result.Completed)

	if result.WaitRequested {
		wait := &WaitState{
			Requested: result.WaitRequested,
			Completed: result.WaitCompleted,
		}

		for _, target := range result.WaitTargets {
			wait.Targets = append(wait.Targets, Target{
				Namespace: target.Namespace,
				Name:      target.Name,
			})
		}

		report.Wait = wait
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

	fmt.Fprintf(&builder, "module run %s: %s\n", report.Definition, report.Status)

	if report.Owner != nil {
		if report.Owner.UID != "" {
			fmt.Fprintf(&builder, "owner: %s (uid %s)\n", report.Owner.Name, report.Owner.UID)
		} else {
			fmt.Fprintf(&builder, "owner: %s\n", report.Owner.Name)
		}
	}

	if report.Completed != "" {
		fmt.Fprintf(&builder, "stages: %s completed\n", report.Completed)
	}

	if report.ApplyCompleted {
		fmt.Fprintf(&builder, "apply: completed\n")
	}

	if report.Wait != nil {
		if report.Wait.Completed {
			fmt.Fprintf(&builder, "wait: completed (%s)\n", joinTargets(report.Wait.Targets))
		} else {
			fmt.Fprintf(&builder, "wait: incomplete\n")
		}
	}

	if report.Failure != nil {
		fmt.Fprintf(&builder, "%s: %s\n", report.Failure.Category, report.Failure.Message)
	}

	if _, err := io.WriteString(w, builder.String()); err != nil {
		return fmt.Errorf("write text report: %w", err)
	}

	return nil
}

// joinTargets renders waited target identities as a comma-separated
// namespace/name list.
func joinTargets(targets []Target) string {
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		parts = append(parts, target.Namespace+"/"+target.Name)
	}

	return strings.Join(parts, ", ")
}
