package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// execute runs one root command execution with captured streams.
func execute(
	ctx context.Context,
	t *testing.T,
	run lint.Run,
	args ...string,
) (string, error) {
	t.Helper()

	root := NewRootCommand(run)

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	err := root.ExecuteContext(ctx)

	return stdout.String(), err
}

func TestRootCommandRejectsDeferredCommands(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	stdout, err := execute(t.Context(), t, recorder.run,
		"module", "run", "--config", "definition.yaml")

	g.Expect(err).To(HaveOccurred())
	g.Expect(ExitCode(err)).To(Equal(lint.ExitUsage))
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

		stdout, err := execute(t.Context(), t, recorder.run, "--help")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(stdout).To(ContainSubstring("Offline platform module tooling"))
		g.Expect(stdout).To(ContainSubstring("\n  module "))
		g.Expect(recorder.calls).To(Equal(0))
	})

	t.Run("lint help", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingRun{}

		stdout, err := execute(t.Context(), t, recorder.run,
			"module", "lint", "--help")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(stdout).To(ContainSubstring("--config"))
		g.Expect(stdout).To(ContainSubstring("--output"))
		g.Expect(recorder.calls).To(Equal(0))
	})
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	g.Expect(ExitCode(nil)).To(Equal(lint.ExitSuccess))
	g.Expect(ExitCode(errors.New("flag needs an argument"))).To(Equal(lint.ExitUsage))
	g.Expect(ExitCode(fmt.Errorf("wrapped: %w",
		&lint.ExitError{Code: lint.ExitFailure, Err: errors.New("render")}))).To(Equal(lint.ExitFailure))
}
