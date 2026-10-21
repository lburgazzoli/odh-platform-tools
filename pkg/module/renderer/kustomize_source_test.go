package renderer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
)

// Kustomize inputs of the standard module tree.
const (
	overlayKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
- ../../base
components:
- ../../components/shared
configMapGenerator:
- name: params
  envs:
  - params.env
patches:
- path: patch.yaml
  target:
    kind: Deployment
    name: module-controller
replacements:
- path: replacement.yaml
`

	baseKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- service.yaml
`

	componentKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
- configmap.yaml
`

	moduleDeploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: module-controller
spec:
  replicas: 1
`

	serviceResourceYAML = `apiVersion: v1
kind: Service
metadata:
  name: module-service
`

	configMapResourceYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared-config
data:
  key: value
`

	patchYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: module-controller
spec:
  replicas: 2
`

	replacementYAML = `- source:
    kind: Service
    name: module-service
    fieldPath: metadata.name
  targets:
  - select:
      kind: Deployment
    fieldPaths:
    - spec.template.spec.containers[0].env[0].value
`

	paramsEnv = `LOG_LEVEL=info
`
)

func TestCaptureKustomizeCapturesReferencedInputs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", overlayKustomizationYAML)

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	// Every referenced input is captured at its module-root-relative
	// position, so relative references keep working inside the
	// snapshot.
	g.Expect(capturedPaths(snapshot)).To(ConsistOf(
		"manifests/overlays/development/kustomization.yaml",
		"manifests/overlays/development/deployment.yaml",
		"manifests/overlays/development/params.env",
		"manifests/overlays/development/patch.yaml",
		"manifests/overlays/development/replacement.yaml",
		"manifests/base/kustomization.yaml",
		"manifests/base/service.yaml",
		"manifests/components/shared/kustomization.yaml",
		"manifests/components/shared/configmap.yaml",
	))

	// Repository metadata and unreferenced files are not copied.
	for _, path := range capturedPaths(snapshot) {
		g.Expect(path).NotTo(HavePrefix(".git/"))
		g.Expect(path).NotTo(Equal("unrelated.txt"))
	}

	// The entry points inside the snapshot.
	g.Expect(snapshot.Entry()).To(Equal(filepath.Join(
		snapshot.Root(), "manifests", "overlays", "development")))

	// The captured bytes are the source bytes.
	g.Expect(snapshotFileContent(t, snapshot, "manifests/base/service.yaml")).To(Equal(serviceResourceYAML))
	g.Expect(snapshotFileContent(t, snapshot, "manifests/overlays/development/params.env")).To(Equal(paramsEnv))
}

func TestCaptureKustomizeAcceptsYmlExtension(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yml", `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- deployment.yaml
`)

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	g.Expect(capturedPaths(snapshot)).To(ContainElement(
		"manifests/overlays/development/kustomization.yml"))
	g.Expect(capturedPaths(snapshot)).To(ContainElement(
		"manifests/overlays/development/deployment.yaml"))
}

func TestCaptureKustomizeRejectsUnsafeInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		kustomization string
		extra         func(t *testing.T, root string)
		errContains   string
	}{
		{
			name: "remote https resource",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- https://example.com/base
`,
			errContains: `remote URL`,
		},
		{
			name: "remote git resource",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- github.com/org/repo//manifests
`,
			errContains: "remote Git source",
		},
		{
			name: "git ssh resource",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- git@example.com:org/repo.git
`,
			errContains: "remote source",
		},
		{
			name: "oci resource",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- oci://registry.example.com/base:latest
`,
			errContains: "remote URL",
		},
		{
			name: "absolute resource",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- /etc/passwd
`,
			errContains: "absolute reference",
		},
		{
			name: "escaping relative resource",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- ../../../../outside/service.yaml
`,
			extra: func(t *testing.T, root string) {
				outside := filepath.Join(filepath.Dir(root), "outside")
				if err := os.MkdirAll(outside, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(outside, "service.yaml"), []byte(serviceResourceYAML), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			errContains: "resolves outside module root",
		},
		{
			name: "unknown field fails closed",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
someFutureCapability: {}
`,
			errContains: `unknown field "someFutureCapability"`,
		},
		{
			name: "helm inflation",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
helmCharts:
- name: chart
  version: 1.0.0
`,
			errContains: `"helmCharts"`,
		},
		{
			name: "helm globals",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
helmGlobals:
  chartHome: charts
`,
			errContains: `"helmGlobals"`,
		},
		{
			name: "custom openapi",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
openapi:
  path: openapi.json
`,
			errContains: `"openapi"`,
		},
		{
			name: "missing resource",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- absent.yaml
`,
			errContains: "missing input",
		},
		{
			name: "duplicate keys",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- a.yaml
resources:
- b.yaml
`,
			errContains: "invalid kustomization",
		},
		{
			name: "malformed resource list",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources: not-a-list
`,
			errContains: "must be a list",
		},
		{
			name: "directory resource without kustomization",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- empty
`,
			extra: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "manifests/overlays/development/empty"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			errContains: "no kustomization.yaml",
		},
		{
			name: "external plugin generator",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
generators:
- generator.yaml
`,
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/overlays/development/generator.yaml",
					"apiVersion: example.com/v1\nkind: FancyGenerator\n")
			},
			errContains: "external or exec plugin",
		},
		{
			name: "escaping patch path",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- deployment.yaml
patches:
- path: ../../../../outside/patch.yaml
  target:
    kind: Deployment
`,
			extra: func(t *testing.T, root string) {
				outside := filepath.Join(filepath.Dir(root), "outside")
				if err := os.MkdirAll(outside, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(outside, "patch.yaml"), []byte(patchYAML), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			errContains: "resolves outside module root",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			root := t.TempDir()
			writeKustomizeFixture(t, root)
			writeModuleFile(t, root, "manifests/overlays/development/deployment.yaml", moduleDeploymentYAML)
			writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", test.kustomization)
			if test.extra != nil {
				test.extra(t, root)
			}

			snapshot, err := capture(t, root)
			g.Expect(err).To(HaveOccurred())
			g.Expect(snapshot).To(BeNil())
			g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeTrue())
			g.Expect(err.Error()).To(ContainSubstring(test.errContains))
		})
	}
}

func TestCaptureKustomizeSymlinkInsideRoot(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	// The overlay references the base service through a contained
	// symlink with a relative target.
	overlayDir := filepath.Join(root, "manifests/overlays/development")
	if err := os.Symlink("../../base/service.yaml", filepath.Join(overlayDir, "shared-service.yaml")); err != nil {
		t.Fatal(err)
	}

	kustomization := `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- shared-service.yaml
`
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", kustomization)

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	// Both the canonical file and the symlink are captured, and the
	// symlink keeps its authored relative target so it resolves inside
	// the snapshot.
	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/base/service.yaml"))
	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/overlays/development/shared-service.yaml"))
	g.Expect(snapshotHasLink(t, snapshot,
		"manifests/overlays/development/shared-service.yaml",
		"../../base/service.yaml")).To(BeTrue())

	// The snapshotted symlink resolves to the snapshotted file.
	resolved, err := filepath.EvalSymlinks(filepath.Join(
		snapshot.Root(), "manifests/overlays/development/shared-service.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	content, err := os.ReadFile(resolved)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(content)).To(Equal(serviceResourceYAML))
}

func TestCaptureKustomizeSymlinkOutsideRoot(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "service.yaml")
	if err := os.WriteFile(outsideFile, []byte(serviceResourceYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	overlayDir := filepath.Join(root, "manifests/overlays/development")
	if err := os.Symlink(outsideFile, filepath.Join(overlayDir, "escaping.yaml")); err != nil {
		t.Fatal(err)
	}

	kustomization := `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- escaping.yaml
`
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", kustomization)

	snapshot, err := capture(t, root)
	g.Expect(err).To(HaveOccurred())
	g.Expect(snapshot).To(BeNil())
	g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("resolves outside module root"))
}

func TestCaptureKustomizeDirectorySymlinkInsideRoot(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	// The base is referenced through a contained directory symlink.
	overlayDir := filepath.Join(root, "manifests/overlays/development")
	if err := os.Symlink("../../base", filepath.Join(overlayDir, "base-link")); err != nil {
		t.Fatal(err)
	}

	kustomization := `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- base-link
`
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", kustomization)

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/overlays/development/base-link"))
	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/base/kustomization.yaml"))
	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/base/service.yaml"))
}

func TestCaptureKustomizationFileReachedThroughSymlink(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	// The entry kustomization itself is a contained symlink.
	overlayDir := filepath.Join(root, "manifests/overlays/development")
	writeModuleFile(t, root, "manifests/shared-kustomization.yaml", `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- deployment.yaml
`)
	if err := os.Symlink("../../shared-kustomization.yaml", filepath.Join(overlayDir, "kustomization.yaml")); err != nil {
		t.Fatal(err)
	}

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/shared-kustomization.yaml"))
	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/overlays/development/kustomization.yaml"))
	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/overlays/development/deployment.yaml"))
	g.Expect(snapshotHasLink(t, snapshot,
		"manifests/overlays/development/kustomization.yaml",
		"../../shared-kustomization.yaml")).To(BeTrue())
}

func TestCaptureKustomizeSymlinkWithAbsoluteTargetFails(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)

	// Even an absolute symlink target that stays inside the root is
	// rejected: absolute references fail closed.
	target := filepath.Join(root, "manifests/base/service.yaml")
	overlayDir := filepath.Join(root, "manifests/overlays/development")
	if err := os.Symlink(target, filepath.Join(overlayDir, "absolute-link.yaml")); err != nil {
		t.Fatal(err)
	}

	kustomization := `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- absolute-link.yaml
`
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", kustomization)

	snapshot, err := capture(t, root)
	g.Expect(err).To(HaveOccurred())
	g.Expect(snapshot).To(BeNil())
	g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("absolute target"))
}

func TestCaptureKustomizeFileReferenceEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		kustomization string
		extra         func(t *testing.T, root string)
		errContains   string
	}{
		{
			name: "envs entry pointing at a directory",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
configMapGenerator:
- name: params
  envs:
  - adir
`,
			extra: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "manifests/overlays/development/adir"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			errContains: "is a directory, not a file",
		},
		{
			name: "file reference that is a directory symlink",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
configMapGenerator:
- name: params
  envs:
  - dir-link
`,
			extra: func(t *testing.T, root string) {
				overlayDir := filepath.Join(root, "manifests/overlays/development")
				if err := os.Symlink("../../base", filepath.Join(overlayDir, "dir-link")); err != nil {
					t.Fatal(err)
				}
			},
			errContains: "is a directory, not a file",
		},
		{
			name: "broken symlink reference",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- broken.yaml
`,
			extra: func(t *testing.T, root string) {
				overlayDir := filepath.Join(root, "manifests/overlays/development")
				if err := os.Symlink("nowhere.yaml", filepath.Join(overlayDir, "broken.yaml")); err != nil {
					t.Fatal(err)
				}
			},
			errContains: "broken symlink",
		},
		{
			name: "control file with malformed apiVersion",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
generators:
- generator.yaml
`,
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/overlays/development/generator.yaml",
					"apiVersion: [not, a, string]\n")
			},
			errContains: "malformed apiVersion",
		},
		{
			name: "control file with invalid yaml",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
transformers:
- transformer.yaml
`,
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/overlays/development/transformer.yaml",
					"apiVersion: [unclosed\n")
			},
			errContains: "invalid control file",
		},
		{
			name: "generator object without envs or files list",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
configMapGenerator:
- name: params
  envs: not-a-list
`,
			errContains: "must be a list",
		},
		{
			name: "patches entry with malformed path",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
patches:
- path: [not, a, string]
`,
			errContains: "malformed path",
		},
		{
			name: "non-string resource entry",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- 42
`,
			errContains: "must contain strings",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			root := t.TempDir()
			writeKustomizeFixture(t, root)
			writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", test.kustomization)
			if test.extra != nil {
				test.extra(t, root)
			}

			snapshot, err := capture(t, root)
			g.Expect(err).To(HaveOccurred())
			g.Expect(snapshot).To(BeNil())
			g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeTrue())
			g.Expect(err.Error()).To(ContainSubstring(test.errContains))
		})
	}
}

func TestCaptureKustomizeIgnoresOriginalsChangedAfterCapture(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeFixture(t, root)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", overlayKustomizationYAML)

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	// Replace the original kustomization with an unsafe one and delete
	// the original resources: the snapshot must keep the captured bytes.
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml",
		"apiVersion: kustomize.config.k8s.io/v1beta1\nresources:\n- https://example.com/evil\n")
	if err := os.Remove(filepath.Join(root, "manifests/base/service.yaml")); err != nil {
		t.Fatal(err)
	}

	g.Expect(snapshotFileContent(t, snapshot, "manifests/base/service.yaml")).To(Equal(serviceResourceYAML))
	g.Expect(snapshotFileContent(t, snapshot, "manifests/overlays/development/kustomization.yaml")).
		To(Equal(overlayKustomizationYAML))
}
