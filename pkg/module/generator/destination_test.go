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

package generator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	. "github.com/onsi/gomega"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// newChart creates a chart root with a templates directory and the
// given .helmignore content, returning the chart root.
func newChart(t *testing.T, helmignore string) string {
	t.Helper()

	chart := t.TempDir()

	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatalf("create templates: %v", err)
	}

	if helmignore != "" {
		if err := os.WriteFile(filepath.Join(chart, ".helmignore"), []byte(helmignore), 0o644); err != nil {
			t.Fatalf("write helmignore: %v", err)
		}
	}

	return chart
}

// TestResolveOutputNormalizesDestinations proves empty and stdout
// select the stream while any other value selects a file.
func TestResolveOutputNormalizesDestinations(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	for _, output := range []string{"", "stdout"} {
		resolved, err := resolveOutput(output)

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(resolved.file).To(BeFalse())
	}

	resolved, err := resolveOutput(filepath.Join("some", "dir", "module.yaml"))

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resolved.file).To(BeTrue())
	g.Expect(filepath.IsAbs(resolved.path)).To(BeTrue())
}

// TestEnsureDestinationParentRequiresExistingDirectory proves a
// missing parent is an output failure and no directory is created.
func TestEnsureDestinationParentRequiresExistingDirectory(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	missing := filepath.Join(base, "missing", "module.yaml")

	err := ensureDestinationParent(missing)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrGenerateOutput)).To(BeTrue())

	if _, statErr := os.Stat(filepath.Join(base, "missing")); statErr == nil {
		t.Fatal("publication must not create destination directories")
	}
}

// TestEnsureDestinationParentRejectsFileParents proves a parent that
// is a regular file is an output failure.
func TestEnsureDestinationParentRejectsFileParents(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	parent := filepath.Join(base, "plain-file")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatalf("write parent file: %v", err)
	}

	err := ensureDestinationParent(filepath.Join(parent, "module.yaml"))

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrGenerateOutput)).To(BeTrue())
}

// TestGuardChartDestinationAllowsSafeDestinations proves destinations
// outside the chart and inside-chart destinations excluded by an
// existing .helmignore rule are allowed.
func TestGuardChartDestinationAllowsSafeDestinations(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		helmignore string
		relative   string
	}{
		{name: "outside the chart", helmignore: "", relative: ""},
		{
			name:       "exact file rule",
			helmignore: "generated.yaml\n",
			relative:   "generated.yaml",
		},
		{
			name:       "ancestor directory rule",
			helmignore: "out/\n",
			relative:   "out/module.yaml",
		},
		{
			name:       "ignored templates dotfiles",
			helmignore: "",
			relative:   "templates/.generated.yaml",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			chart := newChart(t, tc.helmignore)

			base := t.TempDir()
			dest := filepath.Join(base, "module.yaml")
			if tc.relative != "" {
				dest = filepath.Join(chart, filepath.FromSlash(tc.relative))

				// The destination parent always exists in the
				// production flow: ensureDestinationParent runs
				// before the guard.
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					t.Fatalf("create destination parent: %v", err)
				}
			}

			g.Expect(guardChartDestination(chart, dest)).To(Succeed())
		})
	}
}

// TestGuardChartDestinationRejectsUnignoredPaths proves chart-root,
// templates, and CRD destinations without an applicable rule fail
// with the options category.
func TestGuardChartDestinationRejectsUnignoredPaths(t *testing.T) {
	t.Parallel()

	for _, relative := range []string{
		"generated.yaml",
		"templates/generated.yaml",
		"crds/generated.yaml",
	} {
		t.Run(relative, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			chart := newChart(t, "")
			dest := filepath.Join(chart, filepath.FromSlash(relative))

			err := guardChartDestination(chart, dest)

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue())
		})
	}
}

// TestGuardChartDestinationRejectsPartialIgnores proves a rule
// covering one unignored destination shape does not silence the
// others.
func TestGuardChartDestinationRejectsPartialIgnores(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	chart := newChart(t, "crds/\n")

	err := guardChartDestination(chart, filepath.Join(chart, "generated.yaml"))

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue())
}

// TestGuardChartDestinationRejectsMalformedIgnoreFile proves a
// .helmignore the pinned parser rejects is a definition failure, and
// the file is left unchanged.
func TestGuardChartDestinationRejectsMalformedIgnoreFile(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	chart := newChart(t, "**\n")
	ignoreFile := filepath.Join(chart, ".helmignore")

	err := guardChartDestination(chart, filepath.Join(chart, "generated.yaml"))

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())

	contents, readErr := os.ReadFile(ignoreFile)
	g.Expect(readErr).NotTo(HaveOccurred())
	g.Expect(string(contents)).To(Equal("**\n"))
}

// TestGuardChartDestinationResolvesSymlinkAliases proves a destination
// reached through a symlinked directory cannot bypass the chart
// inclusion check, and an unusable chart root fails as indeterminate
// rather than assumed safe.
func TestGuardChartDestinationResolvesSymlinkAliases(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	chart := newChart(t, "")
	link := filepath.Join(base, "chart-link")

	if err := os.Symlink(chart, link); err != nil {
		t.Fatalf("symlink chart: %v", err)
	}

	err := guardChartDestination(chart, filepath.Join(link, "generated.yaml"))

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue(),
		"a symlinked parent must not bypass chart inclusion")

	err = guardChartDestination(
		filepath.Join(base, "missing-chart"),
		filepath.Join(base, "module.yaml"))

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue(),
		"an unusable chart root must fail as indeterminate")
}

// TestPublishFileInstallsCreateOnly proves publication writes 0600
// bytes with no-replace semantics: an existing destination, including
// a directory or symlink, is never touched.
func TestPublishFileInstallsCreateOnly(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	dir := t.TempDir()
	dest := filepath.Join(dir, "module.yaml")
	document := []byte("kind: PlatformModuleDefinition\n")

	g.Expect(publishFile(context.Background(), dest, document)).To(Succeed())

	contents, err := os.ReadFile(dest)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(contents).To(Equal(document))

	info, err := os.Stat(dest)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

	g.Expect(publishFile(context.Background(), dest, []byte("second"))).NotTo(Succeed())

	contents, err = os.ReadFile(dest)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(contents).To(Equal(document), "an existing destination must keep its bytes")

	directory := filepath.Join(dir, "as-directory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("create directory: %v", err)
	}

	g.Expect(publishFile(context.Background(), directory, document)).NotTo(Succeed())

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	symlink := filepath.Join(dir, "as-symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("symlink target: %v", err)
	}

	g.Expect(publishFile(context.Background(), symlink, document)).NotTo(Succeed())

	contents, err = os.ReadFile(symlink)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(contents).To(Equal([]byte("target")), "a symlinked destination must not be followed")
}

// TestPublishFileSupportsSpacedPaths proves destinations containing
// spaces publish normally.
func TestPublishFileSupportsSpacedPaths(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	dir := filepath.Join(t.TempDir(), "output dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("create spaced dir: %v", err)
	}

	dest := filepath.Join(dir, "module definition.yaml")

	g.Expect(publishFile(context.Background(), dest, []byte("kind: x\n"))).To(Succeed())

	_, err := os.Stat(dest)
	g.Expect(err).NotTo(HaveOccurred())
}

// TestPublishFileHonorsCancellationBeforeStaging proves a cancelled
// context prevents publication entirely.
func TestPublishFileHonorsCancellationBeforeStaging(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	dest := filepath.Join(dir, "module.yaml")

	err := publishFile(ctx, dest, []byte("kind: x\n"))

	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("a cancelled publication must not create the destination")
	}

	entries, readErr := os.ReadDir(dir)
	g.Expect(readErr).NotTo(HaveOccurred())
	g.Expect(entries).To(BeEmpty(), "a cancelled publication must leave no staged files")
}

// TestPublishFileLeavesNoStagedFilesOnFailure proves a failed
// installation cleans its staging file instead of installing partial
// bytes.
func TestPublishFileLeavesNoStagedFilesOnFailure(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	dir := t.TempDir()
	dest := filepath.Join(dir, "module.yaml")

	if err := os.WriteFile(dest, []byte("existing"), 0o644); err != nil {
		t.Fatalf("write existing destination: %v", err)
	}

	g.Expect(publishFile(context.Background(), dest, []byte("kind: x\n"))).NotTo(Succeed())

	entries, err := os.ReadDir(dir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(entries).To(HaveLen(1), "only the pre-existing destination may remain")

	contents, err := os.ReadFile(dest)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(contents).To(Equal([]byte("existing")))
}

// cancellationAfterStaging cancels at the precommit checkpoint: the
// first context check passes and the second fails.
type cancellationAfterStaging struct {
	context.Context
	checks int
}

// Err reports cancellation from the second checkpoint on.
func (c *cancellationAfterStaging) Err() error {
	c.checks++
	if c.checks >= 2 {
		return context.Canceled
	}

	return nil
}

// TestPublishFileHonorsCancellationBeforeInstallation proves a
// cancellation observed after staging but before the no-replace
// installation still prevents publication and removes the staging
// file.
func TestPublishFileHonorsCancellationBeforeInstallation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	dir := t.TempDir()
	dest := filepath.Join(dir, "module.yaml")

	ctx := &cancellationAfterStaging{Context: context.Background()}

	err := publishFile(ctx, dest, []byte("kind: x\n"))

	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	g.Expect(ctx.checks).To(Equal(2), "both checkpoints must be observed")

	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("a cancelled publication must not create the destination")
	}

	entries, readErr := os.ReadDir(dir)
	g.Expect(readErr).NotTo(HaveOccurred())
	g.Expect(entries).To(BeEmpty(), "a cancelled publication must leave no staged files")
}

// TestGuardChartDestinationRejectsSymlinkedChartDirs proves a
// chart-internal directory symlink makes destinations under its
// external target chart content: unignored coverage rejects, an
// ignored symlink path permits, and a file symlink naming the
// destination rejects.
func TestGuardChartDestinationRejectsSymlinkedChartDirs(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	shared := filepath.Join(base, "shared")

	if err := os.MkdirAll(filepath.Join(base, "shared", "nested"), 0o755); err != nil {
		t.Fatalf("create shared: %v", err)
	}

	t.Run("unignored symlink coverage", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		chart := newChart(t, "")
		if err := os.Symlink(shared, filepath.Join(chart, "templates", "shared")); err != nil {
			t.Fatalf("symlink shared: %v", err)
		}

		for _, dest := range []string{
			filepath.Join(shared, "module.yaml"),
			filepath.Join(shared, "nested", "module.yaml"),
		} {
			err := guardChartDestination(chart, dest)

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue(),
				"coverage through a chart symlink must reject "+dest)
		}
	})

	t.Run("ignored symlink path", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		chart := newChart(t, "templates/shared\n")
		if err := os.Symlink(shared, filepath.Join(chart, "templates", "shared")); err != nil {
			t.Fatalf("symlink shared: %v", err)
		}

		g.Expect(guardChartDestination(chart, filepath.Join(shared, "module.yaml"))).
			To(Succeed(), "an ignored symlink path is never walked by the loader")
	})

	t.Run("ignored coverage under a symlink", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		chart := newChart(t, "templates/shared/module.yaml\n")
		if err := os.Symlink(shared, filepath.Join(chart, "templates", "shared")); err != nil {
			t.Fatalf("symlink shared: %v", err)
		}

		g.Expect(guardChartDestination(chart, filepath.Join(shared, "module.yaml"))).
			To(Succeed(), "a rule excluding the composed path permits the destination")
	})

	t.Run("file symlink naming the destination", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		chart := newChart(t, "")

		dest := filepath.Join(base, "existing.yaml")
		if err := os.WriteFile(dest, []byte("kind: x\n"), 0o644); err != nil {
			t.Fatalf("write dest: %v", err)
		}

		if err := os.Symlink(dest, filepath.Join(chart, "templates", "gen.yaml")); err != nil {
			t.Fatalf("symlink dest: %v", err)
		}

		err := guardChartDestination(chart, dest)

		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue(),
			"a chart file symlink naming the destination must reject")
	})

	t.Run("unrelated external symlink", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		chart := newChart(t, "")
		if err := os.Symlink(shared, filepath.Join(chart, "templates", "shared")); err != nil {
			t.Fatalf("symlink shared: %v", err)
		}

		dest := filepath.Join(base, "outside", "module.yaml")
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatalf("create outside: %v", err)
		}

		g.Expect(guardChartDestination(chart, dest)).
			To(Succeed(), "a destination outside all symlink targets stays allowed")
	})
}

// TestPublishFileLeavesNoStagedFilesOnStagingFailure proves a write
// failure during staging removes the partial staging file: no
// plaintext overrides survive beside the destination. The file size
// limit is process state, so this test runs sequentially, restores
// the previous limit, and must never call t.Parallel.
func TestPublishFileLeavesNoStagedFilesOnStagingFailure(t *testing.T) {
	var before syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &before); err != nil {
		t.Fatalf("get file size limit: %v", err)
	}

	limit := syscall.Rlimit{Cur: 16, Max: before.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatalf("set file size limit: %v", err)
	}

	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &before); err != nil {
			t.Errorf("restore file size limit: %v", err)
		}
	}()

	g := NewWithT(t)

	dir := t.TempDir()
	dest := filepath.Join(dir, "module.yaml")

	err := publishFile(context.Background(), dest, []byte("kind: PlatformModuleDefinition\n"))

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrGenerateOutput)).To(BeTrue())

	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("a failed staging attempt must not create the destination")
	}

	entries, readErr := os.ReadDir(dir)
	g.Expect(readErr).NotTo(HaveOccurred())
	g.Expect(entries).To(BeEmpty(), "a failed staging attempt must leave no staged files")
}
