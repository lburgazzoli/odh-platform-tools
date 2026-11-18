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
	"errors"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// writeSource creates a source directory containing the named
// regular marker files.
func writeSource(t *testing.T, markers ...string) string {
	t.Helper()

	dir := t.TempDir()

	for _, marker := range markers {
		path := filepath.Join(dir, marker)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create marker parent: %v", err)
		}

		if err := os.WriteFile(path, []byte("# marker\n"), 0o644); err != nil {
			t.Fatalf("write marker %s: %v", marker, err)
		}
	}

	return dir
}

// TestResolveSourceDirAcceptsDirectories proves an existing directory
// resolves to its absolute form, including one whose name contains
// spaces.
func TestResolveSourceDirAcceptsDirectories(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	base := t.TempDir()
	spaced := filepath.Join(base, "source dir")
	if err := os.Mkdir(spaced, 0o755); err != nil {
		t.Fatalf("create spaced source: %v", err)
	}

	resolved, err := resolveSourceDir(spaced)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resolved).To(Equal(spaced))
	g.Expect(filepath.IsAbs(resolved)).To(BeTrue())
}

// TestResolveSourceDirRejectsUnusableSources proves a missing source
// and a file source are definition failures.
func TestResolveSourceDirRejectsUnusableSources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	file := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	for _, source := range []string{
		file,
		filepath.Join(t.TempDir(), "missing"),
	} {
		_, err := resolveSourceDir(source)

		g.Expect(err).To(HaveOccurred(),
			"source %s must fail", source)
		g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue(),
			"source %s must be a definition failure", source)
	}
}

// TestDetectBackendRecognizesMarkers proves each single marker family
// selects its backend, with no recursion into subdirectories.
func TestDetectBackendRecognizesMarkers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		marker string
		format toolApi.Format
	}{
		{marker: "Chart.yaml", format: toolApi.FormatHelm},
		{marker: "kustomization.yaml", format: toolApi.FormatKustomize},
		{marker: "kustomization.yml", format: toolApi.FormatKustomize},
		{marker: "Kustomization", format: toolApi.FormatKustomize},
	} {
		t.Run(tc.marker, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			format, err := detectBackend(writeSource(t, tc.marker))

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(format).To(Equal(tc.format))
		})
	}
}

// TestDetectBackendIgnoresNestedMarkers proves markers below the
// source root do not select a backend.
func TestDetectBackendIgnoresNestedMarkers(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "base"), 0o755); err != nil {
		t.Fatalf("create base: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base", "kustomization.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write nested marker: %v", err)
	}

	_, err := detectBackend(dir)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
}

// TestDetectBackendRejectsAmbiguousRoots proves no marker, both
// backend families, and duplicate Kustomize markers all fail as
// definition failures.
func TestDetectBackendRejectsAmbiguousRoots(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		markers []string
	}{
		{name: "no marker", markers: nil},
		{name: "both backends", markers: []string{"Chart.yaml", "kustomization.yaml"}},
		{
			name:    "duplicate kustomize markers",
			markers: []string{"kustomization.yaml", "kustomization.yml"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			var dir string
			if tc.markers == nil {
				dir = t.TempDir()
			} else {
				dir = writeSource(t, tc.markers...)
			}

			_, err := detectBackend(dir)

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
		})
	}
}
