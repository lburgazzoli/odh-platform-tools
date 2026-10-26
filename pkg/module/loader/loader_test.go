package loader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// helmDefinitionYAML is the valid Helm module definition.
const helmDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
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

// helmValuesDefinitionYAML is a co-located Helm module definition
// authoring literal chart values with an omitted backend path.
const helmValuesDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    type:
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
  manifests:
    format: helm
    helm:
      values:
        replicas: 2
        image:
          tag: v1.0.0
        logLevel: "{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}"
  relatedImages:
    - RELATED_IMAGE_SYNTHETIC_OPERAND
  controller:
    deployment:
      name: synthetic-controller
      container:
        image: "{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}"
`

// kustomizeDefinitionYAML is the valid Kustomize module definition.
const kustomizeDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
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
`

// definitionJSON is the Helm definition in JSON syntax.
const definitionJSON = `{
  "apiVersion": "config.opendatahub.io/v1alpha1",
  "kind": "PlatformModuleDefinition",
  "metadata": {"name": "synthetic"},
  "spec": {
    "instance": {
      "type": {
        "group": "components.platform.opendatahub.io",
        "version": "v1alpha1",
        "kind": "SyntheticModule"
      }
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
	legacyModuleWrapperYAML = `module:
  type:
    group: components.platform.opendatahub.io
    version: v1alpha1
    kind: SyntheticModule
`

	// instanceNameDefinitionYAML is the Helm definition with an
	// explicit instance name different from the module name.
	instanceNameDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    name: synthetic-trainer
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

	metadataNamespaceYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
  namespace: opendatahub
`

	// duplicateKeyYAML relies on the framework decoder's yaml.v3
	// semantics: duplicate keys are rejected instead of resolving
	// last-wins.
	duplicateKeyYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
  name: duplicate
spec:
  instance:
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

	integerNameYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: 123
`

	multipleDocumentsYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
---
apiVersion: config.opendatahub.io/v1alpha1
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

	booleanNameYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: true
`

	floatNameYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: 3.14
`

	hexNameYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: 0x1F
`

	infiniteNameYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: .inf
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
func writeModuleFile(t *testing.T, root string, relative string, content string) {
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
	g.Expect(definition.Spec.Controller.Deployment.Container.Name).To(Equal(v1alpha1.DefaultMainContainerName))

	// The omitted instance name defaults to the module name.
	g.Expect(definition.Spec.Instance.Name).To(Equal("synthetic"))
}

// TestLoadHelmValuesAndOmittedPath proves authored literal chart
// values survive loading as the raw JSON the renderer decodes —
// scalars, nested objects, and placeholder-looking text stay literal —
// and an omitted backend path loads without defaulting: the entry
// defaults to the definition file's directory at render time, not at
// load time.
func TestLoadHelmValuesAndOmittedPath(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmValuesDefinitionYAML)
	writeModuleFile(t, root, "Chart.yaml", chartYAML)

	definition, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(definition.Spec.Manifests.Helm.Path).To(BeEmpty())
	g.Expect(definition.Spec.Manifests.Helm.Values).To(SatisfyAll(
		HaveLen(3),
		HaveKeyWithValue("replicas", apiextensionsv1.JSON{Raw: []byte("2")}),
		HaveKeyWithValue("image", apiextensionsv1.JSON{Raw: []byte(`{"tag":"v1.0.0"}`)}),
		HaveKeyWithValue("logLevel", apiextensionsv1.JSON{Raw: []byte(`"{{RELATED_IMAGE_SYNTHETIC_CONTROLLER}}"`)}),
	))
}

// TestLoadKeepsExplicitInstanceName proves the nested instance name
// reaches the normalized config unchanged.
func TestLoadKeepsExplicitInstanceName(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", instanceNameDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)

	definition, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(definition.Spec.Instance.Name).To(Equal("synthetic-trainer"))
	g.Expect(definition.Name).To(Equal("synthetic"))
}

// containerNameDefinitionYAML is the Helm definition with an explicit
// main container name and no instance name.
const containerNameDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
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
        name: custom-manager
        image: quay.io/example/controller:v1
`

// authoredNamesDefinitionYAML is the Helm definition with both the
// instance name and the main container name authored.
const authoredNamesDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    name: trainer
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
        name: custom-manager
        image: quay.io/example/controller:v1
`

// TestLoadDefaultsNamesIndependently proves the instance and main
// container name defaults are independent: neither may skip the
// other, and authored names are never overwritten.
func TestLoadDefaultsNamesIndependently(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		content       string
		wantInstance  string
		wantContainer string
	}{
		{
			name:          "both names omitted",
			content:       helmDefinitionYAML,
			wantInstance:  "synthetic",
			wantContainer: v1alpha1.DefaultMainContainerName,
		},
		{
			name:          "instance name omitted",
			content:       containerNameDefinitionYAML,
			wantInstance:  "synthetic",
			wantContainer: "custom-manager",
		},
		{
			name:          "container name omitted",
			content:       instanceNameDefinitionYAML,
			wantInstance:  "synthetic-trainer",
			wantContainer: v1alpha1.DefaultMainContainerName,
		},
		{
			name:          "both names authored",
			content:       authoredNamesDefinitionYAML,
			wantInstance:  "trainer",
			wantContainer: "custom-manager",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			root := t.TempDir()
			writeModuleFile(t, root, "definition.yaml", test.content)
			writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)

			definition, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(definition.Spec.Instance.Name).To(Equal(test.wantInstance))
			g.Expect(definition.Spec.Controller.Deployment.Container.Name).To(Equal(test.wantContainer))
		})
	}
}

// TestLoadDecodesStandardMetadata proves the definition metadata is
// standard Kubernetes object metadata: members the old custom wire
// type rejected, such as namespace, now decode permissively. Loading
// adds no metadata validation.
func TestLoadDecodesStandardMetadata(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", metadataNamespaceYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)

	definition, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(definition.Name).To(Equal("synthetic"))
	g.Expect(definition.Namespace).To(Equal("opendatahub"))
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
	g.Expect(definition.Spec.Controller.Deployment.InitContainer.Name).To(Equal("copy-manifests"))
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
	g.Expect(definition.Spec.Manifests.Format).To(Equal(v1alpha1.FormatHelm))
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

// TestLoadRejectsDuplicateKeys pins the framework decoder's yaml.v3
// semantics: a duplicate key is rejected rather than resolving
// last-wins.
func TestLoadRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", duplicateKeyYAML)

	_, err := Load(t.Context(), os.DirFS(root), "definition.yaml")
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring(`mapping key "name" already defined`))
}

func TestLoadRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     string
		errContains string
	}{
		{
			// The legacy wrapper carries no kind, so the framework
			// decoder skips it: the stream reads as no document.
			name:        "legacy module wrapper",
			content:     legacyModuleWrapperYAML,
			errContains: "one object document",
		},
		{
			// The typed conversion reports type mismatches; the
			// converter wording varies with the scalar kind
			// ("cannot convert int64 to string",
			// "unrecognized type: string"), so assert the stem.
			name:        "integer where a string is required",
			content:     integerNameYAML,
			errContains: "unable to convert unstructured object",
		},
		{
			name:        "multiple documents",
			content:     multipleDocumentsYAML,
			errContains: "exactly one document",
		},
		{
			// The root is a scalar, not a mapping; the framework
			// decoder fails it at the YAML level.
			name:        "scalar root",
			content:     scalarRootYAML,
			errContains: "unable to decode resource",
		},
		{
			// See the scalar root case: the root is a sequence.
			name:        "list root",
			content:     listRootYAML,
			errContains: "unable to decode resource",
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
			// The document carries no kind, so the framework
			// decoder skips it: the stream reads as no document.
			name:        "non-string key",
			content:     nonStringKeyYAML,
			errContains: "one object document",
		},
		{
			// The document carries no kind, so the framework
			// decoder skips it: the stream reads as no document.
			name:        "merge key",
			content:     mergeKeyYAML,
			errContains: "one object document",
		},
		{
			name:        "malformed yaml",
			content:     malformedYAML,
			errContains: "did not find expected",
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
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
			g.Expect(err.Error()).To(ContainSubstring(test.errContains))

			// Failures name the source file.
			g.Expect(err.Error()).To(ContainSubstring(path))
		})
	}
}

// timestampValueYAML is a valid Kustomize definition whose env value is
// a quoted date: the literal text survives as a string. An unquoted
// date would follow the framework decoder's yaml semantics and
// normalize to RFC3339, so authored dates are quoted.
const timestampValueYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
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
            value: "2026-01-02"
`

func TestLoadRejectsNonStringScalars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     string
		errContains string
	}{
		{
			// The typed conversion reports type mismatches; the
			// converter wording varies with the scalar kind, so
			// assert the stem.
			name:        "boolean where a string is required",
			content:     booleanNameYAML,
			errContains: "unable to convert unstructured object",
		},
		{
			// See the boolean case: assert only the stable stem of
			// the converter's type-mismatch wording.
			name:        "float where a string is required",
			content:     floatNameYAML,
			errContains: "unable to convert unstructured object",
		},
		{
			// See the boolean case: assert only the stable stem of
			// the converter's type-mismatch wording.
			name:        "hexadecimal integer where a string is required",
			content:     hexNameYAML,
			errContains: "unable to convert unstructured object",
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
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
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

	// The quoted date stays literal text; it is never a parsed date.
	g.Expect(*definition.Spec.Controller.Deployment.Container.Env[0].Value).To(Equal("2026-01-02"))
}

func TestLoadRejectsMissingAndNonRegularFiles(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	_, err := Load(t.Context(), os.DirFS(t.TempDir()), "missing.yaml")
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())

	// A directory is not a definition file; ReadFile rejects it.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = Load(t.Context(), os.DirFS(root), "subdir")
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())

	_, err = Load(t.Context(), os.DirFS(root), "")
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
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
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeFalse())
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
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeFalse())
}
