package deployments

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/linter"
)

// object builds one unstructured object with the given identity.
func object(apiVersion string, kind string, name string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
	}}
}

// testDefinition returns a minimal module definition: its type is the
// SyntheticModule custom resource.
func testDefinition() toolApi.PlatformModuleDefinition {
	return toolApi.PlatformModuleDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic"},
		Spec: toolApi.PlatformModuleDefinitionSpec{
			Controller: toolApi.Controller{Deployment: toolApi.ControllerDeployment{Name: "synthetic-controller"}},
			Instance: toolApi.Instance{Type: toolApi.ModuleType{
				Group:   "components.platform.opendatahub.io",
				Version: "v1alpha1",
				Kind:    "SyntheticModule",
			}},
		},
	}
}

// managerDefinition returns a definition selecting the default main
// container name.
func managerDefinition() toolApi.PlatformModuleDefinition {
	definition := testDefinition()
	definition.Spec.Controller.Deployment.Container = &toolApi.ContainerDefinition{
		Name:  "manager",
		Image: "quay.io/example/controller:v1",
	}

	return definition
}

// initContainerDefinition returns a definition selecting the named
// init container.
func initContainerDefinition(name string) toolApi.PlatformModuleDefinition {
	definition := testDefinition()
	definition.Spec.Controller.Deployment.InitContainer = &toolApi.ContainerTarget{
		Name: name,
	}

	return definition
}

// deployment builds one Deployment whose pod spec lists containers
// with the given names.
func deployment(containers []string, initContainers []string) unstructured.Unstructured {
	res := object("apps/v1", "Deployment", "synthetic-controller")

	podSpec := map[string]any{}
	if containers != nil {
		podSpec["containers"] = containerList(containers)
	}
	if initContainers != nil {
		podSpec["initContainers"] = containerList(initContainers)
	}

	res.Object["spec"] = map[string]any{
		"template": map[string]any{"spec": podSpec},
	}

	return res
}

// containerList builds one pod spec container list with the given
// names.
func containerList(names []string) []any {
	list := make([]any, 0, len(names))
	for _, name := range names {
		list = append(list, map[string]any{"name": name})
	}

	return list
}

func TestDeploymentValidate(t *testing.T) {
	t.Parallel()

	t.Run("exactly one deployment passes", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		resources := []unstructured.Unstructured{
			object("v1", "ServiceAccount", "controller"),
			object("apps/v1", "Deployment", "synthetic-controller"),
		}

		diagnostics, err := Validate(testDefinition(), resources)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(BeEmpty())
	})

	t.Run("no deployment is reported on the module", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(testDefinition(), []unstructured.Unstructured{
			object("v1", "ServiceAccount", "controller"),
		})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(Missing))
		g.Expect(diagnostics[0].Severity).To(Equal(linter.SeverityError))
		g.Expect(diagnostics[0].GVK.Group).To(Equal("components.platform.opendatahub.io"))
		g.Expect(diagnostics[0].GVK.Kind).To(Equal("SyntheticModule"))
		g.Expect(diagnostics[0].Message).To(ContainSubstring("found 0"))
	})

	t.Run("multiple deployments are reported on the module", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(testDefinition(), []unstructured.Unstructured{
			object("apps/v1", "Deployment", "controller-a"),
			object("apps/v1", "Deployment", "controller-b"),
		})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(Multiple))
		g.Expect(diagnostics[0].Message).To(ContainSubstring("found 2"))
	})

	t.Run("empty bundle is a missing deployment", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(testDefinition(), nil)

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(Missing))
	})
}

func TestControllerSelection(t *testing.T) {
	t.Parallel()
	for _, resource := range []unstructured.Unstructured{
		object("apps/v1", "Deployment", "wrong-name"),
		object("apps/v1beta1", "Deployment", "synthetic-controller"),
	} {
		t.Run(resource.GetAPIVersion()+resource.GetName(), func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)
			findings, err := Validate(managerDefinition(), []unstructured.Unstructured{resource})
			g.Expect(err).To(Succeed())
			g.Expect(findings).To(HaveLen(1))
			g.Expect(findings[0].ID).To(Equal(ControllerMismatch))
		})
	}
	t.Run("module name fallback", func(t *testing.T) {
		g := NewWithT(t)
		definition := managerDefinition()
		definition.Spec.Controller.Deployment.Name = ""
		resource := deployment([]string{"manager"}, nil)
		resource.SetName(definition.Name)
		g.Expect(Validate(definition, []unstructured.Unstructured{resource})).To(BeEmpty())
	})
	t.Run("unnamed configured init container", func(t *testing.T) {
		g := NewWithT(t)
		findings, err := Validate(initContainerDefinition(""), []unstructured.Unstructured{deployment([]string{"manager"}, nil)})
		g.Expect(err).To(Succeed())
		g.Expect(findings).To(HaveLen(1))
		g.Expect(findings[0].ID).To(Equal(ContainerMissing))
	})
}

func TestDeploymentValidateContainerExists(t *testing.T) {
	t.Parallel()

	t.Run("selected main container present passes", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(managerDefinition(),
			[]unstructured.Unstructured{deployment([]string{"manager"}, nil)})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(BeEmpty())
	})

	t.Run("selected main container missing is reported", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(managerDefinition(),
			[]unstructured.Unstructured{deployment([]string{"other"}, nil)})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(ContainerMissing))
		g.Expect(diagnostics[0].Severity).To(Equal(linter.SeverityError))
		g.Expect(diagnostics[0].GVK.Kind).To(Equal("Deployment"))
		g.Expect(diagnostics[0].Name).To(Equal("synthetic-controller"))
		g.Expect(diagnostics[0].Field).To(Equal("spec.template.spec.containers"))
		g.Expect(diagnostics[0].Message).To(ContainSubstring(`container "manager" is not present in containers`))
	})

	t.Run("selected main container in the init container list is still missing", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(managerDefinition(),
			[]unstructured.Unstructured{deployment([]string{"other"}, []string{"manager"})})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(ContainerMissing))
	})

	t.Run("selected container present more than once still passes", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		// Existence is the contract: duplicates are not the check's
		// concern.
		diagnostics, err := Validate(managerDefinition(),
			[]unstructured.Unstructured{deployment([]string{"manager", "manager"}, nil)})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(BeEmpty())
	})

	t.Run("selected init container validated in its list", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(initContainerDefinition("wait-for-crd"),
			[]unstructured.Unstructured{deployment([]string{"manager"}, []string{"other"})})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(ContainerMissing))
		g.Expect(diagnostics[0].Field).To(Equal("spec.template.spec.initContainers"))
		g.Expect(diagnostics[0].Message).To(ContainSubstring(`container "wait-for-crd" is not present in initContainers`))
	})

	t.Run("unconfigured init container is skipped", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		// No init container block: only the main container is
		// validated.
		diagnostics, err := Validate(managerDefinition(),
			[]unstructured.Unstructured{deployment([]string{"manager"}, nil)})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(BeEmpty())
	})

	t.Run("unconfigured placements are skipped", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		// No controller container block: nothing to validate beyond
		// the deployment count.
		diagnostics, err := Validate(testDefinition(),
			[]unstructured.Unstructured{deployment([]string{"anything"}, nil)})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(BeEmpty())
	})

	t.Run("deployment without pod spec has no containers", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		diagnostics, err := Validate(managerDefinition(),
			[]unstructured.Unstructured{object("apps/v1", "Deployment", "synthetic-controller")})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(diagnostics).To(HaveLen(1))
		g.Expect(diagnostics[0].ID).To(Equal(ContainerMissing))
	})

	t.Run("malformed container list is an evaluation error, never a silent non-match", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		malformed := object("apps/v1", "Deployment", "synthetic-controller")
		malformed.Object["spec"] = map[string]any{
			"template": map[string]any{
				"spec": map[string]any{"containers": "not-a-list"},
			},
		}

		_, err := Validate(managerDefinition(), []unstructured.Unstructured{malformed})

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`match container "manager" in containers`))
	})
}
