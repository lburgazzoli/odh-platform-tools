package lint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	. "github.com/onsi/gomega"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/crd"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/deployments"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter/checks/types"
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

// exitCode maps one lint command error to its process exit status:
// classified outcomes keep their own code, and flag or usage
// failures are usage failures.
func exitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}

	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}

	return ExitUsage
}

// executeLint runs the lint command with the given flag arguments.
func executeLint(
	ctx context.Context,
	t *testing.T,
	run Run,
	flags ...string,
) (string, string, error) {
	t.Helper()

	command := NewLintCommand(run)

	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(flags)

	err := command.ExecuteContext(ctx)

	return stdout.String(), stderr.String(), err
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
		ID:       "EXAMPLE_WARNING",
		Severity: linter.SeverityWarning,
		Message:  "a claim that could not be verified",
	}}}

	errorFinding := linter.Result{Diagnostics: []linter.Diagnostic{{
		ID:       deployments.Missing,
		Severity: linter.SeverityError,
		GVK: schema.GroupVersionKind{
			Group:   "components.platform.opendatahub.io",
			Version: "v1alpha1",
			Kind:    "SyntheticModule",
		},
		Message: "module must bundle exactly one Deployment, found 0",
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
			err:      fmt.Errorf("lint: %w", moduleerrors.ErrInvalidLintOptions),
			exitCode: ExitUsage,
			status:   StatusFailed,
			category: FailureInvalidOptions,
		},
		{
			name: "lint options wrapper",
			err: moduleerrors.New(moduleerrors.ErrInvalidLintOptions,
				"invalid lint options: DefinitionFile is required"),
			exitCode: ExitUsage,
			status:   StatusFailed,
			category: FailureInvalidOptions,
		},
		{
			name: "run options wrapper is not a lint usage failure",
			err: moduleerrors.New(moduleerrors.ErrInvalidRunOptions,
				"invalid run options: DefinitionFile is required"),
			exitCode: ExitFailure,
			status:   StatusFailed,
			category: FailureRender,
		},
		{
			name: "definition wrapper",
			err: moduleerrors.New(moduleerrors.ErrInvalidDefinition,
				"invalid module definition: bad spec",
				moduleerrors.WithCause(errors.New("bad spec"))),
			exitCode: ExitUsage,
			status:   StatusFailed,
			category: FailureDefinition,
		},
		{
			name: "definition wrapper carrying cancellation",
			err: moduleerrors.New(moduleerrors.ErrInvalidDefinition,
				"invalid module definition: cancelled",
				moduleerrors.WithCause(context.Canceled)),
			exitCode: ExitFailure,
			status:   StatusFailed,
			category: FailureCancelled,
		},
		{
			name: "definition wrapper carrying deadline",
			err: moduleerrors.New(moduleerrors.ErrInvalidDefinition,
				"invalid module definition: deadline",
				moduleerrors.WithCause(context.DeadlineExceeded)),
			exitCode: ExitFailure,
			status:   StatusFailed,
			category: FailureCancelled,
		},
		{
			name:     "render error",
			err:      errors.New("kustomize render for module synthetic: accumulate resources: accumulate"),
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

			g.Expect(exitCode(err)).To(Equal(testCase.exitCode))

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
	combined := fmt.Errorf("%w: %w", moduleerrors.ErrInvalidDefinition, context.Canceled)

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
				ID:       crd.Missing,
				Severity: linter.SeverityError,
				GVK: schema.GroupVersionKind{
					Group:   "components.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "SyntheticModule",
				},
				Message: "no CustomResourceDefinition for " +
					"components.platform.opendatahub.io, Kind=SyntheticModule in the rendered set",
			},
			{
				ID:        types.ResourceKindDisallowed,
				Severity:  linter.SeverityWarning,
				GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Namespace"},
				Namespace: "synthetic",
				Name:      "odh-applications",
				Field:     "metadata.name",
				Message:   "resource kind is not allowed for module manifests",
			},
		}}}

		stdout, stderr, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml")
		g.Expect(err).To(HaveOccurred())
		g.Expect(exitCode(err)).To(Equal(ExitFailure))
		g.Expect(stderr).To(BeEmpty())

		g.Expect(stdout).To(Equal(`module lint definition.yaml: findings
error MODULE_CRD_MISSING components.platform.opendatahub.io/v1alpha1, Kind=SyntheticModule: no CustomResourceDefinition for components.platform.opendatahub.io, Kind=SyntheticModule in the rendered set
warning RESOURCE_KIND_DISALLOWED v1, Kind=Namespace/synthetic/odh-applications/metadata.name: resource kind is not allowed for module manifests
`))
	})

	t.Run("failed", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{
			err: moduleerrors.New(moduleerrors.ErrInvalidDefinition,
				"invalid module definition: unknown object member name \"type\"",
				moduleerrors.WithCause(errors.New(`unknown object member name "type"`))),
		}

		stdout, stderr, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml")
		g.Expect(err).To(HaveOccurred())
		g.Expect(exitCode(err)).To(Equal(ExitUsage))

		g.Expect(stdout).To(ContainSubstring("module lint definition.yaml: failed\n"))
		g.Expect(stdout).To(ContainSubstring(`definition: unknown object member name "type"` + "\n"))
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
			ID:       "EXAMPLE_WARNING",
			Severity: linter.SeverityWarning,
			Message:  "a claim that could not be verified",
		}}}}

		stdout, _, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml", "--output", FormatJSON)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(Equal(
			`{"definition":"definition.yaml","status":"findings","hasErrors":false,"hasWarnings":true,` +
				`"diagnostics":[{"id":"EXAMPLE_WARNING","severity":"warning",` +
				`"message":"a claim that could not be verified"}]}` + "\n"))
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
		g.Expect(exitCode(err)).To(Equal(ExitFailure))

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
		g.Expect(exitCode(err)).To(Equal(ExitUsage))
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
		g.Expect(exitCode(err)).To(Equal(ExitUsage))
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
		g.Expect(exitCode(err)).To(Equal(ExitUsage))
		g.Expect(recorder.calls).To(Equal(0))
	})

	t.Run("legacy descriptor override flags are rejected", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{}

		_, _, err := executeLint(t.Context(), t, recorder.run,
			"--config", "definition.yaml", "--set-image", "manager=quay.io/other:v1")

		g.Expect(err).To(HaveOccurred())
		g.Expect(exitCode(err)).To(Equal(ExitUsage))
		g.Expect(recorder.calls).To(Equal(0))
	})
}

func TestLintCommandStatelessAcrossExecutions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	command := NewLintCommand(recorder.run)

	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&bytes.Buffer{})

	command.SetArgs([]string{"--config", "first.yaml"})
	g.Expect(command.ExecuteContext(t.Context())).NotTo(HaveOccurred())

	command.SetArgs([]string{"--config", "second.yaml", "--output", FormatJSON})
	g.Expect(command.ExecuteContext(t.Context())).NotTo(HaveOccurred())

	// The second execution sees only its own flag values: no state
	// leaks from the first run into options or output format.
	g.Expect(recorder.calls).To(Equal(2))
	g.Expect(recorder.options[0].DefinitionFile).To(Equal("first.yaml"))
	g.Expect(recorder.options[1].DefinitionFile).To(Equal("second.yaml"))
	g.Expect(stdout.String()).To(ContainSubstring("module lint first.yaml: pass"))
	g.Expect(stdout.String()).To(ContainSubstring(`"definition":"second.yaml"`))
}

func TestLintCommandWriteFailuresExitAsExecutionFailures(t *testing.T) {
	t.Parallel()

	t.Run("report write failure preserves cancellation identity", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{
			err: fmt.Errorf("render manifests: %w", context.Canceled),
		}

		command := NewLintCommand(recorder.run)
		command.SetOut(failingWriter{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"--config", "definition.yaml"})

		err := command.ExecuteContext(t.Context())

		g.Expect(exitCode(err)).To(Equal(ExitFailure))
		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		g.Expect(err.Error()).To(ContainSubstring("write report"))
	})

	t.Run("report write failure on a successful action", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{}

		command := NewLintCommand(recorder.run)
		command.SetOut(failingWriter{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"--config", "definition.yaml"})

		err := command.ExecuteContext(t.Context())

		g.Expect(exitCode(err)).To(Equal(ExitFailure))
		g.Expect(err.Error()).To(ContainSubstring("write report"))
	})

	t.Run("summary write failure preserves the action outcome", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		definitionFailure := moduleerrors.New(moduleerrors.ErrInvalidDefinition,
			"invalid module definition: bad spec",
			moduleerrors.WithCause(errors.New("bad spec")))

		recorder := &recordingRun{err: definitionFailure}

		command := NewLintCommand(recorder.run)
		command.SetOut(&bytes.Buffer{})
		command.SetErr(failingWriter{})
		command.SetArgs([]string{"--config", "definition.yaml"})

		err := command.ExecuteContext(t.Context())

		g.Expect(exitCode(err)).To(Equal(ExitFailure))
		g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
		g.Expect(err.Error()).To(ContainSubstring("write failure summary"))
	})
}
