package renderer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
)

// Definitions of minimal valid modules, one per backend.
const (
	kustomizeDefinitionYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  type:
    group: components.platform.opendatahub.io
    version: v1alpha1
    kind: SyntheticModule
  manifests:
    format: kustomize
    kustomize:
      path: ./manifests/overlays/development
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

	helmDefinitionYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  type:
    group: components.platform.opendatahub.io
    version: v1alpha1
    kind: SyntheticModule
  manifests:
    format: helm
    helm:
      path: ./manifests/chart
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`
)

// writeModuleFile writes content at a module-root-relative path.
func writeModuleFile(t *testing.T, root, rel, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadDefinition loads the definition of the module rooted at root
// through the real loader.
func loadDefinition(t *testing.T, root string) config.Document {
	t.Helper()

	g := NewWithT(t)

	document, err := loader.New().Load(t.Context(), filepath.Join(root, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	return document
}

// capture captures the renderer inputs of the module rooted at root.
func capture(t *testing.T, root string) (*Snapshot, error) {
	t.Helper()

	return Capture(t.Context(), loadDefinition(t, root))
}

// capturedPaths returns the module-root-relative paths of a snapshot.
func capturedPaths(snapshot *Snapshot) []string {
	paths := make([]string, 0, len(snapshot.Files()))
	for _, file := range snapshot.Files() {
		paths = append(paths, file.Path)
	}

	return paths
}

func TestCapturePropagatesCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	document := loadDefinition(t, root)

	_, err := Capture(ctx, document)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeFalse())
}

func TestCaptureRejectsUnsupportedFormat(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)

	document := loadDefinition(t, root)
	document.Definition.Spec.Manifests.Format = config.Format("bogus")

	_, err := Capture(t.Context(), document)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("unsupported manifest format"))
	g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeFalse())
}

func TestCaptureCleansUpOnFailure(t *testing.T) {
	// An exclusive temporary directory makes leaked snapshots
	// attributable to this test. t.Setenv intentionally keeps this test
	// non-parallel.
	exclusive := t.TempDir()
	t.Setenv("TMPDIR", exclusive)

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithMissingDependencyYAML)

	snapshot, err := capture(t, root)
	g.Expect(err).To(HaveOccurred())
	g.Expect(snapshot).To(BeNil())
	g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeTrue())

	count := func() int {
		matches, err := filepath.Glob(filepath.Join(exclusive, "odh-module-snapshot-*"))
		if err != nil {
			t.Fatal(err)
		}

		return len(matches)
	}

	// Snapshots held briefly by concurrently running tests settle; a
	// snapshot leaked by the failed capture never would.
	g.Eventually(count).WithTimeout(10 * time.Second).Should(BeZero())
}

func TestSnapshotCloseRemovesRoot(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	g.Expect(snapshot.Root()).NotTo(BeEmpty())
	info, err := os.Stat(snapshot.Root())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(info.IsDir()).To(BeTrue())

	g.Expect(snapshot.Close()).To(Succeed())
	_, err = os.Stat(snapshot.Root())
	g.Expect(os.IsNotExist(err)).To(BeTrue())
}

func TestCaptureFailsWhenEntryEscapesRoot(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)

	document := loadDefinition(t, root)
	document.RendererEntry = filepath.Join(filepath.Dir(root), "elsewhere")

	_, err := Capture(t.Context(), document)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("escapes module root"))
}

func TestCapturedPathsAreUnique(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", overlayKustomizationYAML)

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	paths := capturedPaths(snapshot)
	g.Expect(paths).NotTo(BeEmpty())

	sorted := slices.Clone(paths)
	slices.Sort(sorted)
	deduplicated := slices.Compact(sorted)
	g.Expect(deduplicated).To(HaveLen(len(sorted)))
}

// writeKustomizeFixture writes the supporting files of the standard
// Kustomize module tree referenced by overlayKustomizationYAML.
func writeKustomizeFixture(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", kustomizeDefinitionYAML)
	writeModuleFile(t, root, "manifests/overlays/development/deployment.yaml", deploymentResourceYAML)
	writeModuleFile(t, root, "manifests/overlays/development/params.env", paramsEnv)
	writeModuleFile(t, root, "manifests/overlays/development/patch.yaml", patchYAML)
	writeModuleFile(t, root, "manifests/overlays/development/replacement.yaml", replacementYAML)
	writeModuleFile(t, root, "manifests/base/kustomization.yaml", baseKustomizationYAML)
	writeModuleFile(t, root, "manifests/base/service.yaml", serviceResourceYAML)
	writeModuleFile(t, root, "manifests/components/shared/kustomization.yaml", componentKustomizationYAML)
	writeModuleFile(t, root, "manifests/components/shared/configmap.yaml", configMapResourceYAML)
	writeModuleFile(t, root, ".git/config", "unrelated repository metadata")
	writeModuleFile(t, root, "unrelated.txt", "not referenced by the kustomization")
}

// snapshotFileContent reads one file back from the materialized
// snapshot.
func snapshotFileContent(t *testing.T, snapshot *Snapshot, rel string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(snapshot.Root(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}

	return string(content)
}

// snapshotHasLink reports whether the snapshot contains a symlink at
// rel with the given target.
func snapshotHasLink(t *testing.T, snapshot *Snapshot, rel, target string) bool {
	t.Helper()

	linkTarget, err := os.Readlink(filepath.Join(snapshot.Root(), filepath.FromSlash(rel)))
	if err != nil {
		return false
	}

	return linkTarget == target
}
