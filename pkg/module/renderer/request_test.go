package renderer

import (
	"context"
	"path"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	. "github.com/onsi/gomega"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/render/kustomize"
	mk "github.com/opendatahub-io/odh-platform-utilities/framework/render/kustomize"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// kustomizationYAML is the in-memory Kustomize entry.
const kustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
resources:
- configmap.yaml
- deployment.yaml
`

// configMapYAML is the in-memory ConfigMap resource.
const configMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: module-cm
data:
  foo: bar
`

// deploymentResourceYAML is the in-memory Deployment resource.
const deploymentResourceYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: module-controller
spec:
  replicas: 1
`

// helmDefinition is a fully normalized Helm module definition.
func helmDefinition() v1alpha1.PlatformModuleDefinition {
	return v1alpha1.PlatformModuleDefinition{
		APIVersion: v1alpha1.APIVersion,
		Kind:       v1alpha1.Kind,
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic"},
		Spec: v1alpha1.PlatformModuleDefinitionSpec{
			Instance: v1alpha1.Instance{Type: v1alpha1.ModuleType{
				Group:   "components.platform.opendatahub.io",
				Version: "v1alpha1",
				Kind:    "SyntheticModule",
			}},
			Manifests: v1alpha1.Manifests{
				Format: v1alpha1.FormatHelm,
				Helm:   &v1alpha1.HelmManifests{Path: "./manifests/chart"},
			},
			Controller: v1alpha1.Controller{
				Deployment: v1alpha1.ControllerDeployment{
					Name: "synthetic-controller",
					Container: &v1alpha1.ContainerDefinition{
						Name:  v1alpha1.DefaultMainContainerName,
						Image: "quay.io/example/controller:v1",
					},
				},
			},
		},
	}
}

// definitionFile locates helmDefinition on disk; its directory is the
// root renderer paths resolve against.
const definitionFile = "/modules/synthetic/definition.yaml"

// kustomizeDefinition is a fully normalized Kustomize module
// definition whose entry path is the argument.
func kustomizeDefinition(path string) v1alpha1.PlatformModuleDefinition {
	definition := helmDefinition()
	definition.Spec.Manifests = v1alpha1.Manifests{
		Format:    v1alpha1.FormatKustomize,
		Kustomize: &v1alpha1.KustomizeManifests{Path: path},
	}

	return definition
}

// TestNewRequestWithoutBackendBlock proves a definition that selects a
// backend without its block — decodable now that structural validation
// is deferred — yields a request with no entry instead of a panic.
func TestNewRequestWithoutBackendBlock(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	helmless := helmDefinition()
	helmless.Spec.Manifests = v1alpha1.Manifests{Format: v1alpha1.FormatHelm}

	request := NewRequest(helmless, definitionFile)
	g.Expect(request.HelmCharts).To(BeEmpty())
	g.Expect(request.Manifests).To(BeEmpty())

	blockless := kustomizeDefinition("./manifests/controller")
	blockless.Spec.Manifests = v1alpha1.Manifests{Format: v1alpha1.FormatKustomize}

	request = NewRequest(blockless, definitionFile)
	g.Expect(request.HelmCharts).To(BeEmpty())
	g.Expect(request.Manifests).To(BeEmpty())
}

func TestNewRequestHelm(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := helmDefinition()
	request := NewRequest(definition, definitionFile)

	// The chart source is the contained local entry, and the release
	// name is the module name. No values are attached: replacement
	// resolution does not exist yet and never belongs to requests.
	g.Expect(request.HelmCharts).To(HaveLen(1))
	g.Expect(request.HelmCharts[0].Chart).To(Equal("/modules/synthetic/manifests/chart"))
	g.Expect(request.HelmCharts[0].ReleaseName).To(Equal("synthetic"))
	g.Expect(request.HelmCharts[0].Values).To(BeNil())
	g.Expect(request.Manifests).To(BeEmpty())

	// The request owns no cluster state.
	g.Expect(request.Client).To(BeNil())
	g.Expect(request.Controller).To(BeNil())
	g.Expect(request.Conditions).To(BeNil())
	g.Expect(request.SkipDeploy).To(BeFalse())

	// The instance carries the authored type and name only.
	instance := request.Instance
	gvk := instance.GetObjectKind().GroupVersionKind()
	g.Expect(gvk.Group).To(Equal("components.platform.opendatahub.io"))
	g.Expect(gvk.Version).To(Equal("v1alpha1"))
	g.Expect(gvk.Kind).To(Equal("SyntheticModule"))
	g.Expect(instance.GetName()).To(Equal("synthetic"))
	g.Expect(instance.GetUID()).To(BeEmpty())
	g.Expect(instance.GetGeneration()).To(Equal(int64(0)))
	g.Expect(instance.GetNamespace()).To(BeEmpty())

	// The source definition is not aliased or mutated.
	g.Expect(definition.Spec.Controller.Deployment.Container.Name).To(Equal(v1alpha1.DefaultMainContainerName))
	instance.SetName("mutated")
	g.Expect(definition.Name).To(Equal("synthetic"))
}

func TestNewRequestIgnoresInstanceName(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := helmDefinition()
	definition.Spec.Instance.Name = "synthetic-trainer"

	request := NewRequest(definition, definitionFile)

	// The configured instance name is reserved for future custom
	// resource creation and lookup: the in-memory instance and the
	// Helm release stay metadata.name-based, while the GVK comes from
	// the instance type.
	g.Expect(request.Instance.GetName()).To(Equal("synthetic"))
	g.Expect(request.Instance.GetObjectKind().GroupVersionKind().Kind).To(Equal("SyntheticModule"))
	g.Expect(request.HelmCharts[0].ReleaseName).To(Equal("synthetic"))
}

func TestNewRequestKustomize(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := NewRequest(kustomizeDefinition("./manifests/overlays/development"), definitionFile)

	// The authored relative path resolves against the definition
	// file's directory.
	g.Expect(request.Manifests).To(HaveLen(1))
	g.Expect(request.Manifests[0].Path).To(Equal("/modules/synthetic/manifests/overlays/development"))
	g.Expect(request.HelmCharts).To(BeEmpty())
}

func TestNewRequestHonorsAbsoluteEntryPath(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	// An absolute path is used exactly as configured; it is not
	// joined onto the definition file's directory.
	definition := helmDefinition()
	definition.Spec.Manifests.Helm.Path = "/elsewhere/chart"

	request := NewRequest(definition, definitionFile)

	g.Expect(request.HelmCharts[0].Chart).To(Equal("/elsewhere/chart"))
}

func TestNewRequestWithOptionsNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := Options{Namespace: "opendatahub"}

	// The Helm backend carries the options' namespace as the release
	// namespace, exactly as the operator passes the applications
	// namespace to module charts.
	request := NewRequestWithOptions(helmDefinition(), definitionFile, options)
	g.Expect(request.HelmCharts[0].ReleaseNamespace).To(Equal("opendatahub"))
	g.Expect(request.HelmCharts[0].ReleaseName).To(Equal("synthetic"))

	// The Kustomize backend deliberately does not carry the namespace
	// on the manifest entry: like the operator's render action, the
	// backend applies it through the engine's namespace transformer
	// at render time.
	request = NewRequestWithOptions(kustomizeDefinition("./manifests/controller"), definitionFile, options)
	g.Expect(request.Manifests[0].Namespace).To(BeEmpty())

	// The zero-option adapter reproduces the offline request.
	offline := NewRequest(helmDefinition(), definitionFile)
	g.Expect(offline.HelmCharts[0].ReleaseNamespace).To(BeEmpty())
}

func TestRequestsAreIndependent(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := helmDefinition()

	first := NewRequest(definition, definitionFile)
	second := NewRequest(definition, definitionFile)

	first.Instance.SetName("mutated")

	resource := unstructured.Unstructured{}
	resource.SetAPIVersion("v1")
	resource.SetKind("ConfigMap")
	resource.SetName("module-cm")
	first.Resources = append(first.Resources, resource)

	g.Expect(second.Instance.GetName()).To(Equal("synthetic"))
	g.Expect(second.Resources).To(BeEmpty())
}

func TestModuleInstanceConditionsAreCopied(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	instance := NewInstance(helmDefinition())

	conditions := []api.Condition{{Type: "Ready", Status: "True"}}
	instance.SetConditions(conditions)

	// Mutating the argument after the call does not change the instance.
	conditions[0].Status = "False"

	g.Expect(instance.GetConditions()).To(HaveLen(1))
	g.Expect(string(instance.GetConditions()[0].Status)).To(Equal("True"))
}

func TestModuleInstanceDeepCopyDoesNotAlias(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	instance := NewInstance(helmDefinition())
	instance.SetLabels(map[string]string{"app": "synthetic"})
	instance.SetAnnotations(map[string]string{"note": "original"})
	instance.SetConditions([]api.Condition{{Type: "Ready", Status: "True"}})

	copied := instance.DeepCopy()

	// Mutating the original's mutable state leaves the copy unchanged.
	instance.GetLabels()["app"] = "mutated"
	instance.GetAnnotations()["note"] = "mutated"
	instance.SetConditions([]api.Condition{{Type: "Degraded", Status: "True"}})

	g.Expect(copied.GetLabels()).To(HaveKeyWithValue("app", "synthetic"))
	g.Expect(copied.GetAnnotations()).To(HaveKeyWithValue("note", "original"))
	g.Expect(copied.GetConditions()).To(HaveLen(1))
	g.Expect(copied.GetConditions()[0].Type).To(Equal("Ready"))

	// DeepCopyObject returns a distinct, equally isolated copy.
	deep := instance.DeepCopyObject()
	g.Expect(deep).NotTo(BeIdenticalTo(instance))
	g.Expect(deep.(*ModuleInstance).GetLabels()).To(HaveKeyWithValue("app", "mutated"))

	// The copied status block is its own pointer.
	copied.GetStatus().Phase = "Ready"
	g.Expect(instance.GetStatus().Phase).To(BeEmpty())
}

func TestFrameworkKustomizeActionRunsCacheDisabled(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	// A real in-memory Kustomize tree rendered through the actual
	// pinned framework action, with caching disabled.
	fs := filesys.MakeFsInMemory()
	entry := path.Join("/", "module", "manifests", "overlays", "development")
	_ = fs.MkdirAll(entry)
	_ = fs.WriteFile(path.Join(entry, "kustomization.yaml"), []byte(kustomizationYAML))
	_ = fs.WriteFile(path.Join(entry, "configmap.yaml"), []byte(configMapYAML))
	_ = fs.WriteFile(path.Join(entry, "deployment.yaml"), []byte(deploymentResourceYAML))

	action := kustomize.NewAction(
		kustomize.WithCache(false),
		kustomize.WithManifestsOptions(mk.WithEngineFS(fs)),
	)

	request := NewRequest(kustomizeDefinition(entry), definitionFile)

	// First run renders: the cacher reads the instance kind for
	// metrics even with caching disabled, so the authored module type
	// must satisfy it.
	g.Expect(action(t.Context(), request)).To(Succeed())
	g.Expect(request.Resources).To(HaveLen(2))
	g.Expect(request.Generated).To(BeTrue())

	// Second run re-renders instead of serving a cached result, so no
	// cluster identity (UID, generation) is ever required.
	request2 := NewRequest(kustomizeDefinition(entry), definitionFile)
	g.Expect(action(t.Context(), request2)).To(Succeed())
	g.Expect(request2.Resources).To(HaveLen(2))
	g.Expect(request2.Generated).To(BeTrue())
	g.Expect(request2.Instance.GetObjectKind().GroupVersionKind().Kind).To(Equal("SyntheticModule"))
}

func TestFrameworkKustomizeActionSkipDeploy(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := NewRequest(kustomizeDefinition("unused"), definitionFile)
	request.SkipDeploy = true

	action := kustomize.NewAction(kustomize.WithCache(false))
	g.Expect(action(context.Background(), request)).To(Succeed())
	g.Expect(request.Resources).To(BeEmpty())
}
