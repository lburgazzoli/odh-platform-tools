package module

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/generate"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/generator"

	. "github.com/onsi/gomega"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	rest "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/deploy"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/validate"
	deployer "github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/validator"
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

// recordingModuleValidate is a fake validate action boundary recording
// every invocation, including the getter it received.
type recordingModuleValidate struct {
	calls   int
	getters []genericclioptions.RESTClientGetter
	options []validator.Options
}

// validate records one invocation and returns a minimal passing
// result.
func (r *recordingModuleValidate) validate(
	_ context.Context,
	getter genericclioptions.RESTClientGetter,
	options validator.Options,
) (validator.Result, error) {
	r.calls++
	r.getters = append(r.getters, getter)
	r.options = append(r.options, options)

	return validator.Result{
		Checks: []validator.CheckResult{
			{ID: validator.CheckModuleAPI, Status: validator.StatusPass},
		},
	}, nil
}

// execute runs one module group execution with captured streams.
func execute(
	ctx context.Context,
	t *testing.T,
	lintRun lint.Run,
	deployRun deploy.Run,
	generateRun generate.Run,
	validateRun validate.Run,
	getter genericclioptions.RESTClientGetter,
	args ...string,
) (string, error) {
	t.Helper()

	group := NewModuleCommand(lintRun, deployRun, nil, generateRun, validateRun, getter)

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

	stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil,
		genericclioptions.NewConfigFlags(true))

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(ContainSubstring("lint"))
	g.Expect(stdout).To(ContainSubstring("deploy"))
	g.Expect(stdout).To(ContainSubstring("generate"))
	g.Expect(stdout).To(ContainSubstring("mock"))
	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(runRecorder.calls).To(Equal(0))
}

func TestModuleCommandRejectsUnknownCommands(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil,
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

	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil,
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

	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil, getter,
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

// recordingModuleGenerate is a fake generate action boundary recording
// every invocation.
type recordingModuleGenerate struct {
	calls   int
	options []generator.Options
}

// generate records one invocation and returns a stdout result.
func (r *recordingModuleGenerate) generate(
	_ context.Context,
	options generator.Options,
) (generator.Result, error) {
	r.calls++
	r.options = append(r.options, options)

	return generator.Result{YAML: []byte("kind: x\n"), Output: "stdout"}, nil
}

// inaccessibleGetter fails the access paths a cluster-bound command
// would take, proving generate never touches them.
type inaccessibleGetter struct {
	*genericclioptions.ConfigFlags
}

// ToRESTConfig reports access instead of a configuration.
func (inaccessibleGetter) ToRESTConfig() (*rest.Config, error) {
	return nil, errors.New("the getter must not be accessed")
}

// ToRawKubeConfigLoader reports access instead of a loader.
func (inaccessibleGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return failingClientConfig{}
}

// failingClientConfig fails every raw-config access.
type failingClientConfig struct{}

// RawConfig reports access instead of a configuration.
func (failingClientConfig) RawConfig() (clientcmdapi.Config, error) {
	return clientcmdapi.Config{}, errors.New("the getter must not be accessed")
}

// ClientConfig reports access instead of a configuration.
func (failingClientConfig) ClientConfig() (*rest.Config, error) {
	return nil, errors.New("the getter must not be accessed")
}

// Namespace reports access instead of a namespace.
func (failingClientConfig) Namespace() (string, bool, error) {
	return "", false, errors.New("the getter must not be accessed")
}

// ConfigAccess reports access instead of an accessor.
func (failingClientConfig) ConfigAccess() clientcmd.ConfigAccess {
	return nil
}

// TestModuleCommandRunsGenerate proves the group forwards the source
// argument to the generate action, which runs cluster-independently
// over a getter that fails on access.
func TestModuleCommandRunsGenerate(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	deployRecorder := &recordingModuleDeploy{}
	generateRecorder := &recordingModuleGenerate{}

	getter := inaccessibleGetter{ConfigFlags: genericclioptions.NewConfigFlags(true)}

	stdout, err := execute(t.Context(), t, lintRecorder.run, deployRecorder.deploy,
		generateRecorder.generate, nil, getter,
		"generate", "source-dir")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(Equal("kind: x\n"))
	g.Expect(generateRecorder.calls).To(Equal(1))
	g.Expect(generateRecorder.options).To(Equal([]generator.Options{{SourceDir: "source-dir"}}))
	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(deployRecorder.calls).To(Equal(0))
}

// TestModuleCommandGenerateHelpListsCommand proves the group help
// documents generate and its help lists the command without running
// any action or touching the getter.
func TestModuleCommandGenerateHelpListsCommand(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	deployRecorder := &recordingModuleDeploy{}
	generateRecorder := &recordingModuleGenerate{}

	getter := inaccessibleGetter{ConfigFlags: genericclioptions.NewConfigFlags(true)}

	groupStdout, err := execute(t.Context(), t, lintRecorder.run, deployRecorder.deploy,
		generateRecorder.generate, nil, getter)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(groupStdout).To(ContainSubstring("generate"))

	helpStdout, err := execute(t.Context(), t, lintRecorder.run, deployRecorder.deploy,
		generateRecorder.generate, nil, getter,
		"generate", "--help")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(helpStdout).To(ContainSubstring("--values-file"))

	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(deployRecorder.calls).To(Equal(0))
	g.Expect(generateRecorder.calls).To(Equal(0))
}

// TestModuleCommandRunsValidate proves the group forwards the standard
// getter and flags to the injected validate action and reports its
// result.
func TestModuleCommandRunsValidate(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	deployRecorder := &recordingModuleDeploy{}
	validateRecorder := &recordingModuleValidate{}

	getter := genericclioptions.NewConfigFlags(true)

	stdout, err := execute(t.Context(), t, lintRecorder.run, deployRecorder.deploy,
		nil, validateRecorder.validate, getter,
		"validate", "--config", "definition.yaml", "--timeout", "15s")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(ContainSubstring("module validate definition.yaml: pass"))
	g.Expect(validateRecorder.calls).To(Equal(1))
	g.Expect(validateRecorder.options).To(HaveLen(1))
	g.Expect(validateRecorder.options[0].DefinitionFile).To(Equal("definition.yaml"))
	g.Expect(validateRecorder.options[0].Timeout).To(Equal(15 * time.Second))
	g.Expect(validateRecorder.getters[0]).To(Equal(getter))
	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(deployRecorder.calls).To(Equal(0))
}

// TestModuleCommandValidateHelpListsCommand proves the group help
// documents validate and its help works without any action or getter
// access.
func TestModuleCommandValidateHelpListsCommand(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	deployRecorder := &recordingModuleDeploy{}
	validateRecorder := &recordingModuleValidate{}

	getter := inaccessibleGetter{ConfigFlags: genericclioptions.NewConfigFlags(true)}

	groupStdout, err := execute(t.Context(), t, lintRecorder.run, deployRecorder.deploy,
		nil, validateRecorder.validate, getter)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(groupStdout).To(ContainSubstring("validate"))

	helpStdout, err := execute(t.Context(), t, lintRecorder.run, deployRecorder.deploy,
		nil, validateRecorder.validate, getter,
		"validate", "--help")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(helpStdout).To(ContainSubstring("--timeout"))

	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(deployRecorder.calls).To(Equal(0))
	g.Expect(validateRecorder.calls).To(Equal(0))
}
