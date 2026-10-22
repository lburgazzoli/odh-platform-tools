//go:build integration

package integration

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/lint"
)

// cliBinary is the built CLI binary, compiled once for the suite: the
// command tree lives in the main package and cannot be imported, so
// the suite exercises the real process, stdout, stderr, and exit
// codes.
var cliBinary string

// TestMain builds the CLI binary once for the whole suite.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "odh-platform-tools-cli")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	cliBinary = filepath.Join(dir, "odh-platform-tools")

	build := exec.Command("go", "build", "-o", cliBinary,
		"github.com/lburgazzoli/odh-platform-tools/cmd")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// jsonUnmarshalReport decodes one executed JSON report document.
func jsonUnmarshalReport(data []byte, report *lint.Report) error {
	return jsonv2.Unmarshal(data, report)
}

// runCLI executes the built CLI over one module root, returning
// stdout, stderr, and the process exit code.
func runCLI(root string, flags ...string) (string, string, int) {
	args := append([]string{
		"module",
		"lint",
		"--config", filepath.Join(root, "definition.yaml"),
	}, flags...)

	var stdout, stderr bytes.Buffer

	command := exec.Command(cliBinary, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr

	exitCode := 0
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = lint.ExitFailure
		}
	}

	return stdout.String(), stderr.String(), exitCode
}

func TestCLIChainHelmValidModule(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmModule(t, root)

	stdout, stderr, exitCode := runCLI(root, "--output", "json")

	g.Expect(exitCode).To(Equal(lint.ExitSuccess))
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

	stdout, stderr, exitCode := runCLI(root, "--output", "json")

	g.Expect(exitCode).To(Equal(lint.ExitSuccess))
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

	stdout, _, exitCode := runCLI(root)

	g.Expect(exitCode).To(Equal(lint.ExitSuccess))
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

	stdout, stderr, exitCode := runCLI(root, "--output", "json")

	g.Expect(exitCode).To(Equal(lint.ExitFailure))
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

	stdout, stderr, exitCode := runCLI(root, "--output", "json")

	g.Expect(exitCode).To(Equal(lint.ExitUsage))
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

	stdout, stderr, exitCode := runCLI(root, "--output", "json")

	g.Expect(exitCode).To(Equal(lint.ExitFailure))
	g.Expect(stderr).To(ContainSubstring(lint.FailureRender))

	var report lint.Report
	g.Expect(jsonUnmarshalReport([]byte(stdout), &report)).To(Succeed())
	g.Expect(report.Status).To(Equal(lint.StatusFailed))
	g.Expect(report.Failure.Category).To(Equal(lint.FailureRender))
}
