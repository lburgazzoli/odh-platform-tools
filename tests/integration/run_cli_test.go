//go:build integration

package integration

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/cmd/module/run"
)

// runCLIRun executes the built CLI's run command over one module
// root with a nonexistent kubeconfig — every case below fails before
// any cluster contact, so the unreachable configuration proves no API
// call happened — returning stdout, stderr, and the exit code.
func runCLIRun(root string, flags ...string) (string, string, int) {
	args := append([]string{
		"module",
		"run",
		"--config", filepath.Join(root, "definition.yaml"),
		"--platform-version", "2.19.0",
		"--kubeconfig", "/nonexistent/kubeconfig",
	}, flags...)

	var stdout, stderr bytes.Buffer

	command := exec.Command(cliBinary, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr

	exitCode := run.ExitSuccess
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = run.ExitFailure
		}
	}

	return stdout.String(), stderr.String(), exitCode
}

// TestCLIRunHelp exits successfully without touching any module or
// cluster.
func TestCLIRunHelp(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	command := exec.Command(cliBinary, "module", "run", "--help")

	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	g.Expect(command.Run()).To(Succeed())
	g.Expect(stdout.String()).To(SatisfyAll(
		ContainSubstring("module run"),
		ContainSubstring("--config"),
		ContainSubstring("--platform-version"),
		ContainSubstring("--wait"),
	))
	g.Expect(stderr.String()).To(BeEmpty())
}

// TestCLIRunRequiresConfigAndPlatformVersion proves the required
// flags are usage failures: exit code 2, no report, no cluster
// contact.
func TestCLIRunRequiresConfigAndPlatformVersion(t *testing.T) {
	t.Parallel()

	t.Run("missing config", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var stdout, stderr bytes.Buffer

		command := exec.Command(cliBinary, "module", "run",
			"--platform-version", "2.19.0",
			"--kubeconfig", "/nonexistent/kubeconfig")
		command.Stdout = &stdout
		command.Stderr = &stderr

		exitCode := run.ExitSuccess
		if err := command.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = run.ExitFailure
			}
		}

		g.Expect(exitCode).To(Equal(run.ExitUsage))
		g.Expect(stdout.String()).To(BeEmpty())
		g.Expect(stderr.String()).To(ContainSubstring(`"config"`))
	})

	t.Run("missing platform version", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var stdout, stderr bytes.Buffer

		command := exec.Command(cliBinary, "module", "run",
			"--config", "/nonexistent/definition.yaml",
			"--kubeconfig", "/nonexistent/kubeconfig")
		command.Stdout = &stdout
		command.Stderr = &stderr

		exitCode := run.ExitSuccess
		if err := command.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = run.ExitFailure
			}
		}

		g.Expect(exitCode).To(Equal(run.ExitUsage))
		g.Expect(stdout.String()).To(BeEmpty())
		g.Expect(stderr.String()).To(ContainSubstring(`"platform-version"`))
	})
}

// TestCLIRunLocalDefinitionFailure proves an invalid definition is a
// usage-classified failure reported before any cluster contact.
func TestCLIRunLocalDefinitionFailure(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", legacyTypeDefinitionYAML)

	stdout, stderr, exitCode := runCLIRun(root, "--output", "json")

	g.Expect(exitCode).To(Equal(run.ExitUsage))
	g.Expect(stderr).To(ContainSubstring(run.FailureDefinition))

	var report run.Report
	g.Expect(json.Unmarshal([]byte(stdout), &report)).To(Succeed())
	g.Expect(report.Status).To(Equal(run.StatusFailed))
	g.Expect(report.Failure).NotTo(BeNil())
	g.Expect(report.Failure.Category).To(Equal(run.FailureDefinition))
	g.Expect(report.Owner).To(BeNil())
}

// TestCLIRunImageFileFailure proves an explicit but unreadable
// related-images file is a usage-classified failure that stops the
// run before any cluster contact — the unreachable kubeconfig never
// matters.
func TestCLIRunImageFileFailure(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunHelmModule(t, root)

	stdout, stderr, exitCode := runCLIRun(root,
		"--output", "json",
		"--related-images-file", filepath.Join(root, "nonexistent.env"))

	g.Expect(exitCode).To(Equal(run.ExitUsage))
	g.Expect(stderr).To(ContainSubstring(run.FailureImageFile))

	var report run.Report
	g.Expect(json.Unmarshal([]byte(stdout), &report)).To(Succeed())
	g.Expect(report.Status).To(Equal(run.StatusFailed))
	g.Expect(report.Failure).NotTo(BeNil())
	g.Expect(report.Failure.Category).To(Equal(run.FailureImageFile))
	g.Expect(report.Owner).To(BeNil())
}

// TestLintChainIgnoresImageSources proves lint isolation despite
// related-image sources: an ambient related-image value and a
// malformed related-images file present do not affect a passing
// lint — lint never reads image sources. (No t.Parallel: the
// environment is process-global.)
func TestLintChainIgnoresImageSources(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("RELATED_IMAGE_ALPHA", "quay.io/example/alpha:v3")

	root := t.TempDir()
	writeHelmModule(t, root)

	malformed := filepath.Join(root, "images.env")
	if err := os.WriteFile(malformed, []byte("this is not dotenv content \x00\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := runLint(t, root)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Diagnostics).To(BeEmpty())
}
