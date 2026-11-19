package validate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/validator"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/exit"
)

// recordingValidate is a fake validate action boundary recording every
// invocation.
type recordingValidate struct {
	calls   int
	context []context.Context
	getters []genericclioptions.RESTClientGetter
	options []validator.Options

	result validator.Result
	err    error
}

// validate records one invocation and returns the scripted outcome.
func (r *recordingValidate) validate(
	ctx context.Context,
	getter genericclioptions.RESTClientGetter,
	options validator.Options,
) (validator.Result, error) {
	r.calls++
	r.context = append(r.context, ctx)
	r.getters = append(r.getters, getter)
	r.options = append(r.options, options)

	return r.result, r.err
}

// passingResult is the smallest result that passes every check.
func passingResult() validator.Result {
	return validator.Result{
		Namespace: "run-namespace",
		Checks: []validator.CheckResult{
			{ID: validator.CheckModuleAPI, Status: validator.StatusPass},
		},
	}
}

// failingResult carries one observed finding without an execution
// failure.
func failingResult() validator.Result {
	return validator.Result{
		Namespace: "run-namespace",
		Checks: []validator.CheckResult{
			{
				ID:      validator.CheckModuleExists,
				Status:  validator.StatusFail,
				Field:   "status.conditions",
				Message: "condition is missing",
			},
		},
	}
}

// kubeconfigFor returns standard ConfigFlags bound to a standalone
// kubeconfig whose context namespace is run-namespace and whose server
// is serverURL. Command tests never contact it: the fake action does
// no cluster access.
func kubeconfigFor(t *testing.T, serverURL string) *genericclioptions.ConfigFlags {
	t.Helper()

	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test-cluster
  cluster:
    server: %s
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
    namespace: run-namespace
current-context: test-context
users:
- name: test-user
  user: {}
`, serverURL)

	return getterFromKubeconfig(t, kubeconfig, "")
}

// getterFromKubeconfig binds ConfigFlags to one kubeconfig document,
// optionally with the standard explicit namespace override.
func getterFromKubeconfig(t *testing.T, kubeconfig, explicitNamespace string) *genericclioptions.ConfigFlags {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}

	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path

	if explicitNamespace != "" {
		namespace := explicitNamespace
		flags.Namespace = &namespace
	}

	return flags
}

// executeValidate runs one validate command execution with captured
// streams.
func executeValidate(
	ctx context.Context,
	t *testing.T,
	action Run,
	getter genericclioptions.RESTClientGetter,
	args ...string,
) (string, string, error) {
	t.Helper()

	command := NewValidateCommand(action, getter)

	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)

	err := command.ExecuteContext(ctx)

	return stdout.String(), stderr.String(), err
}

// exitCodeOf extracts the classified process exit status.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()

	var coder *exit.Error
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}

	t.Fatalf("expected an exit.Error, got %v", err)

	return -1
}

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		category string
		code     int
	}{
		{name: "success", err: nil, category: "", code: ExitSuccess},
		{
			name:     "deadline",
			err:      context.DeadlineExceeded,
			category: FailureTimeout,
			code:     ExitFailure,
		},
		{
			name:     "cancellation",
			err:      context.Canceled,
			category: FailureCancelled,
			code:     ExitFailure,
		},
		{
			name: "invalid options",
			err: moduleerrors.New(
				moduleerrors.ErrInvalidValidateOptions, "invalid validate options"),
			category: FailureInvalidOptions,
			code:     ExitUsage,
		},
		{
			name: "definition",
			err: moduleerrors.New(
				moduleerrors.ErrInvalidDefinition, "invalid module definition"),
			category: FailureDefinition,
			code:     ExitUsage,
		},
		{
			name: "runtime",
			err: moduleerrors.New(
				moduleerrors.ErrValidateRuntime, "observation failed"),
			category: FailureRuntime,
			code:     ExitFailure,
		},
		{
			name:     "unclassified",
			err:      errors.New("plain failure"),
			category: FailureRuntime,
			code:     ExitFailure,
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			category, code := classify(test.err)

			g.Expect(category).To(Equal(test.category))
			g.Expect(code).To(Equal(test.code))
		})
	}
}

// TestClassifyContextIdentityPrecedence checks that cancellation and
// deadline identity wins over every option, definition, or runtime
// category, even when the same error also carries one.
func TestClassifyContextIdentityPrecedence(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	deadline := moduleerrors.New(
		moduleerrors.ErrInvalidValidateOptions, "invalid validate options",
		moduleerrors.WithCause(context.DeadlineExceeded))

	category, code := classify(deadline)

	g.Expect(category).To(Equal(FailureTimeout))
	g.Expect(code).To(Equal(ExitFailure))

	cancelled := moduleerrors.New(
		moduleerrors.ErrValidateRuntime, "observation failed",
		moduleerrors.WithCause(context.Canceled))

	category, code = classify(cancelled)

	g.Expect(category).To(Equal(FailureCancelled))
	g.Expect(code).To(Equal(ExitFailure))
}

// TestValidateCommandDefaultsAndFlagSurface checks the flag surface
// and defaults without executing anything.
func TestValidateCommandDefaultsAndFlagSurface(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	command := NewValidateCommand((&recordingValidate{}).validate,
		genericclioptions.NewConfigFlags(true))

	flags := command.Flags()

	g.Expect(flags.Lookup("config")).NotTo(BeNil())
	g.Expect(flags.Lookup("timeout").DefValue).To(Equal("30s"))
	g.Expect(flags.Lookup("output").DefValue).To(Equal(FormatText))

	// Waiting is automatic; there is no wait toggle, separate wait
	// budget, or platform-version input.
	for _, absent := range []string{"wait", "wait-timeout", "platform-version", "namespace"} {
		g.Expect(flags.Lookup(absent)).To(BeNil(), "flag %s must not exist", absent)
	}
}

// TestValidateCommandHelpIsOffline checks help needs no action,
// definition, or cluster access.
func TestValidateCommandHelpIsOffline(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingValidate{}

	stdout, _, err := executeValidate(t.Context(), t, recorder.validate,
		genericclioptions.NewConfigFlags(true), "--help")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(ContainSubstring("validate"))
	g.Expect(stdout).To(ContainSubstring("platform contract"))
	g.Expect(stdout).To(ContainSubstring("--timeout"))
	g.Expect(recorder.calls).To(Equal(0))
}

func TestValidateCommandPassesAllChecks(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	getter := kubeconfigFor(t, "https://127.0.0.1:6443")
	recorder := &recordingValidate{result: passingResult()}

	stdout, stderr, err := executeValidate(t.Context(), t, recorder.validate, getter,
		"--config", "definition.yaml", "--timeout", "45s")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stderr).To(BeEmpty())
	g.Expect(stdout).To(ContainSubstring("module validate definition.yaml: pass"))
	g.Expect(stdout).To(ContainSubstring("MODULE_API: pass"))

	g.Expect(recorder.calls).To(Equal(1))
	g.Expect(recorder.options).To(HaveLen(1))
	g.Expect(recorder.options[0].DefinitionFile).To(Equal("definition.yaml"))
	g.Expect(recorder.options[0].Timeout).To(Equal(45 * time.Second))
	g.Expect(recorder.getters[0]).To(Equal(getter))
	g.Expect(recorder.context[0]).NotTo(BeNil())
}

// TestValidateCommandFindingsWithoutError checks that observed
// findings fail the invocation with exit 1 and stay in the report:
// no stderr summary is printed for ordinary findings.
func TestValidateCommandFindingsWithoutError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingValidate{result: failingResult()}

	stdout, stderr, err := executeValidate(t.Context(), t, recorder.validate,
		kubeconfigFor(t, "https://127.0.0.1:6443"),
		"--config", "definition.yaml")

	g.Expect(err).To(HaveOccurred())
	g.Expect(exitCodeOf(t, err)).To(Equal(ExitFailure))
	g.Expect(stderr).To(BeEmpty())
	g.Expect(stdout).To(ContainSubstring("module validate definition.yaml: failed"))
	g.Expect(stdout).To(ContainSubstring("MODULE_EXISTS: fail (status.conditions)"))
}

// TestValidateCommandFailureCategories checks the classified outcome
// of every execution failure category: exit code, stderr summary, and
// the failure line of the text report.
func TestValidateCommandFailureCategories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		category string
		code     int
	}{
		{
			name:     "deadline",
			err:      context.DeadlineExceeded,
			category: FailureTimeout,
			code:     ExitFailure,
		},
		{
			name:     "cancellation",
			err:      context.Canceled,
			category: FailureCancelled,
			code:     ExitFailure,
		},
		{
			name: "runtime",
			err: moduleerrors.New(
				moduleerrors.ErrValidateRuntime, "observation failed"),
			category: FailureRuntime,
			code:     ExitFailure,
		},
		{
			name: "definition",
			err: moduleerrors.New(
				moduleerrors.ErrInvalidDefinition, "invalid module definition"),
			category: FailureDefinition,
			code:     ExitUsage,
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			recorder := &recordingValidate{result: failingResult(), err: test.err}

			stdout, stderr, err := executeValidate(t.Context(), t, recorder.validate,
				kubeconfigFor(t, "https://127.0.0.1:6443"),
				"--config", "definition.yaml")

			g.Expect(err).To(HaveOccurred())
			g.Expect(exitCodeOf(t, err)).To(Equal(test.code))
			g.Expect(stderr).To(ContainSubstring("module validate definition.yaml: " + test.category))
			g.Expect(stdout).To(ContainSubstring("module validate definition.yaml: failed"))
			g.Expect(stdout).To(ContainSubstring(test.category + ": "))

			// The retained checks stay in the report.
			g.Expect(stdout).To(ContainSubstring("MODULE_EXISTS: fail"))
		})
	}
}

// TestValidateCommandBlankConfig checks that an explicitly blank
// --config is invalid input that never reaches the action.
func TestValidateCommandBlankConfig(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingValidate{result: passingResult()}

	stdout, _, err := executeValidate(t.Context(), t, recorder.validate,
		kubeconfigFor(t, "https://127.0.0.1:6443"),
		"--config", "")

	g.Expect(err).To(HaveOccurred())
	g.Expect(exitCodeOf(t, err)).To(Equal(ExitUsage))
	g.Expect(recorder.calls).To(Equal(0))
	g.Expect(stdout).To(ContainSubstring(FailureInvalidOptions))
}

// TestValidateCommandNonPositiveTimeout checks that zero and negative
// budgets are invalid input.
func TestValidateCommandNonPositiveTimeout(t *testing.T) {
	t.Parallel()

	for _, timeout := range []string{"0", "-5s"} {
		t.Run("timeout "+timeout, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			recorder := &recordingValidate{result: passingResult()}

			stdout, _, err := executeValidate(t.Context(), t, recorder.validate,
				kubeconfigFor(t, "https://127.0.0.1:6443"),
				"--config", "definition.yaml", "--timeout", timeout)

			g.Expect(err).To(HaveOccurred())
			g.Expect(exitCodeOf(t, err)).To(Equal(ExitUsage))
			g.Expect(recorder.calls).To(Equal(0))
			g.Expect(stdout).To(ContainSubstring(FailureInvalidOptions))
		})
	}
}

// TestValidateCommandInvalidOutputFormat checks flag-time rejection of
// unsupported formats.
func TestValidateCommandInvalidOutputFormat(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingValidate{result: passingResult()}

	_, _, err := executeValidate(t.Context(), t, recorder.validate,
		kubeconfigFor(t, "https://127.0.0.1:6443"),
		"--config", "definition.yaml", "--output", "yaml")

	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("output must be"))
	g.Expect(recorder.calls).To(Equal(0))
}

// TestValidateCommandMissingConfigFlag checks Cobra's required-flag
// handling stays a usage failure.
func TestValidateCommandMissingConfigFlag(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingValidate{result: passingResult()}

	_, _, err := executeValidate(t.Context(), t, recorder.validate,
		genericclioptions.NewConfigFlags(true))

	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("required"))
	g.Expect(recorder.calls).To(Equal(0))
}

// TestValidateCommandRejectsPositionalArgs checks the no-args rule.
func TestValidateCommandRejectsPositionalArgs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingValidate{result: passingResult()}

	_, _, err := executeValidate(t.Context(), t, recorder.validate,
		genericclioptions.NewConfigFlags(true),
		"extra")

	g.Expect(err).To(HaveOccurred())
	// Positional arguments are rejected as unknown, never stubbed.
	g.Expect(err.Error()).To(ContainSubstring("unknown command"))
	g.Expect(recorder.calls).To(Equal(0))
}

// TestValidateCommandJSONOutput checks the JSON report is one
// newline-terminated document on stdout.
func TestValidateCommandJSONOutput(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingValidate{result: passingResult()}

	stdout, stderr, err := executeValidate(t.Context(), t, recorder.validate,
		kubeconfigFor(t, "https://127.0.0.1:6443"),
		"--config", "definition.yaml", "--output", "json")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stderr).To(BeEmpty())
	g.Expect(stdout).To(ContainSubstring(`"status":"pass"`))
	g.Expect(stdout).To(ContainSubstring(`"namespace":"run-namespace"`))
	g.Expect(bytes.Count([]byte(stdout), []byte("\n"))).To(Equal(1))
}

// TestValidateCommandWriteFailureExitsOne checks that a failing
// stdout preserves the action error cause and exits 1.
func TestValidateCommandWriteFailureExitsOne(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	actionErr := moduleerrors.New(moduleerrors.ErrValidateRuntime, "observation failed")
	recorder := &recordingValidate{result: failingResult(), err: actionErr}

	command := NewValidateCommand(recorder.validate, kubeconfigFor(t, "https://127.0.0.1:6443"))
	command.SetOut(failingWriter{err: errors.New("disk full")})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--config", "definition.yaml"})

	err := command.ExecuteContext(t.Context())

	g.Expect(err).To(HaveOccurred())
	g.Expect(exitCodeOf(t, err)).To(Equal(ExitFailure))
	g.Expect(errors.Is(err, moduleerrors.ErrValidateRuntime)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("write report"))
}

// TestValidateCommandStderrWriteFailureExitsOne checks the stderr
// summary failure path.
func TestValidateCommandStderrWriteFailureExitsOne(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	actionErr := moduleerrors.New(moduleerrors.ErrValidateRuntime, "observation failed")
	recorder := &recordingValidate{result: failingResult(), err: actionErr}

	command := NewValidateCommand(recorder.validate, kubeconfigFor(t, "https://127.0.0.1:6443"))
	command.SetOut(&bytes.Buffer{})
	command.SetErr(failingWriter{err: errors.New("disk full")})
	command.SetArgs([]string{"--config", "definition.yaml"})

	err := command.ExecuteContext(t.Context())

	g.Expect(err).To(HaveOccurred())
	g.Expect(exitCodeOf(t, err)).To(Equal(ExitFailure))
	g.Expect(errors.Is(err, moduleerrors.ErrValidateRuntime)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("write failure summary"))
}
