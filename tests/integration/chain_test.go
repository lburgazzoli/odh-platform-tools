//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/helm"
)

func TestLintChainHelmValidModule(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmModule(t, root)

	result, err := lint(t, root)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Diagnostics).To(BeEmpty())
}

func TestLintChainKustomizeValidModule(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeKustomizeModule(t, root)

	result, err := lint(t, root)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Diagnostics).To(BeEmpty())
}

func TestLintChainHelmFindings(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", plainChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", chartValuesYAML)
	// Structural manifest defects, not execution errors: a disallowed
	// kind, an unsupported List object, and dangling references on
	// top of the missing controller Deployment and Module CRD.
	writeModuleFile(t, root, "manifests/chart/templates/namespace.yaml", namespaceTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/list.yaml", listTemplateYAML)
	writeModuleFile(t, root, "manifests/chart/templates/rolebinding.yaml",
		unresolvedRoleBindingTemplateYAML)

	result, err := lint(t, root)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.HasErrors()).To(BeTrue())
	g.Expect(diagnosticIDs(result)).To(ConsistOf(
		linter.CheckResourceKindDisallowed,
		linter.CheckObjectListUnsupported,
		linter.CheckRoleReferenceUnresolved,
		linter.CheckServiceAccountUnresolved,
		linter.CheckDeploymentMissing,
		linter.CheckModuleCRDMissing,
		linter.CheckSingletonEvidenceMissing,
	))
}

func TestLintChainInvalidDefinition(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", invalidDefinitionYAML)

	result, err := lint(t, root)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeTrue())
	g.Expect(result.Diagnostics).To(BeEmpty())
}

func TestLintChainInvalidOptions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result, err := module.NewLinter().Run(t.Context(), linter.Options{})

	g.Expect(errors.Is(err, linter.ErrInvalidOptions)).To(BeTrue())
	g.Expect(result.Diagnostics).To(BeEmpty())
}

func TestLintChainRejectsEscapingSource(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	parent := t.TempDir()
	root := filepath.Join(parent, "module")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	// The referenced file exists, but outside the module root: the
	// containment guard must reject the escape, not read it.
	writeModuleFile(t, root, "definition.yaml", kustomizeDefinitionYAML)
	writeModuleFile(t, root, "manifests/kustomization.yaml", escapingKustomizationYAML)
	writeModuleFile(t, parent, "external/resource.yaml", externalResourceYAML)

	result, err := lint(t, root)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeFalse())
	g.Expect(errors.Is(err, linter.ErrInvalidOptions)).To(BeFalse())
	g.Expect(result.Diagnostics).To(BeEmpty())
}

func TestLintChainRejectsRemoteSource(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", kustomizeDefinitionYAML)
	writeModuleFile(t, root, "manifests/kustomization.yaml", remoteKustomizationYAML)

	result, err := lint(t, root)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, config.ErrInvalidDefinition)).To(BeFalse())
	g.Expect(result.Diagnostics).To(BeEmpty())
}

func TestLintChainRenderError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", plainChartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", chartValuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/broken.yaml", brokenTemplateYAML)

	result, err := lint(t, root)

	// A failed render is an execution error, never a partial object
	// set presented as a successful lint.
	g.Expect(err).To(HaveOccurred())
	g.Expect(result.Diagnostics).To(BeEmpty())
}

func TestLintChainCancelledContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmModule(t, root)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := lintContext(ctx, t, root)

	g.Expect(err).To(MatchError(context.Canceled))
	g.Expect(result.Diagnostics).To(BeEmpty())
}

func TestLintChainEnvironmentIndependence(t *testing.T) {
	g := NewWithT(t)

	// Undefined replacement-source variables and an invalid
	// KUBECONFIG do not affect lint behavior: the chain reads no
	// environment source. (No t.Parallel: the environment is
	// process-global.)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-kubeconfig"))
	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/env/controller:v1")

	root := t.TempDir()
	writeHelmModule(t, root)

	result, err := lint(t, root)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Diagnostics).To(BeEmpty())
}

func TestLintChainPreservesDescriptorIsolation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	// The reusable services stay independently callable: load and
	// render the module with the descriptor image and env declared,
	// and prove the rendered controller Deployment keeps the chart's
	// own image and env instead.
	root := t.TempDir()
	writeHelmModule(t, root)

	document, err := loader.New().Load(t.Context(),
		filepath.Join(root, "definition.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	// The loader preserves authored descriptor strings verbatim.
	g.Expect(document.Definition.Spec.Controller.Deployment.Container.Image).
		To(Equal("quay.io/example/controller:v1"))

	resources, err := helm.Render(t.Context(), document)
	g.Expect(err).NotTo(HaveOccurred())

	var deployment *unstructured.Unstructured

	for i := range resources {
		if resources[i].GetKind() == "Deployment" &&
			resources[i].GetName() == "synthetic-controller" {
			deployment = &resources[i]
		}
	}

	g.Expect(deployment).NotTo(BeNil())

	containers, found, err := unstructured.NestedSlice(
		deployment.Object, "spec", "template", "spec", "containers")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(containers).To(HaveLen(1))

	manager, ok := containers[0].(map[string]any)
	g.Expect(ok).To(BeTrue())
	g.Expect(manager["name"]).To(Equal("manager"))

	// The chart's image survives; the descriptor image and env never
	// reach the rendered container.
	g.Expect(manager["image"]).To(Equal("quay.io/chart/synthetic-controller:v9"))
	g.Expect(manager).NotTo(HaveKey("env"))
}
