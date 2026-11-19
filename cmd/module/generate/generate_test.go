/*
Copyright 2026 Luca Burgazzoli

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package generate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"sigs.k8s.io/yaml"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/generator"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/exit"
)

// generatedDocument is a stable result payload.
const generatedDocument = "apiVersion: config.opendatahub.io/v1alpha1\nkind: PlatformModuleDefinition\n"

// recordingRun is a fake action boundary recording every invocation.
type recordingRun struct {
	calls    int
	contexts []context.Context
	options  []generator.Options
	result   generator.Result
	err      error
}

// run records one invocation and returns the configured outcome.
func (r *recordingRun) run(ctx context.Context, options generator.Options) (generator.Result, error) {
	r.calls++
	r.contexts = append(r.contexts, ctx)
	r.options = append(r.options, options)

	return r.result, r.err
}

// exitCode maps one generate command error to its process exit
// status: classified outcomes keep their own code, and flag or usage
// failures are usage failures.
func exitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}

	var exitErr *exit.Error
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}

	return ExitUsage
}

// executeGenerate runs the generate command with the given arguments.
func executeGenerate(
	ctx context.Context,
	t *testing.T,
	run Run,
	args ...string,
) (string, string, error) {
	t.Helper()

	command := NewGenerateCommand(run)

	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)

	err := command.ExecuteContext(ctx)

	return stdout.String(), stderr.String(), err
}

// failingWriter rejects every write.
type failingWriter struct{}

// Write reports a transport failure.
func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("stdout transport failed")
}

// shortWriter accepts every write but reports one byte short.
type shortWriter struct {
	inner bytes.Buffer
}

// Write stores the payload and reports a short count.
func (w *shortWriter) Write(payload []byte) (int, error) {
	_, _ = w.inner.Write(payload)

	if len(payload) == 0 {
		return 0, nil
	}

	return len(payload) - 1, nil
}

// TestGenerateCommandForwardsOptionsAndContext proves every flag maps
// to its Options field, defaults stay empty, and the caller's context
// is forwarded untouched.
func TestGenerateCommandForwardsOptionsAndContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}
	ctx := t.Context()

	_, _, err := executeGenerate(ctx, t, recorder.run,
		"--name", "demo",
		"--init-container", "setup",
		"--related-images-file", "images.yaml",
		"--values-file", "values.yaml",
		"--values", "a=b",
		"--values", "c=d,e",
		"--output", "module.yaml",
		"source")
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(recorder.calls).To(Equal(1))
	g.Expect(recorder.contexts).To(HaveLen(1))
	g.Expect(recorder.contexts[0]).To(BeIdenticalTo(ctx))
	g.Expect(recorder.options).To(Equal([]generator.Options{{
		SourceDir:         "source",
		Name:              "demo",
		InitContainer:     "setup",
		RelatedImagesFile: "images.yaml",
		ValuesFile:        "values.yaml",
		Values:            []string{"a=b", "c=d,e"},
		Output:            "module.yaml",
	}}))
}

// TestGenerateCommandDefaultsToEmptyOptions proves an omitted flag
// leaves its Options field empty for the service to default.
func TestGenerateCommandDefaultsToEmptyOptions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	_, _, err := executeGenerate(t.Context(), t, recorder.run, "source")
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(recorder.calls).To(Equal(1))
	g.Expect(recorder.options).To(Equal([]generator.Options{{
		SourceDir: "source",
	}}))
}

// TestGenerateCommandPreservesValueExpressions proves repeated --values
// arguments keep their order and their entire text, including commas
// and escapes, without comma splitting.
func TestGenerateCommandPreservesValueExpressions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	_, _, err := executeGenerate(t.Context(), t, recorder.run,
		"--values", "list=a,b,c",
		"--values", `nested={"x":"y"}`,
		"--values", "escape=val\\,ue",
		"source")
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(recorder.options).To(HaveLen(1))
	g.Expect(recorder.options[0].Values).To(Equal([]string{
		"list=a,b,c",
		`nested={"x":"y"}`,
		`escape=val\,ue`,
	}))
}

// TestGenerateCommandRejectsInvalidFlagUsage proves arity, unknown
// flags, missing flag values, a repeated or empty values file, and
// explicitly empty scalar options all fail as usage errors before the
// action runs.
func TestGenerateCommandRejectsInvalidFlagUsage(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "missing source", args: nil},
		{name: "two sources", args: []string{"one", "two"}},
		{name: "unknown flag", args: []string{"--frobnicate", "source"}},
		{name: "missing flag value", args: []string{"--name"}},
		{
			name: "repeated values file",
			args: []string{"--values-file", "a.yaml", "--values-file", "b.yaml", "source"},
		},
		{
			name: "empty values file",
			args: []string{"--values-file", "", "source"},
		},
		{
			name: "empty output",
			args: []string{"--output", "", "source"},
		},
		{
			name: "empty name",
			args: []string{"--name", "", "source"},
		},
		{
			name: "empty init container",
			args: []string{"--init-container", "", "source"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			recorder := &recordingRun{}

			_, _, err := executeGenerate(t.Context(), t, recorder.run, tc.args...)

			g.Expect(err).To(HaveOccurred())
			g.Expect(exitCode(err)).To(Equal(ExitUsage))
			g.Expect(recorder.calls).To(Equal(0), "no action may run")
		})
	}
}

// TestGenerateCommandClassifiesFailures proves the failure table:
// option, definition, and image-file categories exit 2; validation,
// output, reserved, render, and cancellation failures exit 1, with
// cancellation identity preserved.
func TestGenerateCommandClassifiesFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		err      error
		code     int
		category string
	}{
		{
			name: "invalid options",
			err:  moduleerrors.New(moduleerrors.ErrInvalidGenerateOptions, "bad options"),
			code: ExitUsage,
		},
		{
			name: "invalid definition",
			err:  moduleerrors.New(moduleerrors.ErrInvalidDefinition, "bad definition"),
			code: ExitUsage,
		},
		{
			name: "image file",
			err:  moduleerrors.New(moduleerrors.ErrImageSource, "bad image file"),
			code: ExitUsage,
		},
		{
			name: "validation findings",
			err:  moduleerrors.New(moduleerrors.ErrGenerateValidation, "findings"),
			code: ExitFailure,
		},
		{
			name: "output failure",
			err:  moduleerrors.New(moduleerrors.ErrGenerateOutput, "output"),
			code: ExitFailure,
		},
		{
			name: "reserved resource",
			err:  moduleerrors.New(moduleerrors.ErrReservedResource, "reserved"),
			code: ExitFailure,
		},
		{
			name: "render failure",
			err:  moduleerrors.New(moduleerrors.ErrGenerateRender, "render"),
			code: ExitFailure,
		},
		{
			name: "cancellation",
			err:  context.Canceled,
			code: ExitFailure,
		},
		{
			name: "deadline",
			err:  context.DeadlineExceeded,
			code: ExitFailure,
		},
		{
			name: "unclassified render failure",
			err:  errors.New("native render boom"),
			code: ExitFailure,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			recorder := &recordingRun{err: tc.err}

			stdout, stderr, err := executeGenerate(t.Context(), t, recorder.run, "source")

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, tc.err)).To(BeTrue(),
				"the action error identity must survive")
			g.Expect(exitCode(err)).To(Equal(tc.code))
			g.Expect(stdout).To(BeEmpty(), "classified failures write no YAML")
			g.Expect(stderr).To(ContainSubstring("module generate source"))
			g.Expect(stderr).To(ContainSubstring(recorder.err.Error()))
		})
	}
}

// TestGenerateCommandWritesStdoutByteForByte proves a stdout result is
// written exactly, with no banners or summaries, and an empty stderr.
func TestGenerateCommandWritesStdoutByteForByte(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{result: generator.Result{
		YAML:   []byte(generatedDocument),
		Output: stdoutOutput,
	}}

	stdout, stderr, err := executeGenerate(t.Context(), t, recorder.run, "source")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(Equal(generatedDocument))
	g.Expect(stderr).To(BeEmpty())
}

// TestGenerateCommandFileResultWritesNothing proves a file result is
// already published and produces no stdout at all.
func TestGenerateCommandFileResultWritesNothing(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{result: generator.Result{
		YAML:   []byte(generatedDocument),
		Output: "/work/module.yaml",
	}}

	stdout, stderr, err := executeGenerate(t.Context(), t, recorder.run,
		"--output", "module.yaml", "source")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(BeEmpty())
	g.Expect(stderr).To(BeEmpty())
}

// TestGenerateCommandRejectsStdoutWriteFailures proves a failing or
// short stdout writer is an exit-1 failure.
func TestGenerateCommandRejectsStdoutWriteFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		writer io.Writer
	}{
		{name: "failing writer", writer: failingWriter{}},
		{name: "short writer", writer: &shortWriter{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			recorder := &recordingRun{result: generator.Result{
				YAML:   []byte(generatedDocument),
				Output: stdoutOutput,
			}}

			command := NewGenerateCommand(recorder.run)
			command.SetOut(tc.writer)
			command.SetErr(&bytes.Buffer{})
			command.SetArgs([]string{"source"})

			err := command.ExecuteContext(t.Context())

			g.Expect(err).To(HaveOccurred())
			g.Expect(exitCode(err)).To(Equal(ExitFailure))
			g.Expect(err.Error()).To(ContainSubstring("write generated definition"))
		})
	}
}

// TestGenerateCommandCancelsBeforeStdoutWrite proves cancellation
// between the action's success and the first stdout write prevents
// all output and exits 1 with the cancellation identity.
func TestGenerateCommandCancelsBeforeStdoutWrite(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	recorder := &recordingRun{result: generator.Result{
		YAML:   []byte(generatedDocument),
		Output: stdoutOutput,
	}}

	var stdout bytes.Buffer

	command := NewGenerateCommand(recorder.run)
	command.SetOut(&stdout)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"source"})

	err := command.ExecuteContext(ctx)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	g.Expect(exitCode(err)).To(Equal(ExitFailure))
	g.Expect(stdout.String()).To(BeEmpty())
}

// TestGenerateCommandHelpListsFlags proves help documents every flag
// and the strict discovery contract without running the action.
func TestGenerateCommandHelpListsFlags(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingRun{}

	stdout, _, err := executeGenerate(t.Context(), t, recorder.run, "--help")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(ContainSubstring("--name"))
	g.Expect(stdout).To(ContainSubstring("--init-container"))
	g.Expect(stdout).To(ContainSubstring("--related-images-file"))
	g.Expect(stdout).To(ContainSubstring("--values-file"))
	g.Expect(stdout).To(ContainSubstring("--values"))
	g.Expect(stdout).To(ContainSubstring("--output"))
	g.Expect(stdout).To(ContainSubstring("exactly one"))
	g.Expect(stdout).To(ContainSubstring("created once"))
	g.Expect(recorder.calls).To(Equal(0))
}

// exampleSource resolves one of the checked-in example module
// sources.
func exampleSource(t *testing.T, name string) string {
	t.Helper()

	source, err := filepath.Abs(filepath.Join("..", "..", "..", "examples", name))
	if err != nil {
		t.Fatalf("resolve example %s: %v", name, err)
	}

	return source
}

// TestGenerateActionInfersHelmExample proves the composed action
// infers a complete definition from the checked-in Helm chart: the
// lowercase CRD kind names the module, the storage version supplies
// the GVK, and the controller fields come from the rendered
// Deployment.
func TestGenerateActionInfersHelmExample(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result, err := Action(context.Background(), generator.Options{
		SourceDir: exampleSource(t, filepath.Join("helm", "chart")),
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Output).To(Equal("stdout"))

	definition := loadGenerated(t, g, result.YAML)

	g.Expect(definition.Name).To(Equal("helmmodule"))
	g.Expect(definition.Spec.Instance.Type).To(Equal(toolApi.ModuleType{
		Group:   "components.platform.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "HelmModule",
	}))
	g.Expect(definition.Spec.Instance.Name).To(Equal("helmmodule"))
	g.Expect(definition.Spec.Manifests.Format).To(Equal(toolApi.FormatHelm))
	g.Expect(definition.Spec.Manifests.Helm.Path).To(Equal(filepath.Join("..", "..", "..", "examples", "helm", "chart")))
	g.Expect(definition.Spec.Controller.Deployment.Name).To(Equal("mock-controller"))
	g.Expect(definition.Spec.Controller.Deployment.Container.Name).
		To(Equal(toolApi.DefaultMainContainerName))
	g.Expect(definition.Spec.Controller.Deployment.Container.Image).
		To(Equal("quay.io/lburgazzoli/odh-platform-tools:dev"))
}

// TestGenerateActionInfersKustomizeExample proves the composed action
// infers a complete definition from the checked-in Kustomize tree.
func TestGenerateActionInfersKustomizeExample(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result, err := Action(context.Background(), generator.Options{
		SourceDir: exampleSource(t, "kustomize"),
	})

	g.Expect(err).NotTo(HaveOccurred())

	definition := loadGenerated(t, g, result.YAML)

	g.Expect(definition.Name).To(Equal("kustomizemodule"))
	g.Expect(definition.Spec.Instance.Type).To(Equal(toolApi.ModuleType{
		Group:   "components.platform.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "KustomizeModule",
	}))
	g.Expect(definition.Spec.Manifests.Format).To(Equal(toolApi.FormatKustomize))
	g.Expect(definition.Spec.Controller.Deployment.Name).To(Equal("mock-controller"))
	g.Expect(definition.Spec.Controller.Deployment.Container.Image).
		To(Equal("quay.io/lburgazzoli/odh-platform-tools:dev"))
}

// TestGenerateActionIsDeterministic proves repeated runs over the
// same source produce identical bytes.
func TestGenerateActionIsDeterministic(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := generator.Options{SourceDir: exampleSource(t, "kustomize")}

	first, err := Action(context.Background(), options)
	g.Expect(err).NotTo(HaveOccurred())

	second, err := Action(context.Background(), options)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(second.YAML).To(Equal(first.YAML))
}

// loadGenerated decodes a generated document with the existing
// loader, proving the emitted YAML is loadable as authored.
func loadGenerated(
	t *testing.T,
	g *WithT,
	document []byte,
) toolApi.PlatformModuleDefinition {
	t.Helper()

	var generic map[string]any
	g.Expect(yaml.Unmarshal(document, &generic)).To(Succeed())

	path := filepath.Join(t.TempDir(), "module.yaml")
	g.Expect(os.WriteFile(path, document, 0o600)).To(Succeed())

	definition, err := loader.LoadFromPath(context.Background(), path)
	g.Expect(err).NotTo(HaveOccurred())

	return definition
}
