package loader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// helmDefinitionYAML is the valid Helm module definition.
const helmDefinitionYAML = `apiVersion: config.opendatahub.io/v1
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
  relatedImages:
    - RELATED_IMAGE_SYNTHETIC_OPERAND
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: "{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}"
        env:
          - name: LOG_LEVEL
            value: "{{CONTROLLER_LOG_LEVEL}}"
`

// kustomizeDefinitionYAML is the valid Kustomize module definition.
const kustomizeDefinitionYAML = `apiVersion: config.opendatahub.io/v1
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
        env:
          - name: LOG_LEVEL
            value: ""
      initContainer:
        name: copy-manifests
        image: quay.io/example/controller:v1
`

// definitionJSON is the Helm definition in JSON syntax.
const definitionJSON = `{
  "apiVersion": "config.opendatahub.io/v1",
  "kind": "PlatformModuleDefinition",
  "metadata": {"name": "synthetic"},
  "spec": {
    "type": {
      "group": "components.platform.opendatahub.io",
      "version": "v1alpha1",
      "kind": "SyntheticModule"
    },
    "manifests": {"format": "helm", "helm": {"path": "./manifests/chart"}},
    "controller": {
      "deployment": {
        "name": "synthetic-controller",
        "container": {"image": "quay.io/example/controller:v1"}
      }
    }
  }
}`

// Documents rejected while decoding, before structural validation.
const (
	unknownMemberYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
runtime:
  namespace: opendatahub
`

	legacyModuleWrapperYAML = `module:
  type:
    group: components.platform.opendatahub.io
    version: v1alpha1
    kind: SyntheticModule
`

	metadataNamespaceYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
  namespace: opendatahub
`

	// duplicateKeyYAML relies on the Kubernetes YAML semantics of the
	// single-pass decode: duplicate keys resolve last-wins instead of
	// being rejected.
	duplicateKeyYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
  name: duplicate
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

	integerNameYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: 123
`

	multipleDocumentsYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
---
apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: other
`

	scalarRootYAML = `just a string
`

	listRootYAML = `- one
- two
`

	nullRootYAML = `---
`

	commentsOnlyYAML = `# nothing but comments
`

	nonStringKeyYAML = `123: value
`

	mergeKeyYAML = `base: &base
  name: synthetic
merged:
  <<: *base
`

	malformedYAML = `metadata:
  name: [unclosed
`

	booleanNameYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: true
`

	floatNameYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: 3.14
`

	hexNameYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: 0x1F
`

	infiniteNameYAML = `apiVersion: config.opendatahub.io/v1
kind: PlatformModuleDefinition
metadata:
  name: .inf
`
)

// Documents that decode cleanly but fail structural validation. Each
// is a complete valid document except for the field under test.
const (
	wrongAPIVersionYAML = `apiVersion: example.com/v1
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

	wrongKindYAML = `apiVersion: config.opendatahub.io/v1
kind: Module
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

	missingBackendBlockYAML = `apiVersion: config.opendatahub.io/v1
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
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

	bothBackendBlocksYAML = `apiVersion: config.opendatahub.io/v1
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
    kustomize:
      path: ./manifests/tree
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

	nullImageYAML = `apiVersion: config.opendatahub.io/v1
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
        image:
`

	nullEnvValueYAML = `apiVersion: config.opendatahub.io/v1
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
        env:
          - name: LOG_LEVEL
            value:
`
)

// Renderer entry files.
const (
	chartYAML = `apiVersion: v2
name: synthetic
version: 0.1.0
`

	kustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
`

	deploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
`
)

// writeModuleFile writes content at a relative path inside root.
func writeModuleFile(t *testing.T, root, relative, content string) {
	t.Helper()

	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeHelmModule creates a valid Helm module and returns its root.
func writeHelmModule(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)

	return root
}

// writeKustomizeModule creates a valid Kustomize module whose overlay
// has a sibling base inside the module root, and returns its root.
func writeKustomizeModule(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", kustomizeDefinitionYAML)
	writeModuleFile(t, root, "manifests/base/kustomization.yaml", kustomizationYAML)
	writeModuleFile(t, root, "manifests/base/deployment.yaml", deploymentYAML)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", kustomizationYAML)

	return root
}

// firstFieldPath returns the path of the first FieldError in err.
func firstFieldPath(err error) (string, bool) {
	var field *config.FieldError
	if errors.As(err, &field) {
		return field.Path, true
	}

	return "", false
}

func TestLoadHelmDefinition(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := writeHelmModule(t)

	definition, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(definition.Spec.Manifests.Helm.Path).To(Equal("./manifests/chart"))

	// Authored strings are not replaced by canonical runtime state, and
	// replacement references survive as literals.
	g.Expect(definition.Spec.Manifests.Helm.Path).To(Equal("./manifests/chart"))
	g.Expect(definition.Spec.Controller.Deployment.Container.Image).To(Equal("{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}"))
	g.Expect(definition.Spec.RelatedImages).To(Equal([]string{"RELATED_IMAGE_SYNTHETIC_OPERAND"}))

	// Placement defaults are applied.
	g.Expect(*definition.Spec.Controller.Deployment.Container.Name).To(Equal(config.DefaultMainContainerName))
}

func TestLoadKustomizeDefinitionWithSiblingBase(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := writeKustomizeModule(t)

	definition, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())

	// The explicit empty env literal is valid and the init container is
	// preserved.
	g.Expect(*definition.Spec.Controller.Deployment.Container.Env[0].Value).To(Equal(""))
	g.Expect(*definition.Spec.Controller.Deployment.InitContainer.Name).To(Equal("copy-manifests"))
}

func TestLoadFromPathByRelativePath(t *testing.T) {
	g := NewWithT(t)

	root := writeHelmModule(t)
	t.Chdir(root)

	// An operating-system path relative to the working directory
	// resolves through the DirFS adapter.
	definition, err := LoadFromPath(t.Context(), "definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(definition.Spec.Manifests.Helm.Path).To(Equal("./manifests/chart"))
}

func TestLoadJSONDefinition(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.json", definitionJSON)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)

	definition, err := Load(t.Context(), os.DirFS(root), "definition.json")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(definition.Spec.Manifests.Format).To(Equal(config.FormatHelm))
}

func TestLoadToleratesTrailingEmptyDocument(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML+"\n---\n")
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)

	_, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())
}

func TestLoadResolvesDuplicateKeysLastWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	// The single-pass decode follows the Kubernetes YAML semantics:
	// a duplicate key resolves last-wins rather than failing.
	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", duplicateKeyYAML)

	definition, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(definition.Metadata.Name).To(Equal("duplicate"))
}

func TestLoadRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     string
		errContains string
		fieldPath   string
	}{
		{
			name:        "unknown member",
			content:     unknownMemberYAML,
			errContains: `unknown object member name "runtime"`,
		},
		{
			name:        "legacy module wrapper",
			content:     legacyModuleWrapperYAML,
			errContains: `unknown object member name "module"`,
		},
		{
			name:        "unsupported metadata",
			content:     metadataNamespaceYAML,
			errContains: `unknown object member name "namespace"`,
		},
		{
			// The decoder reports type mismatches; the exact json
			// wording varies between "cannot unmarshal" and
			// "unable to unmarshal ... within".
			name:        "integer where a string is required",
			content:     integerNameYAML,
			errContains: "unmarshal",
		},
		{
			name:        "multiple documents",
			content:     multipleDocumentsYAML,
			errContains: "exactly one document",
		},
		{
			// The root is a JSON string, not an object; the typed
			// decode rejects the document shape.
			name:        "scalar root",
			content:     scalarRootYAML,
			errContains: "cannot unmarshal JSON string",
		},
		{
			name:        "list root",
			content:     listRootYAML,
			errContains: "cannot unmarshal JSON array",
		},
		{
			name:        "null root",
			content:     nullRootYAML,
			errContains: "one object document",
		},
		{
			name:        "comments only",
			content:     commentsOnlyYAML,
			errContains: "one object document",
		},
		{
			// The Kubernetes YAML machinery stringifies the key, so
			// the typed decode rejects it as an unknown member.
			name:        "non-string key",
			content:     nonStringKeyYAML,
			errContains: `unknown object member name "123"`,
		},
		{
			// Merge keys resolve through the Kubernetes YAML
			// machinery; this document still fails on its unknown
			// root members. Which one is reported first depends on
			// map iteration order, so only the failure kind is
			// asserted.
			name:        "merge key",
			content:     mergeKeyYAML,
			errContains: `unknown object member name`,
		},
		{
			name:        "malformed yaml",
			content:     malformedYAML,
			errContains: "did not find expected",
		},
		{
			name:        "wrong api version",
			content:     wrongAPIVersionYAML,
			fieldPath:   "apiVersion",
			errContains: `must be "config.opendatahub.io/v1"`,
		},
		{
			name:        "wrong kind",
			content:     wrongKindYAML,
			fieldPath:   "kind",
			errContains: `must be "PlatformModuleDefinition"`,
		},
		{
			name:        "missing backend block",
			content:     missingBackendBlockYAML,
			fieldPath:   "spec.manifests.helm",
			errContains: `is required when format is "helm"`,
		},
		{
			name:        "both backend blocks",
			content:     bothBackendBlocksYAML,
			fieldPath:   "spec.manifests.kustomize",
			errContains: `must not be set when format is "helm"`,
		},
		{
			name:        "null image",
			content:     nullImageYAML,
			fieldPath:   "spec.controller.deployment.container.image",
			errContains: "image is required",
		},
		{
			name:        "null env value",
			content:     nullEnvValueYAML,
			fieldPath:   "spec.controller.deployment.container.env[0].value",
			errContains: "value is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			root := t.TempDir()
			const path = "definition.yaml"
			writeModuleFile(t, root, path, test.content)

			_, err := Load(t.Context(), os.DirFS(root), path)
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeTrue())
			g.Expect(err.Error()).To(ContainSubstring(test.errContains))

			// Failures name the source file.
			g.Expect(err.Error()).To(ContainSubstring(path))

			if test.fieldPath != "" {
				path, ok := firstFieldPath(err)
				g.Expect(ok).To(BeTrue())
				g.Expect(path).To(Equal(test.fieldPath))
			}
		})
	}
}

// timestampValueYAML is a valid Kustomize definition whose env value is
// an unquoted date: the literal text survives as a string.
const timestampValueYAML = `apiVersion: config.opendatahub.io/v1
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
        env:
          - name: LOG_DATE
            value: 2026-01-02
`

func TestLoadRejectsNonStringScalars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     string
		errContains string
	}{
		{
			name:        "boolean where a string is required",
			content:     booleanNameYAML,
			errContains: "unmarshal",
		},
		{
			name:        "float where a string is required",
			content:     floatNameYAML,
			errContains: "unmarshal",
		},
		{
			name:        "hexadecimal integer where a string is required",
			content:     hexNameYAML,
			errContains: "unmarshal",
		},
		{
			name:        "infinity is not a JSON number",
			content:     infiniteNameYAML,
			errContains: "unsupported value: +Inf",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			root := t.TempDir()
			const path = "definition.yaml"
			writeModuleFile(t, root, path, test.content)

			_, err := Load(t.Context(), os.DirFS(root), path)
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeTrue())
			g.Expect(err.Error()).To(ContainSubstring(test.errContains))
		})
	}
}

func TestLoadKeepsTimestampLiteralsAsStrings(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", timestampValueYAML)
	writeModuleFile(t, root, "manifests/overlays/development/kustomization.yaml", kustomizationYAML)

	definition, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())

	// The unquoted date stays literal text; it is never a parsed date.
	g.Expect(*definition.Spec.Controller.Deployment.Container.Env[0].Value).To(Equal("2026-01-02"))
}

func TestLoadRejectsMissingAndNonRegularFiles(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	_, err := Load(t.Context(), os.DirFS(t.TempDir()), "missing.yaml")
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeTrue())

	// A directory is not a definition file; ReadFile rejects it.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = Load(t.Context(), os.DirFS(root), "subdir")
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeTrue())

	_, err = Load(t.Context(), os.DirFS(root), "")
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeTrue())
}

func TestLoadIgnoresEnvironment(t *testing.T) {
	g := NewWithT(t)

	// Replacement sources and kubeconfig state are irrelevant to
	// loading.
	t.Setenv("KUBECONFIG", "/nonexistent/kubeconfig")
	t.Setenv("RELATED_IMAGE_SYNTHETIC_CONTROLLER", "")

	root := writeHelmModule(t)

	_, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())
}

func TestLoadPropagatesCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := writeHelmModule(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Load(ctx, os.DirFS(root), "definition.yaml")
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeFalse())
}

func TestLoadPropagatesDeadline(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := writeHelmModule(t)

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := Load(ctx, os.DirFS(root), "definition.yaml")
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeFalse())
}
