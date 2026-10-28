package module

import (
	"bytes"
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/deploy"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	deployer "github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
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

// recordingModuleDeploy is a fake deploy action boundary recording every
// invocation, including the getter it received.
type recordingModuleDeploy struct {
	calls   int
	getters []genericclioptions.RESTClientGetter
	options []deployer.Options
}

// deploy records one invocation and returns a successful empty result.
func (r *recordingModuleDeploy) deploy(
	_ context.Context,
	getter genericclioptions.RESTClientGetter,
	options deployer.Options,
) (deployer.Result, error) {
	r.calls++
	r.getters = append(r.getters, getter)
	r.options = append(r.options, options)

	return deployer.Result{}, nil
}

// execute runs one module group execution with captured streams.
func execute(
	ctx context.Context,
	t *testing.T,
	lintRun lint.Run,
	deployAction deploy.Deploy,
	getter genericclioptions.RESTClientGetter,
	args ...string,
) (string, error) {
	t.Helper()

	group := NewModuleCommand(lintRun, deployAction, getter)

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
	runRecorder := &recordingModuleDeploy{}

	stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy,
		genericclioptions.NewConfigFlags(true))

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(ContainSubstring("lint"))
	g.Expect(stdout).To(ContainSubstring("deploy"))
	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(runRecorder.calls).To(Equal(0))
}

func TestModuleCommandRejectsUnknownCommands(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy,
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
	runRecorder := &recordingModuleDeploy{}

	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy,
		genericclioptions.NewConfigFlags(true),
		"lint", "--config", "definition.yaml")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(lintRecorder.calls).To(Equal(1))
	g.Expect(lintRecorder.options).To(HaveLen(1))
	g.Expect(lintRecorder.options[0].DefinitionFile).To(Equal("definition.yaml"))
}

func TestModuleCommandRunsDeploy(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	getter := genericclioptions.NewConfigFlags(true)
	// The standard namespace flag is registered once at the root, so
	// the group-level test sets the standard override on the getter
	// directly; the deploy resolves it into its options.
	runNamespace := "run-namespace"
	getter.Namespace = &runNamespace

	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, getter,
		"deploy",
		"--config", "definition.yaml",
		"--platform-version", "2.19.0")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(runRecorder.calls).To(Equal(1))
	g.Expect(runRecorder.options).To(HaveLen(1))
	g.Expect(runRecorder.options[0].DefinitionFile).To(Equal("definition.yaml"))
	g.Expect(runRecorder.options[0].PlatformVersion).To(Equal("2.19.0"))
	g.Expect(runRecorder.options[0].Namespace).To(Equal("run-namespace"))

	// The group forwards the root's one standard getter.
	g.Expect(runRecorder.getters).To(HaveLen(1))
	g.Expect(runRecorder.getters[0]).To(BeIdenticalTo(getter))
}
