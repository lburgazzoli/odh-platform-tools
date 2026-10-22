package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// recordingRun is a fake action boundary recording every invocation.
type recordingRun struct {
	calls    int
	contexts []context.Context
	options  []linter.Options
	result   linter.Result
	err      error
}

// run records one invocation and returns the configured outcome.
func (r *recordingRun) run(ctx context.Context, options linter.Options) (linter.Result, error) {
	r.calls++
	r.contexts = append(r.contexts, ctx)
	r.options = append(r.options, options)

	return r.result, r.err
}

// execute runs one command execution with captured streams.
func execute(
	ctx context.Context,
	t *testing.T,
	run LintRun,
	args ...string,
) (string, string, error) {
	t.Helper()

	root := NewRootCommand(run)

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	err := root.ExecuteContext(ctx)

	return stdout.String(), stderr.String(), err
}

// executeLint runs `module lint` with the given flag arguments.
func executeLint(
	ctx context.Context,
	t *testing.T,
	run LintRun,
	flags ...string,
) (string, string, error) {
	t.Helper()

	return execute(ctx, t, run, append([]string{"module", "lint"}, flags...)...)
}

// reportFromJSON decodes one executed JSON report.
func reportFromJSON(t *testing.T, g *WithT, stdout string) Report {
	t.Helper()

	var report Report
	g.Expect(jsonUnmarshal([]byte(stdout), &report)).To(Succeed())

	return report
}

func TestLintCommandForwardsOptionsAndContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	ctx := t.Context()
	_, _, err := executeLint(ctx, t, recorder.run,
		"--config", "/work/modules/synthetic/definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())

	// Exactly one action call, with the flag value in a plain options
	// struct and the caller's context forwarded untouched.
	g.Expect(recorder.calls).To(Equal(1))
	g.Expect(recorder.options).To(HaveLen(1))
	g.Expect(recorder.options[0]).To(Equal(linter.Options{
		DefinitionFile: "/work/modules/synthetic/definition.yaml",
	}))
	g.Expect(recorder.contexts).To(HaveLen(1))
	g.Expect(recorder.contexts[0]).To(Equal(ctx))
}

func TestLintCommandClassification(t *testing.T) {
	t.Parallel()

	warningFinding := linter.Result{Diagnostics: []linter.Diagnostic{{
		ID:       linter.CheckSingletonWebhookUnverified,
		Severity: linter.SeverityWarning,
		Phase:    linter.PhaseSingleton,
		Message:  "singleton enforcement is only evidenced by an unverified webhook",
	}}}

	errorFinding := linter.Result{Diagnostics: []linter.Diagnostic{{
		ID:       linter.CheckDeploymentMissing,
		Severity: linter.SeverityError,
		Phase:    linter.PhaseDeployment,
		GVK:      schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Name:     "synthetic-controller",
		Message:  "controller deployment not found in rendered objects",
	}}}

	cases := []struct {
		name     string
		result   linter.Result
		err      error
		exitCode int
		status   string
		category string
	}{
		{
			name:     "pass",
			exitCode: ExitSuccess,
			status:   StatusPass,
		},
		{
			name:     "warning-only findings succeed",
			result:   warningFinding,
			exitCode: ExitSuccess,
			status:   StatusFindings,
		},
		{
			name:     "error-severity findings fail",
			result:   errorFinding,
			exitCode: ExitFailure,
			status:   StatusFindings,
		},
		{
			name:     "cancelled context",
			err:      fmt.Errorf("helm backend: %w", context.Canceled),
			exitCode: ExitFailure,
			status:   StatusFailed,
			category: FailureCancelled,
		},
		{
			name:     "deadline exceeded",
			err:      fmt.Errorf("load definition: %w", context.DeadlineExceeded),
			exitCode: ExitFailure,
			status:   StatusFailed,
			category: FailureCancelled,
		},
		{
			name:     "invalid options",
			err:      fmt.Errorf("lint: %w", linter.ErrInvalidOptions),
			exitCode: ExitUsage,
			status:   StatusFailed,
			category: FailureInvalidOptions,
		},
		{
			name:     "definition error",
			err:      config.NewDefinitionError(errors.New("spec.type.group is required")),
			exitCode: ExitUsage,
			status:   StatusFailed,
			category: FailureDefinition,
		},
		{
			name:     "render error",
			err:      errors.New("helm backend for module synthetic: unsafe renderer input"),
			exitCode: ExitFailure,
			status:   StatusFailed,
			category: FailureRender,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			recorder := &recordingRun{result: testCase.result, err: testCase.err}

			stdout, stderr, err := executeLint(t.Context(), t, recorder.run,
				"--config", "definition.yaml", "--output", FormatJSON)

			g.Expect(ExitCode(err)).To(Equal(testCase.exitCode))

			report := reportFromJSON(t, g, stdout)
			g.Expect(report.Status).To(Equal(testCase.status))
			g.Expect(report.Definition).To(Equal("definition.yaml"))
			g.Expect(report.HasErrors).To(Equal(testCase.result.HasErrors()))
			g.Expect(report.HasWarnings).To(Equal(testCase.result.HasWarnings()))

			if testCase.category == "" {
				g.Expect(report.Failure).To(BeNil())
				g.Expect(stderr).To(BeEmpty())
			} else {
				g.Expect(report.Failure).NotTo(BeNil())
				g.Expect(report.Failure.Category).To(Equal(testCase.category))
				g.Expect(stderr).To(ContainSubstring(testCase.category))
			}
		})
	}
}

func TestLintCommandClassificationPrecedence(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	// Cancellation wins even when the same error also matches the
	// definition category through wrapping.
	combined := fmt.Errorf("%w: %w", config.ErrInvalidDefinition, context.Canceled)

	category, code := classify(combined)
	g.Expect(category).To(Equal(FailureCancelled))
	g.Expect(code).To(Equal(ExitFailure))
}

func TestLintCommandTextReportGolden(t *testing.T) {
	t.Parallel()

	t.Run("pass", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		stdout, _, err := executeLint(t.Context(), t, (&recordingRun{}).run,
			"--config", "definition.yaml")
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(Equal("module lint definition.yaml: pass\n"))
	})

	t.Run("findings", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{result: linter.Result{Diagnostics: []linter.Diagnostic{
			{
				ID:       linter.CheckSingletonEvidenceMissing,
				Severity: linter.SeverityError,
				Phase:    linter.PhaseSingleton,
				Message:  "no fixed-name schema or webhook evidence found",
			},
			{
				ID:        linter.CheckServiceAccountUnresolved,
				Severity:  linter.SeverityWarning,
				Phase:     linter.PhaseRBAC,
				GVK:       schema.GroupVersionKind{Version: "v1", Kind: "RoleBinding"},
				Namespace: "default",
				Name:      "synthetic-binding",
				Field:     "subjects[0]",
				Message:   "service account default/synthetic-sa not found",
			},
		}}}

		stdout, stderr, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml")
		g.Expect(err).To(HaveOccurred())
		g.Expect(ExitCode(err)).To(Equal(ExitFailure))
		g.Expect(stderr).To(BeEmpty())

		g.Expect(stdout).To(Equal(`module lint definition.yaml: findings
error SINGLETON_EVIDENCE_MISSING: no fixed-name schema or webhook evidence found
warning SERVICEACCOUNT_UNRESOLVED v1, Kind=RoleBinding/default/synthetic-binding/subjects[0]: service account default/synthetic-sa not found
`))
	})

	t.Run("failed", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{
			err: config.NewDefinitionError(errors.New("spec.type.group is required")),
		}

		stdout, stderr, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml")
		g.Expect(err).To(HaveOccurred())
		g.Expect(ExitCode(err)).To(Equal(ExitUsage))

		g.Expect(stdout).To(ContainSubstring("module lint definition.yaml: failed\n"))
		g.Expect(stdout).To(ContainSubstring("definition: spec.type.group is required\n"))
		g.Expect(stderr).To(ContainSubstring("definition"))
	})
}

func TestLintCommandJSONReportGolden(t *testing.T) {
	t.Parallel()

	t.Run("pass", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		stdout, _, err := executeLint(t.Context(), t, (&recordingRun{}).run,
			"--config", "definition.yaml", "--output", FormatJSON)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(Equal(
			`{"definition":"definition.yaml","status":"pass","hasErrors":false,"hasWarnings":false}` + "\n"))
	})

	t.Run("findings", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{result: linter.Result{Diagnostics: []linter.Diagnostic{{
			ID:       linter.CheckSingletonWebhookUnverified,
			Severity: linter.SeverityWarning,
			Phase:    linter.PhaseSingleton,
			Message:  "singleton enforcement is only evidenced by an unverified webhook",
		}}}}

		stdout, _, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml", "--output", FormatJSON)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(Equal(
			`{"definition":"definition.yaml","status":"findings","hasErrors":false,"hasWarnings":true,` +
				`"diagnostics":[{"id":"SINGLETON_WEBHOOK_UNVERIFIED","severity":"warning",` +
				`"phase":"singleton",` +
				`"message":"singleton enforcement is only evidenced by an unverified webhook"}]}` + "\n"))
	})

	t.Run("failed", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{
			err: fmt.Errorf("load definition: %w", context.Canceled),
		}

		stdout, _, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml", "--output", FormatJSON)
		g.Expect(err).To(HaveOccurred())
		g.Expect(ExitCode(err)).To(Equal(ExitFailure))

		g.Expect(stdout).To(Equal(
			`{"definition":"definition.yaml","status":"failed","hasErrors":false,"hasWarnings":false,` +
				`"failure":{"category":"cancelled","message":"load definition: context canceled"}}` + "\n"))
	})
}

func TestLintCommandFlagFailures(t *testing.T) {
	t.Parallel()

	t.Run("missing config", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{}

		stdout, _, err := executeLint(t.Context(), t, recorder.run)

		g.Expect(err).To(HaveOccurred())
		g.Expect(ExitCode(err)).To(Equal(ExitUsage))
		g.Expect(recorder.calls).To(Equal(0))
		g.Expect(stdout).To(BeEmpty())
	})

	t.Run("invalid output format", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{}

		stdout, _, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml", "--output", "yaml")

		g.Expect(err).To(HaveOccurred())
		g.Expect(ExitCode(err)).To(Equal(ExitUsage))
		g.Expect(recorder.calls).To(Equal(0))
		g.Expect(stdout).To(BeEmpty())
	})

	t.Run("unexpected positional arguments", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{}

		_, _, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml", "extra")

		g.Expect(err).To(HaveOccurred())
		g.Expect(ExitCode(err)).To(Equal(ExitUsage))
		g.Expect(recorder.calls).To(Equal(0))
	})

	t.Run("legacy descriptor override flags are rejected", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{}

		_, _, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml", "--set-image", "manager=quay.io/other:v1")

		g.Expect(err).To(HaveOccurred())
		g.Expect(ExitCode(err)).To(Equal(ExitUsage))
		g.Expect(recorder.calls).To(Equal(0))
	})
}

func TestRootCommandRejectsDeferredCommands(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	stdout, _, err := execute(t.Context(), t, recorder.run,
		"module", "run", "--config", "definition.yaml")

	g.Expect(err).To(HaveOccurred())
	g.Expect(ExitCode(err)).To(Equal(ExitUsage))
	// The deferred command is rejected, never stubbed.
	g.Expect(err.Error()).To(ContainSubstring("unknown"))
	g.Expect(recorder.calls).To(Equal(0))
	g.Expect(stdout).To(BeEmpty())
}

func TestRootCommandHelp(t *testing.T) {
	t.Parallel()

	t.Run("root help", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{}

		stdout, _, err := execute(t.Context(), t, recorder.run, "--help")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(stdout).To(ContainSubstring("Offline platform module tooling"))
		g.Expect(stdout).To(ContainSubstring("\n  module "))
		g.Expect(recorder.calls).To(Equal(0))
	})

	t.Run("lint help", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{}

		stdout, _, err := executeLint(t.Context(), t, recorder.run, "--help")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(stdout).To(ContainSubstring("--config"))
		g.Expect(stdout).To(ContainSubstring("--output"))
		g.Expect(recorder.calls).To(Equal(0))
	})
}

func TestLintCommandStatelessAcrossExecutions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	root := NewRootCommand(recorder.run)

	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&bytes.Buffer{})

	root.SetArgs([]string{"module", "lint", "--config", "first.yaml"})
	g.Expect(root.ExecuteContext(t.Context())).NotTo(HaveOccurred())

	root.SetArgs([]string{"module", "lint", "--config", "second.yaml", "--output", FormatJSON})
	g.Expect(root.ExecuteContext(t.Context())).NotTo(HaveOccurred())

	// The second execution sees only its own flag values: no state
	// leaks from the first run into options or output format.
	g.Expect(recorder.calls).To(Equal(2))
	g.Expect(recorder.options[0].DefinitionFile).To(Equal("first.yaml"))
	g.Expect(recorder.options[1].DefinitionFile).To(Equal("second.yaml"))
	g.Expect(stdout.String()).To(ContainSubstring("module lint first.yaml: pass"))
	g.Expect(stdout.String()).To(ContainSubstring(`"definition":"second.yaml"`))
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	g.Expect(ExitCode(nil)).To(Equal(ExitSuccess))
	g.Expect(ExitCode(errors.New("flag needs an argument"))).To(Equal(ExitUsage))
	g.Expect(ExitCode(fmt.Errorf("wrapped: %w",
		&ExitError{Code: ExitFailure, Err: errors.New("render")}))).To(Equal(ExitFailure))
}
