package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
	runcli "github.com/lburgazzoli/odh-platform-tools/cmd/module/run"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner"
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

// recordingModuleRun is a fake run action boundary recording every
// invocation, the context it received, and the namespace the standard
// getter resolves to.
type recordingModuleRun struct {
	calls      int
	contexts   []context.Context
	getters    []genericclioptions.RESTClientGetter
	options    []runner.Options
	namespaces []string
}

// run records one invocation and returns a successful empty result.
func (r *recordingModuleRun) run(
	ctx context.Context,
	getter genericclioptions.RESTClientGetter,
	options runner.Options,
) (runner.Result, error) {
	r.calls++
	r.contexts = append(r.contexts, ctx)
	r.getters = append(r.getters, getter)
	r.options = append(r.options, options)

	namespace, _, err := getter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return runner.Result{}, err
	}
	r.namespaces = append(r.namespaces, namespace)

	return runner.Result{}, nil
}

// execute runs one root command execution with captured streams.
func execute(
	ctx context.Context,
	t *testing.T,
	lintRun lint.Run,
	runRun runcli.Run,
	args ...string,
) (string, error) {
	t.Helper()

	root := NewRootCommand(lintRun, runRun)

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
		runRecorder := &recordingModuleRun{}

		stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run, "--help")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(stdout).To(ContainSubstring("Offline platform module tooling"))
		g.Expect(stdout).To(ContainSubstring("\n  module "))
		g.Expect(lintRecorder.calls).To(Equal(0))
		g.Expect(runRecorder.calls).To(Equal(0))
	})

	t.Run("lint help", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		lintRecorder := &recordingRun{}
		runRecorder := &recordingModuleRun{}

		stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run,
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
		runRecorder := &recordingModuleRun{}

		stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run,
			"module", "run", "--help")

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
}

func TestRootCommandRejectsUnknownCommands(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleRun{}

	stdout, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run,
		"frobnicate", "--config", "definition.yaml")

	g.Expect(err).To(HaveOccurred())
	g.Expect(ExitCode(err)).To(Equal(lint.ExitUsage))
	// The unknown command is rejected, never stubbed.
	g.Expect(err.Error()).To(ContainSubstring("unknown"))
	g.Expect(lintRecorder.calls).To(Equal(0))
	g.Expect(runRecorder.calls).To(Equal(0))
	g.Expect(stdout).To(BeEmpty())
}

func TestRootCommandRegistersStandardFlagsOnce(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleRun{}

	root := NewRootCommand(lintRecorder.run, runRecorder.run)

	// The standard flags exist exactly once, on the root's persistent
	// flag set.
	for _, name := range []string{
		"kubeconfig", "context", "cluster", "server", "namespace", "user",
		"token", "insecure-skip-tls-verify", "client-certificate",
	} {
		g.Expect(root.PersistentFlags().Lookup(name)).NotTo(BeNil(),
			"root persistent flag %s", name)
	}

	// The run command registers none of them itself.
	runCommand, _, err := root.Find([]string{"module", "run"})
	g.Expect(err).NotTo(HaveOccurred())
	for _, name := range []string{"kubeconfig", "context", "cluster", "namespace", "server"} {
		g.Expect(runCommand.Flags().Lookup(name)).To(BeNil(),
			"run-local flag %s", name)
	}

	// The lint command registers none of them either.
	lintCommand, _, err := root.Find([]string{"module", "lint"})
	g.Expect(err).NotTo(HaveOccurred())
	for _, name := range []string{"kubeconfig", "context", "namespace"} {
		g.Expect(lintCommand.Flags().Lookup(name)).To(BeNil(),
			"lint-local flag %s", name)
	}
}

func TestRootCommandRunForwardsStandardGetterAndContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleRun{}

	ctx := t.Context()

	_, err := execute(ctx, t, lintRecorder.run, runRecorder.run,
		"module", "run",
		"--config", "definition.yaml",
		"--platform-version", "2.19.0",
		"--kubeconfig", kubeconfigFile(t))
	g.Expect(err).NotTo(HaveOccurred())

	// Exactly one run action call, with the caller's context, the
	// root's standard getter, and the kubeconfig context's namespace.
	g.Expect(runRecorder.calls).To(Equal(1))
	g.Expect(runRecorder.contexts).To(HaveLen(1))
	g.Expect(runRecorder.contexts[0]).To(Equal(ctx))
	g.Expect(runRecorder.namespaces).To(Equal([]string{"run-namespace"}))

	g.Expect(lintRecorder.calls).To(Equal(0))
}

func TestRootCommandNamespaceOverrideWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleRun{}

	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run,
		"module", "run",
		"--config", "definition.yaml",
		"--platform-version", "2.19.0",
		"--kubeconfig", kubeconfigFile(t),
		"--namespace", "override-namespace")
	g.Expect(err).NotTo(HaveOccurred())

	// The standard -n override wins over the kubeconfig context
	// namespace.
	g.Expect(runRecorder.namespaces).To(Equal([]string{"override-namespace"}))
}

func TestRootCommandLintNeverResolvesKubeconfig(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleRun{}

	// The inherited --kubeconfig flag is parsed but never loaded:
	// the lint action runs against a nonexistent file.
	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run,
		"module", "lint",
		"--config", "definition.yaml",
		"--kubeconfig", "/nonexistent/kubeconfig")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(lintRecorder.calls).To(Equal(1))
	g.Expect(runRecorder.calls).To(Equal(0))
}

func TestRootCommandRunHelpNeverResolvesKubeconfig(t *testing.T) {
	g := NewWithT(t)

	lintRecorder := &recordingRun{}
	runRecorder := &recordingModuleRun{}

	t.Setenv("KUBECONFIG", "/nonexistent/kubeconfig")

	_, err := execute(t.Context(), t, lintRecorder.run, runRecorder.run,
		"module", "run", "--help")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(runRecorder.calls).To(Equal(0))
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	g.Expect(ExitCode(nil)).To(Equal(lint.ExitSuccess))
	g.Expect(ExitCode(errors.New("flag needs an argument"))).To(Equal(lint.ExitUsage))
	g.Expect(ExitCode(fmt.Errorf("wrapped: %w",
		&lint.ExitError{Code: lint.ExitFailure, Err: errors.New("render")}))).To(Equal(lint.ExitFailure))
	g.Expect(ExitCode(fmt.Errorf("wrapped: %w",
		&runcli.ExitError{Code: runcli.ExitUsage, Err: errors.New("invalid options")}))).To(Equal(runcli.ExitUsage))
	g.Expect(ExitCode(fmt.Errorf("wrapped: %w",
		&runcli.ExitError{Code: runcli.ExitFailure, Err: context.Canceled}))).To(Equal(runcli.ExitFailure))
}
