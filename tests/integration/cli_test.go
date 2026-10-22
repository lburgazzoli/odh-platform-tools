//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	jsonv2 "encoding/json/v2"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/cli"
	"github.com/lburgazzoli/odh-platform-tools/pkg/cli/lint"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module"
)

// jsonUnmarshalReport decodes one executed JSON report document.
func jsonUnmarshalReport(data []byte, report *lint.Report) error {
	return jsonv2.Unmarshal(data, report)
}

// runCLI executes the actual command surface wired with the real
// composed action over one module root, returning stdout, stderr, and
// the execution error.
func runCLI(
	ctx context.Context,
	root string,
	flags ...string,
) (string, string, error) {
	command := cli.NewRootCommand(module.NewLinter().Run)

	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)

	args := append([]string{
		"module",
		"lint",
		"--config", filepath.Join(root, "definition.yaml"),
	}, flags...)
	command.SetArgs(args)

	err := command.ExecuteContext(ctx)

	return stdout.String(), stderr.String(), err
}

func TestCLIChainHelmValidModule(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmModule(t, root)

	stdout, stderr, err := runCLI(t.Context(), root, "--output", "json")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cli.ExitCode(err)).To(Equal(lint.ExitSuccess))
	g.Expect(stderr).To(BeEmpty())

	var report lint.Report
	g.Expect(jsonUnmarshalReport([]byte(stdout), &report)).To(Succeed())
	g.Expect(report.Status).To(Equal(lint.StatusPass))
	g.Expect(report.HasErrors).To(BeFalse())
	g.Expect(report.Diagnostics).To(BeEmpty())
}

func TestCLIChainKustomizeValidModule(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeModule(t, root)

	stdout, stderr, err := runCLI(t.Context(), root, "--output", "json")

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stderr).To(BeEmpty())

	var report lint.Report
	g.Expect(jsonUnmarshalReport([]byte(stdout), &report)).To(Succeed())
	g.Expect(report.Status).To(Equal(lint.StatusPass))
}

func TestCLIChainTextReportDefault(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmModule(t, root)

	stdout, _, err := runCLI(t.Context(), root)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stdout).To(ContainSubstring("module lint "))
	g.Expect(stdout).To(ContainSubstring(": pass\n"))
}

func TestCLIChainFindingsExitFailure(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", plainChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", chartValuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/namespace.yaml", namespaceTemplateYAML)

	stdout, stderr, err := runCLI(t.Context(), root, "--output", "json")

	g.Expect(err).To(HaveOccurred())
	g.Expect(cli.ExitCode(err)).To(Equal(lint.ExitFailure))
	// Findings are report content: no stderr failure line.
	g.Expect(stderr).To(BeEmpty())

	var report lint.Report
	g.Expect(jsonUnmarshalReport([]byte(stdout), &report)).To(Succeed())
	g.Expect(report.Status).To(Equal(lint.StatusFindings))
	g.Expect(report.HasErrors).To(BeTrue())
	g.Expect(report.Diagnostics).NotTo(BeEmpty())
}

func TestCLIChainDefinitionErrorExitUsage(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", invalidDefinitionYAML)

	stdout, stderr, err := runCLI(t.Context(), root, "--output", "json")

	g.Expect(err).To(HaveOccurred())
	g.Expect(cli.ExitCode(err)).To(Equal(lint.ExitUsage))
	g.Expect(stderr).To(ContainSubstring(lint.FailureDefinition))

	var report lint.Report
	g.Expect(jsonUnmarshalReport([]byte(stdout), &report)).To(Succeed())
	g.Expect(report.Status).To(Equal(lint.StatusFailed))
	g.Expect(report.Failure.Category).To(Equal(lint.FailureDefinition))
}

func TestCLIChainRenderErrorExitFailure(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", plainChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", chartValuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/broken.yaml", brokenTemplateYAML)

	stdout, stderr, err := runCLI(t.Context(), root, "--output", "json")

	g.Expect(err).To(HaveOccurred())
	g.Expect(cli.ExitCode(err)).To(Equal(lint.ExitFailure))
	g.Expect(stderr).To(ContainSubstring(lint.FailureRender))

	var report lint.Report
	g.Expect(jsonUnmarshalReport([]byte(stdout), &report)).To(Succeed())
	g.Expect(report.Status).To(Equal(lint.StatusFailed))
	g.Expect(report.Failure.Category).To(Equal(lint.FailureRender))
}

func TestCLIChainCancelledContextExitFailure(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmModule(t, root)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	stdout, _, err := runCLI(ctx, root, "--output", "json")

	g.Expect(err).To(HaveOccurred())
	g.Expect(cli.ExitCode(err)).To(Equal(lint.ExitFailure))
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

	var report lint.Report
	g.Expect(jsonUnmarshalReport([]byte(stdout), &report)).To(Succeed())
	g.Expect(report.Status).To(Equal(lint.StatusFailed))
	g.Expect(report.Failure.Category).To(Equal(lint.FailureCancelled))
}
