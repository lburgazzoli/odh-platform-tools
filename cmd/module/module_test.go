package module

import (
	"bytes"
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// recordingRun is a fake action boundary recording every invocation.
type recordingRun struct {
	calls   int
	options []linter.Options
}

// run records one invocation and returns no outcome.
func (r *recordingRun) run(_ context.Context, options linter.Options) (linter.Result, error) {
	r.calls++
	r.options = append(r.options, options)

	return linter.Result{}, nil
}

// execute runs one module group execution with captured streams.
func execute(
	ctx context.Context,
	t *testing.T,
	run lint.Run,
	args ...string,
) (string, error) {
	t.Helper()

	group := NewModuleCommand(run)

	var stdout, stderr bytes.Buffer
	group.SetOut(&stdout)
	group.SetErr(&stderr)
	group.SetArgs(args)

	err := group.ExecuteContext(ctx)

	return stdout.String(), err
}

func TestModuleCommandShowsHelp(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	stdout, err := execute(t.Context(), t, recorder.run)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(ContainSubstring("Usage:"))
	g.Expect(stdout).To(ContainSubstring("lint"))
	g.Expect(recorder.calls).To(Equal(0))
}

func TestModuleCommandRejectsDeferredCommands(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	stdout, err := execute(t.Context(), t, recorder.run,
		"run", "--config", "definition.yaml")

	g.Expect(err).To(HaveOccurred())
	// The deferred command is rejected, never stubbed.
	g.Expect(err.Error()).To(ContainSubstring("unknown"))
	g.Expect(recorder.calls).To(Equal(0))
	g.Expect(stdout).To(BeEmpty())
}

func TestModuleCommandRunsLint(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	_, err := execute(t.Context(), t, recorder.run,
		"lint", "--config", "definition.yaml")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(recorder.calls).To(Equal(1))
	g.Expect(recorder.options).To(HaveLen(1))
	g.Expect(recorder.options[0].DefinitionFile).To(Equal("definition.yaml"))
}
