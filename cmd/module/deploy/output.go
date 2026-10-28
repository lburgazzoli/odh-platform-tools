package deploy

import (
	"fmt"
	"io"
	"strings"

	jsonv2 "encoding/json/v2"

	deployer "github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer"
)

// Report statuses of one executed deploy invocation.
const (
	StatusApplied = "applied"
	StatusFailed  = "failed"
)

// Report is the CLI's deployment result.
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

// Owner identifies a server-confirmed definition. Absence means unconfirmed,
// not that no write occurred.
type Owner struct {
	Name       string `json:"name"`
	UID        string `json:"uid,omitzero"`
	APIVersion string `json:"apiVersion,omitzero"`
	Kind       string `json:"kind,omitzero"`
}

// WaitState appears only when rollout waiting was requested.
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

// Failure is the classified execution failure of one deploy invocation.
type Failure struct {
	Category string `json:"category"`
	Message  string `json:"message"`
}

func newReport(definition string, result deployer.Result, failure *Failure) Report {
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

func writeTextReport(w io.Writer, report Report) error {
	var builder strings.Builder

	fmt.Fprintf(&builder, "module deploy %s: %s\n", report.Definition, report.Status)

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

func joinTargets(targets []Target) string {
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		parts = append(parts, target.Namespace+"/"+target.Name)
	}

	return strings.Join(parts, ", ")
}
