package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/images"
	moduleowner "github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/owner"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// confirmedOwner is the server-backed owner identity of the golden
// reports.
func confirmedOwner() *v1alpha1.PlatformModuleDefinition {
	return &v1alpha1.PlatformModuleDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic", UID: "definition-uid"},
	}
}

// imageSourceError returns one real image-source failure from the
// images service, whose type is not constructible outside its
// package: an explicit but unreadable related-images file.
func imageSourceError() error {
	_, err := images.Load("/nonexistent/images.env", nil)

	return err
}

// recordingRun is a fake action boundary recording every invocation.
type recordingRun struct {
	calls    int
	contexts []context.Context
	getters  []genericclioptions.RESTClientGetter
	options  []runner.Options
	result   runner.Result
	err      error
}

// run records one invocation and returns the configured outcome.
func (r *recordingRun) run(
	ctx context.Context,
	getter genericclioptions.RESTClientGetter,
	options runner.Options,
) (runner.Result, error) {
	r.calls++
	r.contexts = append(r.contexts, ctx)
	r.getters = append(r.getters, getter)
	r.options = append(r.options, options)

	return r.result, r.err
}

// failingWriter fails every write.
type failingWriter struct{}

// Write fails unconditionally.
func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failure")
}

// exitCode maps one run command error to its process exit status:
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

// executeRun runs the run command with the given flag arguments.
func executeRun(
	ctx context.Context,
	t *testing.T,
	action Run,
	flags ...string,
) (string, string, error) {
	t.Helper()

	command := NewRunCommand(action, genericclioptions.NewConfigFlags(true))

	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(flags)

	err := command.ExecuteContext(ctx)

	return stdout.String(), stderr.String(), err
}

// runFlags are the minimal required flag arguments.
var runFlags = []string{"--config", "definition.yaml", "--platform-version", "2.19.0"}

func TestRunCommandForwardsOptionsContextAndGetter(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	getter := genericclioptions.NewConfigFlags(true)
	command := NewRunCommand(recorder.run, getter)

	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--config", "/work/modules/synthetic/definition.yaml",
		"--platform-version", "v2.19.0+dev",
		"--related-images-file", "/work/images.env",
		"--applications-namespace", "applications",
		"--monitoring-namespace", "monitoring",
		"--platform-type", "OpenDataHub",
		"--timeout", "90s",
		"--wait",
		"--wait-timeout", "45s",
		"--output", "json",
	})

	ctx := t.Context()
	g.Expect(command.ExecuteContext(ctx)).NotTo(HaveOccurred())

	// Exactly one action call, with every flag value in a plain
	// options struct, the caller's context, and the root's standard
	// getter forwarded untouched.
	g.Expect(recorder.calls).To(Equal(1))
	g.Expect(recorder.options).To(Equal([]runner.Options{{
		DefinitionFile:        "/work/modules/synthetic/definition.yaml",
		RelatedImagesFile:     "/work/images.env",
		ApplicationsNamespace: "applications",
		MonitoringNamespace:   "monitoring",
		PlatformType:          "OpenDataHub",
		PlatformVersion:       "v2.19.0+dev",
		Timeout:               90 * time.Second,
		Wait:                  true,
		WaitTimeout:           45 * time.Second,
	}}))
	g.Expect(recorder.contexts).To(HaveLen(1))
	g.Expect(recorder.contexts[0]).To(Equal(ctx))
	g.Expect(recorder.getters).To(HaveLen(1))
	g.Expect(recorder.getters[0]).To(BeIdenticalTo(getter))
}

func TestRunCommandDefaults(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	_, _, err := executeRun(t.Context(), t, recorder.run, runFlags...)
	g.Expect(err).NotTo(HaveOccurred())

	// No wait by default, the standard budgets, and text output.
	g.Expect(recorder.options).To(Equal([]runner.Options{{
		DefinitionFile:  "definition.yaml",
		PlatformVersion: "2.19.0",
		Timeout:         DefaultTimeout,
		Wait:            false,
		WaitTimeout:     DefaultWaitTimeout,
	}}))
}

func TestRunCommandClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		err      error
		category string
		code     int
	}{
		{"success", nil, "", ExitSuccess},
		{
			"cancellation",
			fmt.Errorf("run: %w", context.Canceled),
			FailureCancelled, ExitFailure,
		},
		{
			"deadline",
			fmt.Errorf("waiting: %w", context.DeadlineExceeded),
			FailureCancelled, ExitFailure,
		},
		{
			"invalid options",
			fmt.Errorf("load: %w", runner.ErrInvalidOptions),
			FailureInvalidOptions, ExitUsage,
		},
		{
			"definition",
			fmt.Errorf("load: %w", moduleerrors.ErrInvalidDefinition),
			FailureDefinition, ExitUsage,
		},
		{
			"image file",
			fmt.Errorf("load: %w", imageSourceError()),
			FailureImageFile, ExitUsage,
		},
		{
			"runtime",
			errors.New("apply failure"),
			FailureRuntime, ExitFailure,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			category, code := classify(testCase.err)
			g.Expect(category).To(Equal(testCase.category))
			g.Expect(code).To(Equal(testCase.code))

			// The command wires the classification into its exit
			// outcome.
			recorder := &recordingRun{err: testCase.err}
			_, _, err := executeRun(t.Context(), t, recorder.run, runFlags...)
			g.Expect(exitCode(err)).To(Equal(testCase.code))
		})
	}
}

func TestRunCommandTextReportGolden(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{result: runner.Result{
			Attempted:      runner.StageDeploy,
			Completed:      runner.StageDeploy,
			Owner:          confirmedOwner(),
			Publication:    moduleowner.OutcomeConfirmed,
			ApplyCompleted: true,
		}}

		stdout, stderr, err := executeRun(t.Context(), t, recorder.run, runFlags...)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(Equal(`module run definition.yaml: applied
owner: synthetic (uid definition-uid)
publication: confirmed
stages: deploy completed
apply: completed
`))
		g.Expect(stderr).To(BeEmpty())
	})

	t.Run("success with wait", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{result: runner.Result{
			Attempted:      runner.StageDeploy,
			Completed:      runner.StageDeploy,
			Owner:          confirmedOwner(),
			Publication:    moduleowner.OutcomeConfirmed,
			ApplyCompleted: true,
			WaitRequested:  true,
			WaitCompleted:  true,
			WaitTargets: []runner.Target{
				{Namespace: "run-namespace", Name: "synthetic-controller"},
			},
		}}

		stdout, _, err := executeRun(t.Context(), t, recorder.run,
			append(slicesClone(runFlags), "--wait")...)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(Equal(`module run definition.yaml: applied
owner: synthetic (uid definition-uid)
publication: confirmed
stages: deploy completed
apply: completed
wait: completed (run-namespace/synthetic-controller)
`))
	})

	t.Run("wait failure retains successful apply", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{
			result: runner.Result{
				Attempted:      runner.StageDeploy,
				Completed:      runner.StageRender,
				Owner:          confirmedOwner(),
				Publication:    moduleowner.OutcomeConfirmed,
				ApplyCompleted: true,
				WaitRequested:  true,
				WaitCompleted:  false,
				WaitTargets: []runner.Target{
					{Namespace: "run-namespace", Name: "synthetic-controller"},
				},
			},
			err: fmt.Errorf("waiting for module synthetic controller rollout: %w", context.DeadlineExceeded),
		}

		stdout, stderr, err := executeRun(t.Context(), t, recorder.run,
			append(slicesClone(runFlags), "--wait")...)
		g.Expect(exitCode(err)).To(Equal(ExitFailure))
		g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())

		g.Expect(stdout).To(Equal(`module run definition.yaml: failed
owner: synthetic (uid definition-uid)
publication: confirmed
stages: render completed
apply: completed
wait: incomplete
cancelled: waiting for module synthetic controller rollout: context deadline exceeded
`))
		g.Expect(stderr).To(ContainSubstring("module run definition.yaml: cancelled:"))
	})

	t.Run("unconfirmed owner does not claim no write", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{
			result: runner.Result{
				Attempted:   runner.StageDeploy,
				Completed:   runner.StageRender,
				Publication: moduleowner.OutcomeUnknown,
			},
			err: errors.New("publication failure"),
		}

		stdout, _, err := executeRun(t.Context(), t, recorder.run, runFlags...)
		g.Expect(exitCode(err)).To(Equal(ExitFailure))

		// No owner line at all: the identity is unconfirmed, which is
		// not a claim that no write occurred.
		g.Expect(stdout).To(Equal(`module run definition.yaml: failed
publication: unknown
stages: render completed
runtime: publication failure
`))
	})
}

func TestRunCommandJSONReportGolden(t *testing.T) {
	t.Parallel()

	t.Run("success without wait omits wait state", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{result: runner.Result{
			Attempted:      runner.StageDeploy,
			Completed:      runner.StageDeploy,
			Owner:          confirmedOwner(),
			Publication:    moduleowner.OutcomeConfirmed,
			ApplyCompleted: true,
		}}

		stdout, _, err := executeRun(t.Context(), t, recorder.run,
			append(slicesClone(runFlags), "--output", "json")...)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(MatchJSON(`{
			"definition": "definition.yaml",
			"status": "applied",
			"owner": {
				"name": "synthetic",
				"uid": "definition-uid",
				"apiVersion": "config.opendatahub.io/v1alpha1",
				"kind": "PlatformModuleDefinition"
			},
			"publication": "confirmed",
			"attemptedStage": "deploy",
			"completedStage": "deploy",
			"applyCompleted": true
		}`))
	})

	t.Run("success with wait", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{result: runner.Result{
			Attempted:      runner.StageDeploy,
			Completed:      runner.StageDeploy,
			Owner:          confirmedOwner(),
			Publication:    moduleowner.OutcomeConfirmed,
			ApplyCompleted: true,
			WaitRequested:  true,
			WaitCompleted:  true,
			WaitTargets: []runner.Target{
				{Namespace: "run-namespace", Name: "synthetic-controller"},
			},
		}}

		stdout, _, err := executeRun(t.Context(), t, recorder.run,
			append(slicesClone(runFlags), "--wait", "--output", "json")...)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(MatchJSON(`{
			"definition": "definition.yaml",
			"status": "applied",
			"owner": {
				"name": "synthetic",
				"uid": "definition-uid",
				"apiVersion": "config.opendatahub.io/v1alpha1",
				"kind": "PlatformModuleDefinition"
			},
			"publication": "confirmed",
			"attemptedStage": "deploy",
			"completedStage": "deploy",
			"applyCompleted": true,
			"wait": {
				"requested": true,
				"completed": true,
				"targets": [
					{"namespace": "run-namespace", "name": "synthetic-controller"}
				]
			}
		}`))
	})

	t.Run("failure", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{
			result: runner.Result{
				Attempted:      runner.StageDeploy,
				Completed:      runner.StageRender,
				Owner:          confirmedOwner(),
				Publication:    moduleowner.OutcomeConfirmed,
				ApplyCompleted: true,
				WaitRequested:  true,
				WaitCompleted:  false,
			},
			err: fmt.Errorf("waiting: %w", context.DeadlineExceeded),
		}

		stdout, _, err := executeRun(t.Context(), t, recorder.run,
			append(slicesClone(runFlags), "--wait", "--output", "json")...)
		g.Expect(exitCode(err)).To(Equal(ExitFailure))

		g.Expect(stdout).To(MatchJSON(`{
			"definition": "definition.yaml",
			"status": "failed",
			"owner": {
				"name": "synthetic",
				"uid": "definition-uid",
				"apiVersion": "config.opendatahub.io/v1alpha1",
				"kind": "PlatformModuleDefinition"
			},
			"publication": "confirmed",
			"attemptedStage": "deploy",
			"completedStage": "render",
			"applyCompleted": true,
			"wait": {
				"requested": true,
				"completed": false
			},
			"failure": {
				"category": "cancelled",
				"message": "waiting: context deadline exceeded"
			}
		}`))
	})
}

func TestRunCommandFlagFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		flags []string
	}{
		{"missing config", []string{"--platform-version", "2.19.0"}},
		{"missing platform version", []string{"--config", "definition.yaml"}},
		{
			"invalid output format",
			append(slicesClone(runFlags), "--output", "yaml"),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			recorder := &recordingRun{}

			stdout, _, err := executeRun(t.Context(), t, recorder.run, testCase.flags...)
			g.Expect(err).To(HaveOccurred())
			g.Expect(exitCode(err)).To(Equal(ExitUsage))
			g.Expect(recorder.calls).To(Equal(0))
			g.Expect(stdout).To(BeEmpty())
		})
	}
}

func TestRunCommandForwardsInvalidWaitTimeout(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	// A negative enabled wait budget is forwarded unchanged: the
	// runner rejects it as an options error, never an API error.
	_, _, err := executeRun(t.Context(), t, recorder.run,
		append(slicesClone(runFlags), "--wait", "--wait-timeout", "-5s")...)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(recorder.options[0].Wait).To(BeTrue())
	g.Expect(recorder.options[0].WaitTimeout).To(Equal(-5 * time.Second))
}

func TestRunCommandStatelessAcrossExecutions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	command := NewRunCommand(recorder.run, genericclioptions.NewConfigFlags(true))

	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&bytes.Buffer{})

	command.SetArgs(slicesClone(runFlags))
	g.Expect(command.ExecuteContext(t.Context())).NotTo(HaveOccurred())

	command.SetArgs(append(slicesClone(runFlags), "--output", FormatJSON))
	g.Expect(command.ExecuteContext(t.Context())).NotTo(HaveOccurred())

	// The second execution sees only its own flag values: no state
	// leaks from the first run into options or output format.
	g.Expect(recorder.calls).To(Equal(2))
	g.Expect(recorder.options[0].Wait).To(BeFalse())
	g.Expect(recorder.options[1].PlatformVersion).To(Equal("2.19.0"))
	g.Expect(stdout.String()).To(ContainSubstring("module run definition.yaml: applied"))
	g.Expect(stdout.String()).To(ContainSubstring(`"definition":"definition.yaml"`))
}

func TestRunCommandWriteFailuresExitAsExecutionFailures(t *testing.T) {
	t.Parallel()

	t.Run("report write failure preserves cancellation identity", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{
			err: fmt.Errorf("apply: %w", context.Canceled),
		}

		command := NewRunCommand(recorder.run, genericclioptions.NewConfigFlags(true))
		command.SetOut(failingWriter{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(slicesClone(runFlags))

		err := command.ExecuteContext(t.Context())

		g.Expect(exitCode(err)).To(Equal(ExitFailure))
		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		g.Expect(err.Error()).To(ContainSubstring("write report"))
	})

	t.Run("summary write failure on a failed action", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{err: errors.New("apply failure")}

		command := NewRunCommand(recorder.run, genericclioptions.NewConfigFlags(true))
		command.SetOut(&bytes.Buffer{})
		command.SetErr(failingWriter{})
		command.SetArgs(slicesClone(runFlags))

		err := command.ExecuteContext(t.Context())

		g.Expect(exitCode(err)).To(Equal(ExitFailure))
		g.Expect(err.Error()).To(ContainSubstring("write failure summary"))
	})
}

// slicesClone returns one copy of the flag arguments, so executions
// never share or mutate the shared minimal flag slice.
func slicesClone(flags []string) []string {
	return append([]string(nil), flags...)
}
