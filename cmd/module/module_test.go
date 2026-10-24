package module

import (
	"bytes"
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/run"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner"
)

// recordingRun is a fake lint action boundary recording every
// invocation.
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

// recordingModuleRun is a fake run action boundary recording every
// invocation, including the getter it received.
type recordingModuleRun struct {
	calls   int
	getters []genericclioptions.RESTClientGetter
	options []runner.Options
}

// run records one invocation and returns a successful empty result.
func (r *recordingModuleRun) run(
	_ context.Context,
	getter genericclioptions.RESTClientGetter,
	options runner.Options,
) (runner.Result, error) {
	r.calls++
	r.getters = append(r.getters, getter)
	r.options = append(r.options, options)

	return runner.Result{}, nil
}

// execute runs one module group execution with captured streams.
func execute(
	ctx context.Context,
	t *testing.T,
	lintRun lint.Run,
	runRun run.Run,
	getter genericclioptions.RESTClientGetter,
	args ...string,
) (string, error) {
	t.Helper()

	group := NewModuleCommand(lintRun, runRun, getter)

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

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleRun{}

	stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run,
		genericclioptions.NewConfigFlags(true))

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(ContainSubstring("lint"))
	g.Expect(stdout).To(ContainSubstring("run"))
	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(runRecorder.calls).To(Equal(0))
}

func TestModuleCommandRejectsUnknownCommands(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleRun{}

	stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run,
		genericclioptions.NewConfigFlags(true),
		"frobnicate", "--config", "definition.yaml")

	g.Expect(err).To(HaveOccurred())
	// The unknown command is rejected, never stubbed.
	g.Expect(err.Error()).To(ContainSubstring("unknown"))
	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(runRecorder.calls).To(Equal(0))
	g.Expect(stdout).To(BeEmpty())
}

func TestModuleCommandRunsLint(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleRun{}

	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run,
		genericclioptions.NewConfigFlags(true),
		"lint", "--config", "definition.yaml")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(lintRecorder.calls).To(Equal(1))
	g.Expect(lintRecorder.options).To(HaveLen(1))
	g.Expect(lintRecorder.options[0].DefinitionFile).To(Equal("definition.yaml"))
}

func TestModuleCommandRunsRun(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleRun{}

	getter := genericclioptions.NewConfigFlags(true)

	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run, getter,
		"run",
		"--config", "definition.yaml",
		"--platform-version", "2.19.0")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(runRecorder.calls).To(Equal(1))
	g.Expect(runRecorder.options).To(HaveLen(1))
	g.Expect(runRecorder.options[0].DefinitionFile).To(Equal("definition.yaml"))
	g.Expect(runRecorder.options[0].PlatformVersion).To(Equal("2.19.0"))

	// The group forwards the root's one standard getter.
	g.Expect(runRecorder.getters).To(HaveLen(1))
	g.Expect(runRecorder.getters[0]).To(BeIdenticalTo(getter))
}
