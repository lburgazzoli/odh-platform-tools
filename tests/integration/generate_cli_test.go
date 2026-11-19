//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"

	. "github.com/onsi/gomega"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
)

// runGenerate executes the built CLI's generate command in one working
// directory — the invocation cwd the relative paths resolve against —
// returning stdout, stderr, and the exit code.
func runGenerate(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()

	return runGenerateEnv(t, dir, nil, args...)
}

// runGenerateEnv executes the generate command with an augmented
// environment.
func runGenerateEnv(
	t *testing.T,
	dir string,
	env []string,
	args ...string,
) (string, string, int) {
	t.Helper()

	full := append([]string{"module", "generate"}, args...)

	var stdout, stderr bytes.Buffer

	command := exec.Command(cliBinary, full...)
	command.Dir = dir
	command.Env = append(os.Environ(), env...)
	command.Stdout = &stdout
	command.Stderr = &stderr

	exitCode := 0
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return stdout.String(), stderr.String(), exitCode
}

// runGenerateLint executes the built CLI's lint command over one
// generated definition file, returning the exit code.
func runGenerateLint(t *testing.T, dir string, config string) int {
	t.Helper()

	command := exec.Command(cliBinary,
		"module", "lint", "--config", config)
	command.Dir = dir

	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}

		return 1
	}

	return 0
}

// loadGeneratedDocument decodes one generated definition file with
// the current loader.
func loadGeneratedDocument(t *testing.T, path string) toolApi.PlatformModuleDefinition {
	t.Helper()

	definition, err := loader.LoadFromPath(context.Background(), path)
	if err != nil {
		t.Fatalf("load generated definition %s: %v", path, err)
	}

	return definition
}

// snapshotTree captures the relative paths and contents of every
// regular file under one root.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()

	snapshot := map[string]string{}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		snapshot[relative] = string(contents)

		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}

	return snapshot
}

// writeGeneratedStdout persists one stdout result as the invocation
// cwd's definition file, the way a shell redirect would.
func writeGeneratedStdout(t *testing.T, dir string, document string) string {
	t.Helper()

	path := filepath.Join(dir, "definition.yaml")
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

// decodeJSONValue decodes one literal JSON value; the loader decodes
// explicit nulls into a zero Raw payload.
func decodeJSONValue(t *testing.T, g *WithT, raw []byte) any {
	t.Helper()

	if len(raw) == 0 {
		return nil
	}

	var value any
	g.Expect(jsonv2.Unmarshal(raw, &value)).To(Succeed())

	return value
}

// TestGenerateCLIHelmRoundTrip proves the built CLI infers a complete
// definition from a real Helm chart without a cluster: the inferred
// name, GVK, and controller selection load with the current loader
// and pass the built lint command. A nonexistent kubeconfig proves no
// cluster resolution happens.
func TestGenerateCLIHelmRoundTrip(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	writeGenerateChart(t, filepath.Join(base, "chart"),
		helmDeploymentTemplateYAML, moduleCRDYAML, nil)

	// A missing kubeconfig proves generation never resolves a cluster.
	stdout, _, code := runGenerateEnv(t, base,
		[]string{"KUBECONFIG=" + filepath.Join(base, "missing-kubeconfig")},
		"chart")

	g.Expect(code).To(Equal(0))

	path := writeGeneratedStdout(t, base, stdout)
	definition := loadGeneratedDocument(t, path)

	g.Expect(definition.Name).To(Equal("syntheticmodule"))
	g.Expect(definition.Spec.Instance.Name).To(Equal("syntheticmodule"))
	g.Expect(definition.Spec.Instance.Type).To(Equal(toolApi.ModuleType{
		Group:   "components.platform.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "SyntheticModule",
	}))
	g.Expect(definition.Spec.Manifests.Format).To(Equal(toolApi.FormatHelm))
	g.Expect(definition.Spec.Manifests.Helm.Path).To(Equal("chart"))
	g.Expect(definition.Spec.Controller.Deployment.Name).To(Equal("synthetic-controller"))
	g.Expect(definition.Spec.Controller.Deployment.Container.Name).
		To(Equal(toolApi.DefaultMainContainerName))
	g.Expect(definition.Spec.Controller.Deployment.Container.Image).
		To(Equal("quay.io/chart/synthetic-controller:v9"))

	g.Expect(runGenerateLint(t, base, "definition.yaml")).To(Equal(0))
}

// TestGenerateCLIReleaseNameBindsFinalRender proves a chart binding
// its controller identity to .Release.Name yields a definition that
// selects the Deployment and image rendered under the final module
// name, inferred or explicit — never the bootstrap release name.
func TestGenerateCLIReleaseNameBindsFinalRender(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		explicit         []string
		module           string
		deploymentSuffix string
	}{
		{name: "inferred name", module: "syntheticmodule"},
		{name: "explicit name", explicit: []string{"--name", "demo"}, module: "demo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			base := t.TempDir()
			writeGenerateChart(t, filepath.Join(base, "chart"),
				generateReleaseDeploymentTemplateYAML, moduleCRDYAML, nil)

			args := append([]string(nil), tc.explicit...)
			stdout, _, code := runGenerate(t, base, append(args, "chart")...)
			g.Expect(code).To(Equal(0))

			path := writeGeneratedStdout(t, base, stdout)
			definition := loadGeneratedDocument(t, path)

			g.Expect(definition.Name).To(Equal(tc.module))
			g.Expect(definition.Spec.Controller.Deployment.Name).
				To(Equal(tc.module + "-controller"))
			g.Expect(definition.Spec.Controller.Deployment.Container.Image).
				To(Equal("quay.io/example/" + tc.module + ":v1"))

			g.Expect(runGenerateLint(t, base, "definition.yaml")).To(Equal(0))
		})
	}
}

// TestGenerateCLIStdoutEquivalence proves the default stdout output
// and an explicit --output stdout produce identical bytes, and the
// saved document resolves its relative source path under lint.
func TestGenerateCLIStdoutEquivalence(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	writeGenerateChart(t, filepath.Join(base, "chart"),
		helmDeploymentTemplateYAML, moduleCRDYAML, nil)

	defaulted, _, code := runGenerate(t, base, "chart")
	g.Expect(code).To(Equal(0))

	explicit, _, code := runGenerate(t, base, "--output", "stdout", "chart")
	g.Expect(code).To(Equal(0))

	g.Expect(explicit).To(Equal(defaulted),
		"stdout results must be byte-for-byte equivalent")

	writeGeneratedStdout(t, base, defaulted)
	g.Expect(runGenerateLint(t, base, "definition.yaml")).To(Equal(0))
}

// TestGenerateCLIKustomizeRoundTrip proves Kustomize generation
// infers the definition, colocated file output succeeds without any
// Helm ignore policy, and both forms pass lint.
func TestGenerateCLIKustomizeRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("stdout", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		base := t.TempDir()
		writeGenerateKustomizeSource(t, filepath.Join(base, "kust"))

		stdout, _, code := runGenerate(t, base, "kust")
		g.Expect(code).To(Equal(0))

		path := writeGeneratedStdout(t, base, stdout)
		definition := loadGeneratedDocument(t, path)

		g.Expect(definition.Name).To(Equal("syntheticmodule"))
		g.Expect(definition.Spec.Manifests.Format).To(Equal(toolApi.FormatKustomize))
		g.Expect(definition.Spec.Manifests.Kustomize.Path).To(Equal("kust"))

		g.Expect(runGenerateLint(t, base, "definition.yaml")).To(Equal(0))
	})

	t.Run("colocated output", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		base := t.TempDir()
		writeGenerateKustomizeSource(t, filepath.Join(base, "kust"))

		output := filepath.Join(base, "kust", "module.yaml")

		stdout, _, code := runGenerate(t, base, "--output", output, "kust")
		g.Expect(code).To(Equal(0))
		g.Expect(stdout).To(BeEmpty(), "file success emits no stdout")

		definition := loadGeneratedDocument(t, output)
		g.Expect(definition.Spec.Manifests.Kustomize).To(BeNil(),
			"a colocated definition omits the manifests path")

		g.Expect(runGenerateLint(t, base, output)).To(Equal(0))
	})
}

// TestGenerateCLIValuesAndRelatedImages proves file plus repeatable
// flag overrides reach the generated values with booleans, numbers,
// nested structures, and nulls preserved, and related-image names are
// sorted and deduplicated without their values.
func TestGenerateCLIValuesAndRelatedImages(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	writeGenerateChart(t, filepath.Join(base, "chart"),
		generateTaggedDeploymentTemplateYAML, moduleCRDYAML, nil)

	valuesFile := filepath.Join(base, "values.yaml")
	writeModuleFile(t, base, "values.yaml", generateOverrideValuesYAML)

	imagesFile := filepath.Join(base, "images.yaml")
	writeModuleFile(t, base, "images.yaml", generateRelatedImagesYAML)

	stdout, _, code := runGenerate(t, base,
		"--values-file", valuesFile,
		"--values", "count=7",
		"--values", `nested.items[0]=first`,
		"--values", "tag=v9",
		"--related-images-file", imagesFile,
		"chart")
	g.Expect(code).To(Equal(0))

	path := writeGeneratedStdout(t, base, stdout)
	definition := loadGeneratedDocument(t, path)

	g.Expect(definition.Spec.Controller.Deployment.Container.Image).
		To(Equal("quay.io/chart/synthetic-controller:v9"),
			"the merged tag override must shape the discovered controller image")

	values := definition.Spec.Manifests.Helm.Values
	g.Expect(values).To(HaveKey("enabled"))
	g.Expect(decodeJSONValue(t, g, values["enabled"].Raw)).To(Equal(true))
	g.Expect(decodeJSONValue(t, g, values["count"].Raw)).To(Equal(float64(7)),
		"the later flag override wins over the file")
	g.Expect(decodeJSONValue(t, g, values["empty"].Raw)).To(BeNil())
	g.Expect(decodeJSONValue(t, g, values["nested"].Raw)).To(Equal(map[string]any{
		"name":  "demo",
		"items": []any{"first", "two"},
	}))

	g.Expect(definition.Spec.RelatedImages).To(Equal([]string{"alpha-image", "zeta-image"}))
	g.Expect(stdout).NotTo(ContainSubstring("secret-"),
		"related-image values must never be embedded")
}

// TestGenerateCLIInitContainerSelection proves init containers require
// an explicit selection and an exact name among several succeeds.
func TestGenerateCLIInitContainerSelection(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	writeGenerateChart(t, filepath.Join(base, "chart"),
		generateInitDeploymentTemplateYAML, moduleCRDYAML, nil)

	t.Run("missing selection", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		output := filepath.Join(base, "module.yaml")

		stdout, stderr, code := runGenerate(t, base, "--output", output, "chart")

		g.Expect(code).To(Equal(2))
		g.Expect(stdout).To(BeEmpty())
		g.Expect(stderr).To(ContainSubstring("init container"))
		g.Expect(stderr).To(ContainSubstring(": invalid-options:"))

		if _, statErr := os.Stat(output); statErr == nil {
			t.Fatal("no destination may be created by a failed generation")
		}
	})

	t.Run("exact selection", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		stdout, _, code := runGenerate(t, base,
			"--init-container", "second-setup", "chart")
		g.Expect(code).To(Equal(0))

		path := writeGeneratedStdout(t, base, stdout)
		definition := loadGeneratedDocument(t, path)

		init := definition.Spec.Controller.Deployment.InitContainer
		g.Expect(init).NotTo(BeNil())
		g.Expect(init.Name).To(Equal("second-setup"))
		g.Expect(init.Image).To(Equal("quay.io/example/setup:v2"))
	})

	t.Run("nonexistent selection", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		stdout, _, code := runGenerate(t, base,
			"--init-container", "missing-setup", "chart")

		g.Expect(code).To(Equal(2))
		g.Expect(stdout).To(BeEmpty())
	})
}

// TestGenerateCLIBundleFailures proves ambiguous or malformed bundles
// fail with the shared classification, empty stdout, and no
// destination file.
func TestGenerateCLIBundleFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		deploy   string
		crd      string
		extra    map[string]string
		code     int
		category string
	}{
		{
			name:     "missing crd",
			deploy:   helmDeploymentTemplateYAML,
			code:     2,
			category: "definition",
		},
		{
			name:     "two crds",
			deploy:   helmDeploymentTemplateYAML,
			crd:      moduleCRDYAML,
			extra:    map[string]string{"extra-crd.yaml": generateExtraCRDYAML},
			code:     2,
			category: "definition",
		},
		{
			name:     "missing deployment",
			crd:      moduleCRDYAML,
			code:     2,
			category: "definition",
		},
		{
			name:     "two deployments",
			deploy:   helmDeploymentTemplateYAML,
			crd:      moduleCRDYAML,
			extra:    map[string]string{"extra-deployment.yaml": generateExtraDeploymentTemplateYAML},
			code:     2,
			category: "definition",
		},
		{
			name:     "ambiguous containers",
			deploy:   generateAmbiguousDeploymentTemplateYAML,
			crd:      moduleCRDYAML,
			code:     2,
			category: "definition",
		},
		{
			name:     "two storage versions",
			deploy:   helmDeploymentTemplateYAML,
			crd:      generateDualStorageCRDYAML,
			code:     2,
			category: "definition",
		},
		{
			name:     "reserved module instance",
			deploy:   helmDeploymentTemplateYAML,
			crd:      moduleCRDYAML,
			extra:    map[string]string{"instance.yaml": generateReservedInstanceTemplateYAML},
			code:     1,
			category: "reserved",
		},
		{
			name:     "disallowed kind finding",
			deploy:   helmDeploymentTemplateYAML,
			crd:      moduleCRDYAML,
			extra:    map[string]string{"namespace.yaml": namespaceTemplateYAML},
			code:     1,
			category: "validation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			base := t.TempDir()
			writeGenerateChart(t, filepath.Join(base, "chart"), tc.deploy, tc.crd, tc.extra)

			output := filepath.Join(base, "out", "module.yaml")
			if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
				t.Fatal(err)
			}

			stdout, stderr, code := runGenerate(t, base, "--output", output, "chart")

			g.Expect(code).To(Equal(tc.code))
			g.Expect(stdout).To(BeEmpty(), "no invalid definition may be emitted")
			g.Expect(stderr).To(ContainSubstring(": " + tc.category + ":"))

			if _, statErr := os.Stat(output); statErr == nil {
				t.Fatal("a failed generation must not create its destination")
			}
		})
	}
}

// TestGenerateCLIFileOutput proves --output in another existing
// directory works — including paths with spaces — emits no stdout,
// publishes 0600 bytes once, and never clobbers existing files or
// symlink targets.
func TestGenerateCLIFileOutput(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	writeGenerateChart(t, filepath.Join(base, "chart"),
		helmDeploymentTemplateYAML, moduleCRDYAML, nil)

	outDir := filepath.Join(base, "out dir")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(outDir, "module.yaml")

	stdout, _, code := runGenerate(t, base, "--output", output, "chart")
	g.Expect(code).To(Equal(0))
	g.Expect(stdout).To(BeEmpty())

	info, err := os.Stat(output)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

	definition := loadGeneratedDocument(t, output)
	g.Expect(definition.Spec.Manifests.Helm.Path).To(Equal(filepath.Join("..", "chart")),
		"the manifests path is relative to the definition directory")

	g.Expect(runGenerateLint(t, base, output)).To(Equal(0))

	t.Run("existing destination", func(t *testing.T) {
		g := NewWithT(t)

		g.Expect(os.WriteFile(output, []byte("existing bytes\n"), 0o644)).To(Succeed())

		_, stderr, code := runGenerate(t, base, "--output", output, "chart")

		g.Expect(code).To(Equal(1))
		g.Expect(stderr).To(ContainSubstring(": output:"))

		contents, err := os.ReadFile(output)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(string(contents)).To(Equal("existing bytes\n"))
	})

	t.Run("symlink destination", func(t *testing.T) {
		g := NewWithT(t)

		target := filepath.Join(outDir, "target")
		g.Expect(os.WriteFile(target, []byte("target bytes\n"), 0o644)).To(Succeed())

		symlink := filepath.Join(outDir, "link.yaml")
		g.Expect(os.Symlink(target, symlink)).To(Succeed())

		_, _, code := runGenerate(t, base, "--output", symlink, "chart")

		g.Expect(code).To(Equal(1))

		contents, err := os.ReadFile(target)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(string(contents)).To(Equal("target bytes\n"))
	})
}

// TestGenerateCLIHelmIgnoreGuard proves a chart consuming .Files.Glob
// rejects unignored inside-chart destinations without creating them,
// while an existing .helmignore exclusion permits colocated output
// that still passes lint, with the ignore file unchanged.
func TestGenerateCLIHelmIgnoreGuard(t *testing.T) {
	t.Parallel()

	newBase := func(t *testing.T) string {
		t.Helper()

		base := t.TempDir()
		chart := filepath.Join(base, "chart")
		writeGenerateChart(t, chart, generateGlobDeploymentTemplateYAML, moduleCRDYAML, nil)
		writeModuleFile(t, chart, "extra/config.yaml", generateExtraConfigYAML)

		return base
	}

	for _, tc := range []struct {
		name     string
		ignore   string
		output   string
		allowed  bool
		prepared func(t *testing.T, chart string)
	}{
		{
			name:   "unignored chart root",
			output: "gen.yaml",
		},
		{
			name:   "unignored globbed directory",
			output: "extra/gen.yaml",
		},
		{
			name:   "unignored templates directory",
			output: "templates/gen.yaml",
		},
		{
			name:    "exact file rule",
			ignore:  generateIgnoreFileYAML,
			output:  "gen.yaml",
			allowed: true,
		},
		{
			name:    "ancestor directory rule",
			ignore:  generateIgnoreAncestorYAML,
			output:  "out/module.yaml",
			allowed: true,
			prepared: func(t *testing.T, chart string) {
				t.Helper()

				if err := os.MkdirAll(filepath.Join(chart, "out"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			base := newBase(t)
			chart := filepath.Join(base, "chart")

			if tc.ignore != "" {
				writeModuleFile(t, chart, ".helmignore", tc.ignore)
			}

			if tc.prepared != nil {
				tc.prepared(t, chart)
			}

			output := filepath.Join(chart, filepath.FromSlash(tc.output))

			stdout, stderr, code := runGenerate(t, base, "--output", output, "chart")

			if !tc.allowed {
				g.Expect(code).To(Equal(2))
				g.Expect(stdout).To(BeEmpty())
				g.Expect(stderr).To(ContainSubstring(": invalid-options:"))

				if _, statErr := os.Stat(output); statErr == nil {
					t.Fatal("a rejected destination must not be created")
				}

				return
			}

			g.Expect(code).To(Equal(0))
			g.Expect(stdout).To(BeEmpty())

			_, statErr := os.Stat(output)
			g.Expect(statErr).NotTo(HaveOccurred())

			g.Expect(runGenerateLint(t, base, output)).To(Equal(0))

			if tc.ignore != "" {
				contents, err := os.ReadFile(filepath.Join(chart, ".helmignore"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(contents)).To(Equal(tc.ignore),
					"generation must never edit .helmignore")
			}
		})
	}
}

// TestGenerateCLIRejectsBadInputs proves Kustomize values, malformed
// values files, and malformed image files fail with exit 2 without
// exposing their sensitive contents.
func TestGenerateCLIRejectsBadInputs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		setup     func(t *testing.T, base string)
		args      []string
		secret    string
		category  string
		kustomize bool
	}{
		{
			name:      "values on kustomize",
			kustomize: true,
			args:      []string{"--values", "a=b"},
			category:  "invalid-options",
		},
		{
			name: "malformed values file",
			setup: func(t *testing.T, base string) {
				t.Helper()

				writeModuleFile(t, base, "bad-values.yaml", generateMalformedValuesYAML)
			},
			args:     []string{"--values-file", "bad-values.yaml"},
			secret:   "sensitive-token-123",
			category: "definition",
		},
		{
			name: "malformed image file",
			setup: func(t *testing.T, base string) {
				t.Helper()

				writeModuleFile(t, base, "bad-images.yaml", generateMalformedImagesYAML)
			},
			args:     []string{"--related-images-file", "bad-images.yaml"},
			secret:   "sensitive-image-payload",
			category: "image-file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			base := t.TempDir()

			if tc.kustomize {
				writeGenerateKustomizeSource(t, filepath.Join(base, "kust"))
			} else {
				writeGenerateChart(t, filepath.Join(base, "chart"),
					helmDeploymentTemplateYAML, moduleCRDYAML, nil)
			}

			if tc.setup != nil {
				tc.setup(t, base)
			}

			source := "chart"
			if tc.kustomize {
				source = "kust"
			}

			args := append([]string(nil), tc.args...)
			stdout, stderr, code := runGenerate(t, base, append(args, source)...)

			g.Expect(code).To(Equal(2))
			g.Expect(stdout).To(BeEmpty())
			g.Expect(stderr).To(ContainSubstring(": " + tc.category + ":"))

			if tc.secret != "" {
				g.Expect(stderr).NotTo(ContainSubstring(tc.secret),
					"sensitive input contents must never surface")
			}
		})
	}
}

// TestGenerateCLIUsageFailures proves flag-occurrence and
// explicit-empty violations, and an invalid explicit name, fail with
// the shared exit-2 classification before any render.
func TestGenerateCLIUsageFailures(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	writeGenerateChart(t, filepath.Join(base, "chart"),
		helmDeploymentTemplateYAML, moduleCRDYAML, nil)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "repeated values file",
			args: []string{"--values-file", "a.yaml", "--values-file", "b.yaml"},
		},
		{
			name: "empty values file",
			args: []string{"--values-file", ""},
		},
		{
			name: "empty name",
			args: []string{"--name", ""},
		},
		{
			name: "empty init container",
			args: []string{"--init-container", ""},
		},
		{
			name: "empty output",
			args: []string{"--output", ""},
		},
		{
			name: "invalid explicit name",
			args: []string{"--name", "Bad"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			args := append([]string(nil), tc.args...)
			stdout, _, code := runGenerate(t, base, append(args, "chart")...)

			g.Expect(code).To(Equal(2))
			g.Expect(stdout).To(BeEmpty())
		})
	}
}

// TestGenerateCLISourcesUnchangedAndStable proves repeated safe stdout
// runs produce identical bytes and leave the source tree untouched.
func TestGenerateCLISourcesUnchangedAndStable(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	chart := filepath.Join(base, "chart")
	writeGenerateChart(t, chart, helmDeploymentTemplateYAML, moduleCRDYAML, nil)

	before := snapshotTree(t, chart)

	first, _, code := runGenerate(t, base, "chart")
	g.Expect(code).To(Equal(0))

	second, _, code := runGenerate(t, base, "chart")
	g.Expect(code).To(Equal(0))

	g.Expect(second).To(Equal(first), "stdout runs must be deterministic")

	g.Expect(snapshotTree(t, chart)).To(Equal(before),
		"generation must not modify its source tree")
}

// TestGenerateCLIFileOutputInSourceRoot proves a Helm chart-root
// destination without an ignore rule is rejected even when the source
// is addressed absolutely, and the unutilsed path never appears.
func TestGenerateCLIFileOutputInSourceRoot(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	chart := filepath.Join(base, "chart")
	writeGenerateChart(t, chart, helmDeploymentTemplateYAML, moduleCRDYAML, nil)

	output := filepath.Join(chart, "module.yaml")

	_, stderr, code := runGenerate(t, base, "--output", output, chart)

	g.Expect(code).To(Equal(2))
	g.Expect(stderr).To(ContainSubstring(": invalid-options:"))
	g.Expect(strings.Contains(stderr, "module-discovery")).To(BeFalse())

	if _, statErr := os.Stat(output); statErr == nil {
		t.Fatal("a rejected chart-root destination must not be created")
	}
}

// TestGenerateCLIWithholdsRendererSecrets proves a native renderer
// error quoting values content through tpl is rewrapped with a
// sanitized stage message: the classified render failure reaches
// stderr without the values themselves.
func TestGenerateCLIWithholdsRendererSecrets(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	writeGenerateChart(t, filepath.Join(base, "chart"),
		generateLeakDeploymentTemplateYAML, moduleCRDYAML, nil)

	valuesFile := filepath.Join(base, "values.yaml")
	writeModuleFile(t, base, "values.yaml", generateSecretValuesYAML)

	stdout, stderr, code := runGenerate(t, base, "--values-file", valuesFile, "chart")

	g.Expect(code).To(Equal(1))
	g.Expect(stdout).To(BeEmpty())
	g.Expect(stderr).To(ContainSubstring(": render:"))
	g.Expect(stderr).NotTo(ContainSubstring("sensitive-render-token"),
		"native renderer text quoting values must not reach the wrapper diagnostic")
	g.Expect(stderr).NotTo(ContainSubstring("cannot parse template"),
		"native renderer text must be withheld entirely")
}

// TestGenerateCLIRejectsSymlinkedChartDirs proves a destination under
// a directory a chart-internal symlink points at is chart content:
// the guard rejects it without creating it, because the pinned
// loader follows directory symlinks.
func TestGenerateCLIRejectsSymlinkedChartDirs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	chart := filepath.Join(base, "chart")
	writeGenerateChart(t, chart, helmDeploymentTemplateYAML, moduleCRDYAML, nil)

	shared := filepath.Join(base, "shared")
	g.Expect(os.MkdirAll(shared, 0o755)).To(Succeed())
	g.Expect(os.Symlink(shared, filepath.Join(chart, "templates", "shared"))).To(Succeed())

	output := filepath.Join(shared, "module.yaml")

	stdout, stderr, code := runGenerate(t, base, "--output", output, "chart")

	g.Expect(code).To(Equal(2))
	g.Expect(stdout).To(BeEmpty())
	g.Expect(stderr).To(ContainSubstring(": invalid-options:"))

	if _, statErr := os.Stat(output); statErr == nil {
		t.Fatal("a destination consumed through a chart symlink must not be created")
	}
}
