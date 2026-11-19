package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	deploycli "github.com/lburgazzoli/odh-platform-tools/cmd/module/deploy"
	generatecli "github.com/lburgazzoli/odh-platform-tools/cmd/module/generate"
	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	validatecli "github.com/lburgazzoli/odh-platform-tools/cmd/module/validate"
	deployer "github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/generator"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	modmock "github.com/lburgazzoli/odh-platform-tools/pkg/module/mock"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/validator"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/exit"
)

// rootKubeconfigYAML is a standalone kubeconfig whose context carries
// an explicit namespace, pointing at an unreachable server: flag
// forwarding and namespace resolution are local and never contact it.
const rootKubeconfigYAML = `apiVersion: v1
kind: Config
clusters:
- name: test-cluster
  cluster:
    server: https://127.0.0.1:6443
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
`

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
// invocation, the context it received, and the resolved options.
type recordingModuleDeploy struct {
	calls    int
	contexts []context.Context
	getters  []genericclioptions.RESTClientGetter
	options  []deployer.Options
}

// deploy records one invocation and returns a successful empty result.
func (r *recordingModuleDeploy) deploy(
	ctx context.Context,
	getter genericclioptions.RESTClientGetter,
	options deployer.Options,
) (deployer.Result, error) {
	r.calls++
	r.contexts = append(r.contexts, ctx)
	r.getters = append(r.getters, getter)
	r.options = append(r.options, options)

	return deployer.Result{}, nil
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

// recordingModuleValidate is a fake validate action boundary recording
// every invocation, the context it received, and the resolved options.
type recordingModuleValidate struct {
	calls    int
	contexts []context.Context
	options  []validator.Options
}

// validate records one invocation and returns a minimal passing
// result.
func (r *recordingModuleValidate) validate(
	ctx context.Context,
	_ genericclioptions.RESTClientGetter,
	options validator.Options,
) (validator.Result, error) {
	r.calls++
	r.contexts = append(r.contexts, ctx)
	r.options = append(r.options, options)

	return validator.Result{
		Checks: []validator.CheckResult{
			{ID: validator.CheckModuleAPI, Status: validator.StatusPass},
		},
	}, nil
}

// execute runs one root command execution with captured streams.
func execute(
	ctx context.Context,
	t *testing.T,
	lintRun lint.Run,
	deployRun deploycli.Run,
	generateRun generatecli.Run,
	validateRun validatecli.Run,
	args ...string,
) (string, error) {
	t.Helper()

	root := NewRootCommand(lintRun, deployRun, nil, generateRun, validateRun)

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	err := root.ExecuteContext(ctx)

	return stdout.String(), err
}

// kubeconfigFile writes the standalone kubeconfig fixture and returns
// its path.
func kubeconfigFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(rootKubeconfigYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestRootCommandHelp(t *testing.T) {
	t.Parallel()

	t.Run("root help", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		lintRecorder := &recordingRun{}
		runRecorder := &recordingModuleDeploy{}

		stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil, "--help")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(stdout).To(ContainSubstring("OpenDataHub platform module tooling"))
		g.Expect(stdout).To(ContainSubstring("\n  module "))
		g.Expect(stdout).To(ContainSubstring("--debug"))
		g.Expect(lintRecorder.calls).To(Equal(0))
		g.Expect(runRecorder.calls).To(Equal(0))
	})

	t.Run("lint help", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		lintRecorder := &recordingRun{}
		runRecorder := &recordingModuleDeploy{}

		stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil,
			"module", "lint", "--help")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(stdout).To(ContainSubstring("--config"))
		g.Expect(stdout).To(ContainSubstring("--output"))
		g.Expect(lintRecorder.calls).To(Equal(0))
	})

	t.Run("run help", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		lintRecorder := &recordingRun{}
		runRecorder := &recordingModuleDeploy{}

		stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil,
			"module", "deploy", "--help")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(stdout).To(ContainSubstring("--config"))
		g.Expect(stdout).To(ContainSubstring("--platform-version"))
		g.Expect(stdout).To(ContainSubstring("--related-images-file"))
		g.Expect(stdout).To(ContainSubstring("--timeout"))
		g.Expect(stdout).To(ContainSubstring("--wait"))
		g.Expect(stdout).To(ContainSubstring("--wait-timeout"))
		g.Expect(stdout).To(ContainSubstring("--output"))
		g.Expect(lintRecorder.calls).To(Equal(0))
		g.Expect(runRecorder.calls).To(Equal(0))
	})

	t.Run("generate help", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		lintRecorder := &recordingRun{}
		runRecorder := &recordingModuleDeploy{}
		generateRecorder := &recordingModuleGenerate{}

		stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy,
			generateRecorder.generate, nil,
			"module", "generate", "--help")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(stdout).To(ContainSubstring("--name"))
		g.Expect(stdout).To(ContainSubstring("--init-container"))
		g.Expect(stdout).To(ContainSubstring("--values-file"))
		g.Expect(stdout).To(ContainSubstring("--output"))
		g.Expect(lintRecorder.calls).To(Equal(0))
		g.Expect(runRecorder.calls).To(Equal(0))
		g.Expect(generateRecorder.calls).To(Equal(0))
	})

	t.Run("mock help stays offline", func(t *testing.T) {
		g := NewWithT(t)

		stdout, err := execute(t.Context(), t, nil, nil, nil, nil,
			"module", "mock", "--kubeconfig", filepath.Join(t.TempDir(), "missing"), "--help")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(stdout).To(ContainSubstring("--api-version"))
		g.Expect(stdout).To(ContainSubstring("--kind"))
		g.Expect(stdout).To(ContainSubstring("--platform-configmap"))
	})
}

func TestRootMockForwardsConnectionAndClassifiesActionFailures(t *testing.T) {
	for _, scenario := range []struct {
		name string
		err  error
		code int
	}{
		{name: "foreground completes", code: 0},
		{name: "cancellation", err: context.Canceled, code: 1},
		{name: "runtime error", err: errors.New("manager startup failed"), code: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			g := NewWithT(t)
			ctx := t.Context()
			closeContext := func(actionCtx context.Context, getter genericclioptions.RESTClientGetter, options modmock.Options) error {
				g.Expect(actionCtx).To(BeIdenticalTo(ctx))
				g.Expect(options).To(Equal(modmock.Options{
					GVK:               schema.GroupVersionKind{Group: "mock.example.org", Version: "v1alpha1", Kind: "SampleModule"},
					PlatformConfigMap: "platform-config",
					Namespace:         "selected-namespace",
				}))
				g.Expect(getter.ToRawKubeConfigLoader().RawConfig()).Error().NotTo(HaveOccurred())
				return scenario.err
			}
			root := NewRootCommand(nil, nil, closeContext, nil, nil)
			var stderr bytes.Buffer
			root.SetErr(&stderr)
			root.SetArgs([]string{
				"--kubeconfig", kubeconfigFile(t), "--context", "test-context",
				"module", "mock", "--api-version", "mock.example.org/v1alpha1",
				"--kind", "SampleModule", "--platform-configmap", "platform-config",
				"--namespace", "selected-namespace",
			})

			err := root.ExecuteContext(ctx)

			g.Expect(ExitCode(err)).To(Equal(scenario.code))
			if scenario.err != nil {
				g.Expect(err).To(MatchError(scenario.err))
				g.Expect(stderr.String()).To(ContainSubstring(scenario.err.Error()))
			}
		})
	}
}

func TestRootMockRejectsInvalidFlagsBeforeResolvingKubeconfig(t *testing.T) {
	g := NewWithT(t)
	missingKubeconfig := filepath.Join(t.TempDir(), "does-not-exist")
	for _, args := range [][]string{
		{"--kind", "SampleModule", "--platform-configmap", "platform-config"},
		{"--api-version", "v1", "--kind", "SampleModule", "--platform-configmap", "platform-config"},
	} {
		root := NewRootCommand(nil, nil, nil, nil, nil)
		root.SetErr(new(bytes.Buffer))
		root.SetArgs(append([]string{"--kubeconfig", missingKubeconfig, "module", "mock"}, args...))

		g.Expect(ExitCode(root.ExecuteContext(t.Context()))).To(Equal(2))
	}
}

func TestRootCommandRejectsUnknownCommands(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil,
		"frobnicate", "--config", "definition.yaml")

	g.Expect(err).To(HaveOccurred())
	g.Expect(ExitCode(err)).To(Equal(lint.ExitUsage))
	// The unknown command is rejected, never stubbed.
	g.Expect(err.Error()).To(ContainSubstring("unknown"))
	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(runRecorder.calls).To(Equal(0))
	g.Expect(stdout).To(BeEmpty())
}

func TestRootCommandRejectsOldRunCommand(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	// The old command name is unknown, not an alias: it fails as a
	// usage error before any action, report, or cluster access.
	stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil,
		"module", "run",
		"--config", "definition.yaml",
		"--platform-version", "2.19.0")

	g.Expect(err).To(HaveOccurred())
	g.Expect(ExitCode(err)).To(Equal(lint.ExitUsage))
	g.Expect(err.Error()).To(ContainSubstring("unknown"))
	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(runRecorder.calls).To(Equal(0))
	g.Expect(stdout).To(BeEmpty())
}

func TestRootCommandRegistersStandardFlagsOnce(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	root := NewRootCommand(lintRecorder.run, runRecorder.deploy, nil, nil, nil)

	// The standard flags exist exactly once, on the root's persistent
	// flag set.
	for _, name := range []string{
		"kubeconfig", "context", "cluster", "server", "namespace", "user",
		"token", "insecure-skip-tls-verify", "client-certificate",
	} {
		g.Expect(root.PersistentFlags().Lookup(name)).NotTo(BeNil(),
			"root persistent flag %s", name)
	}

	// The deploy command registers none of them itself.
	runCommand, _, err := root.Find([]string{"module", "deploy"})
	g.Expect(err).NotTo(HaveOccurred())
	for _, name := range []string{"kubeconfig", "context", "cluster", "namespace", "server"} {
		g.Expect(runCommand.Flags().Lookup(name)).To(BeNil(),
			"deploy-local flag %s", name)
	}

	// The lint command registers none of them either.
	lintCommand, _, err := root.Find([]string{"module", "lint"})
	g.Expect(err).NotTo(HaveOccurred())
	for _, name := range []string{"kubeconfig", "context", "namespace"} {
		g.Expect(lintCommand.Flags().Lookup(name)).To(BeNil(),
			"lint-local flag %s", name)
	}
}

func TestRootCommandDeployForwardsStandardGetterAndContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	ctx := t.Context()

	_, err := execute(ctx, t, lintRecorder.run, runRecorder.deploy, nil, nil,
		"module", "deploy",
		"--config", "definition.yaml",
		"--platform-version", "2.19.0",
		"--kubeconfig", kubeconfigFile(t))
	g.Expect(err).NotTo(HaveOccurred())

	// Exactly one deploy action call, with the caller's context, the
	// root's standard getter, and the kubeconfig context's namespace
	// resolved into the options.
	g.Expect(runRecorder.calls).To(Equal(1))
	g.Expect(runRecorder.contexts).To(HaveLen(1))
	g.Expect(runRecorder.contexts[0]).To(Equal(ctx))
	g.Expect(runRecorder.options).To(HaveLen(1))
	g.Expect(runRecorder.options[0].Namespace).To(Equal("run-namespace"))

	g.Expect(lintRecorder.calls).To(Equal(0))
}

func TestRootCommandNamespaceOverrideWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil,
		"module", "deploy",
		"--config", "definition.yaml",
		"--platform-version", "2.19.0",
		"--kubeconfig", kubeconfigFile(t),
		"--namespace", "override-namespace")
	g.Expect(err).NotTo(HaveOccurred())

	// The standard -n override wins over the kubeconfig context
	// namespace.
	g.Expect(runRecorder.options).To(HaveLen(1))
	g.Expect(runRecorder.options[0].Namespace).To(Equal("override-namespace"))
}

func TestRootCommandLintNeverResolvesKubeconfig(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	// The inherited --kubeconfig flag is parsed but never loaded:
	// the lint action runs against a nonexistent file.
	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil,
		"module", "lint",
		"--config", "definition.yaml",
		"--kubeconfig", "/nonexistent/kubeconfig")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(lintRecorder.calls).To(Equal(1))
	g.Expect(runRecorder.calls).To(Equal(0))
}

func TestRootCommandDeployHelpNeverResolvesKubeconfig(t *testing.T) {
	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	t.Setenv("KUBECONFIG", "/nonexistent/kubeconfig")

	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy, nil, nil,
		"module", "deploy", "--help")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(runRecorder.calls).To(Equal(0))
}

// TestRootCommandGenerateNeverResolvesKubeconfig proves generation is
// cluster-independent: the inherited --kubeconfig flag is parsed but
// never loaded, and neither lint nor deploy runs.
func TestRootCommandGenerateNeverResolvesKubeconfig(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}
	generateRecorder := &recordingModuleGenerate{}

	stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.deploy,
		generateRecorder.generate, nil,
		"module", "generate",
		"--kubeconfig", "/nonexistent/kubeconfig",
		"source-dir")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(Equal("kind: x\n"))
	g.Expect(generateRecorder.calls).To(Equal(1))
	g.Expect(generateRecorder.options).To(Equal([]generator.Options{{
		SourceDir: "source-dir",
	}}))
	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(runRecorder.calls).To(Equal(0))
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	g.Expect(ExitCode(nil)).To(Equal(lint.ExitSuccess))
	g.Expect(ExitCode(errors.New("flag needs an argument"))).To(Equal(lint.ExitUsage))
	g.Expect(ExitCode(fmt.Errorf("wrapped: %w",
		&exit.Error{Code: lint.ExitFailure, Err: errors.New("render")}))).To(Equal(lint.ExitFailure))
	g.Expect(ExitCode(fmt.Errorf("wrapped: %w",
		&exit.Error{Code: deploycli.ExitUsage, Err: errors.New("invalid options")}))).To(Equal(deploycli.ExitUsage))
	g.Expect(ExitCode(fmt.Errorf("wrapped: %w",
		&exit.Error{Code: deploycli.ExitFailure, Err: context.Canceled}))).To(Equal(deploycli.ExitFailure))
}

// TestDebugFlagPropagatesToSubcommands proves the root --debug flag is
// parsed through subcommand execution and visible on the root's
// persistent flag set afterwards, which is where run reads it.
func TestDebugFlagPropagatesToSubcommands(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleDeploy{}

	for _, scenario := range []struct {
		name  string
		args  []string
		debug bool
	}{
		{name: "absent", args: []string{"module", "lint", "--config", "definition.yaml"}, debug: false},
		{name: "present", args: []string{"--debug", "module", "lint", "--config", "definition.yaml"}, debug: true},
	} {
		// Sequential by design: the scenarios share the recorders that
		// the outer assertions count once both have run.
		t.Run(scenario.name, func(t *testing.T) {
			g := NewWithT(t)

			root := NewRootCommand(lintRecorder.run, runRecorder.deploy, nil, nil, nil)
			root.SetOut(new(bytes.Buffer))
			root.SetErr(new(bytes.Buffer))
			root.SetArgs(scenario.args)

			g.Expect(root.ExecuteContext(t.Context())).NotTo(HaveOccurred())
			g.Expect(debugEnabled(root)).To(Equal(scenario.debug))
		})
	}

	g.Expect(lintRecorder.calls).To(Equal(2))
	g.Expect(runRecorder.calls).To(Equal(0))
}

// TestWriteErrorChain proves the --debug diagnostic view: the error and
// every wrapped cause, one indented line per cause, descending into
// each branch of joined errors.
func TestWriteErrorChain(t *testing.T) {
	t.Parallel()

	t.Run("nil writes nothing", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var out bytes.Buffer
		g.Expect(writeErrorChain(&out, nil)).To(Succeed())
		g.Expect(out.String()).To(BeEmpty())
	})

	t.Run("plain error writes one line", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var out bytes.Buffer
		g.Expect(writeErrorChain(&out, errors.New("flag needs an argument"))).To(Succeed())

		g.Expect(out.String()).To(Equal("module debug: flag needs an argument\n"))
	})

	t.Run("classified chain writes every cause", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		inner := errors.New("an empty namespace may not be set when a resource name is provided")
		middle := fmt.Errorf("failed to lookup object /policy: %w", inner)
		err := moduleerrors.New(
			moduleerrors.ErrDeployApply,
			"applying the module resources failed",
			moduleerrors.WithCause(middle))

		var out bytes.Buffer
		g.Expect(writeErrorChain(&out, err)).To(Succeed())

		g.Expect(out.String()).To(Equal(strings.Join([]string{
			"module debug: applying the module resources failed",
			"module debug:   cause: failed to lookup object /policy: " + inner.Error(),
			"module debug:     cause: " + inner.Error(),
			"",
		}, "\n")))
	})

	t.Run("joined errors write each branch", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		err := errors.Join(
			errors.New("write report failed"),
			fmt.Errorf("deploy failed: %w", errors.New("boom")))

		var out bytes.Buffer
		g.Expect(writeErrorChain(&out, err)).To(Succeed())

		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		g.Expect(lines).To(HaveLen(5))
		g.Expect(lines[0]).To(Equal("module debug: write report failed"))
		g.Expect(lines[1]).To(Equal("deploy failed: boom"))
		g.Expect(lines[2]).To(Equal("module debug:   cause: write report failed"))
		g.Expect(lines[3]).To(Equal("module debug:   cause: deploy failed: boom"))
		g.Expect(lines[4]).To(Equal("module debug:     cause: boom"))
	})
}

// TestRootCommandValidateForwardsGetterAndContext proves the fifth
// injected action is wired through the root and group with the
// caller's context, and its report reaches stdout.
func TestRootCommandValidateForwardsGetterAndContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	deployRecorder := &recordingModuleDeploy{}
	validateRecorder := &recordingModuleValidate{}

	ctx := t.Context()

	stdout, err := execute(ctx, t, lintRecorder.run, deployRecorder.deploy, nil,
		validateRecorder.validate,
		"module", "validate",
		"--config", "definition.yaml",
		"--timeout", "20s")
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(stdout).To(ContainSubstring("module validate definition.yaml: pass"))
	g.Expect(validateRecorder.calls).To(Equal(1))
	g.Expect(validateRecorder.contexts).To(HaveLen(1))
	g.Expect(validateRecorder.contexts[0]).To(Equal(ctx))
	g.Expect(validateRecorder.options).To(HaveLen(1))
	g.Expect(validateRecorder.options[0].DefinitionFile).To(Equal("definition.yaml"))
	g.Expect(validateRecorder.options[0].Timeout).To(Equal(20 * time.Second))

	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(deployRecorder.calls).To(Equal(0))
}

// TestRootCommandValidateHelpNeverResolvesKubeconfig proves validate
// help works offline: the inherited --kubeconfig flag is parsed but
// never loaded.
func TestRootCommandValidateHelpNeverResolvesKubeconfig(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	deployRecorder := &recordingModuleDeploy{}
	validateRecorder := &recordingModuleValidate{}

	stdout, err := execute(t.Context(), t, lintRecorder.run, deployRecorder.deploy, nil,
		validateRecorder.validate,
		"module", "validate", "--help",
		"--kubeconfig", "/nonexistent/kubeconfig")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(ContainSubstring("platform contract"))
	g.Expect(validateRecorder.calls).To(Equal(0))
}
