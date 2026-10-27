package adapt

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	. "github.com/onsi/gomega"

	toolk8s "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// newDefinition returns a pointer to the synthetic module definition
// carrying the full controller deployment descriptor: a token image,
// one extra env entry, an init container target, and one related
// image. The pointer form is what the runner hands to the injectors;
// tests that tailor it mutate through the pointer directly.
func newDefinition() *toolApi.PlatformModuleDefinition {
	return &toolApi.PlatformModuleDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "synthetic",
		},
		Spec: toolApi.PlatformModuleDefinitionSpec{
			Instance: toolApi.Instance{
				Type: toolApi.ModuleType{
					Group:   "components.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "SyntheticModule",
				},
			},
			Manifests: toolApi.Manifests{
				Format: toolApi.FormatHelm,
			},
			RelatedImages: []string{"RELATED_IMAGE_OPERAND"},
			Controller: toolApi.Controller{
				Deployment: toolApi.ControllerDeployment{
					Name: "synthetic-controller",
					Container: &toolApi.ContainerDefinition{
						Name:  "manager",
						Image: "{{RELATED_IMAGE_CONTROLLER}}",
						Env: []toolApi.EnvVar{
							{Name: "LOG_LEVEL", Value: new("debug")},
						},
					},
					InitContainer: &toolApi.ContainerTarget{
						Name: "copy-manifests",
					},
				},
			},
		},
	}
}

// newResolvedImages returns the fully resolved image snapshot.
func newResolvedImages() map[string]string {
	return map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
		"RELATED_IMAGE_OPERAND":    "quay.io/example/operand:v1",
	}
}

// resource builds an unstructured resource carrying only an identity —
// a group/version/kind, namespace, and name — standing in for one
// rendered resource.
func resource(gvk schema.GroupVersionKind, namespace string, name string) unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(namespace)
	u.SetName(name)

	return *u
}

// findResource returns the first resource of the slice carrying the
// exact group/version/kind, namespace, and name, as a pointer into the
// original slice — or nil when no resource matches. The comparison
// reuses the shared identity helper, so a same-name lookalike of
// another group, version, kind, or namespace never matches. Callers
// assert the result is nonnil before reading it.
func findResource(
	resources []unstructured.Unstructured,
	gvk schema.GroupVersionKind,
	key types.NamespacedName,
) *unstructured.Unstructured {
	target := &unstructured.Unstructured{}
	target.SetGroupVersionKind(gvk)
	target.SetNamespace(key.Namespace)
	target.SetName(key.Name)

	for i := range resources {
		if toolk8s.Is(&resources[i], target) {
			return &resources[i]
		}
	}

	return nil
}

// envVarNames returns the env var names of the container in order.
func envVarNames(container corev1.Container) []string {
	names := make([]string, 0, len(container.Env))
	for _, env := range container.Env {
		names = append(names, env.Name)
	}

	return names
}

// serviceYAML is a non-Deployment rendered resource.
const serviceYAML = `apiVersion: v1
kind: Service
metadata:
  name: synthetic-service
spec:
  ports:
  - port: 8080
`

// TestFindResource pins the identity findResource selects: the exact
// group/version/kind, namespace, and name — never a same-name
// lookalike of another group, version, kind, or namespace — with no
// scheme behind the comparison, so foreign groups resolve too.
func TestFindResource(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	gvk := schema.GroupVersionKind{Group: "config.example.com", Version: "v1", Kind: "ConfigMap"}
	key := types.NamespacedName{Namespace: "opendatahub", Name: "odh-config"}

	resources := []unstructured.Unstructured{
		resource(schema.GroupVersionKind{Group: "other.example.com", Version: "v1", Kind: "ConfigMap"}, key.Namespace, key.Name),
		resource(schema.GroupVersionKind{Group: "config.example.com", Version: "v2", Kind: "ConfigMap"}, key.Namespace, key.Name),
		resource(schema.GroupVersionKind{Group: "config.example.com", Version: "v1", Kind: "Secret"}, key.Namespace, key.Name),
		resource(gvk, "rendered-namespace", key.Name),
		resource(gvk, key.Namespace, "odh-other-config"),
		resource(gvk, key.Namespace, key.Name),
	}

	found := findResource(resources, gvk, key)
	g.Expect(found).To(BeIdenticalTo(&resources[5]), "the exact identity after the lookalikes matches")
	g.Expect(found.GetName()).To(Equal(key.Name))
}

// TestFindResourceRejectsPartialIdentities pins that every identity
// component is required on its own: a candidate differing in exactly
// one of group, version, kind, namespace, or name never matches.
func TestFindResourceRejectsPartialIdentities(t *testing.T) {
	t.Parallel()

	gvk := schema.GroupVersionKind{Group: "config.example.com", Version: "v1", Kind: "ConfigMap"}
	key := types.NamespacedName{Namespace: "opendatahub", Name: "odh-config"}

	cases := []struct {
		name      string
		candidate unstructured.Unstructured
	}{
		{
			"different group",
			resource(schema.GroupVersionKind{Group: "other.example.com", Version: "v1", Kind: "ConfigMap"}, key.Namespace, key.Name),
		},
		{
			"different version",
			resource(schema.GroupVersionKind{Group: "config.example.com", Version: "v2", Kind: "ConfigMap"}, key.Namespace, key.Name),
		},
		{
			"different kind",
			resource(schema.GroupVersionKind{Group: "config.example.com", Version: "v1", Kind: "Secret"}, key.Namespace, key.Name),
		},
		{
			"different namespace",
			resource(gvk, "rendered-namespace", key.Name),
		},
		{
			"different name",
			resource(gvk, key.Namespace, "odh-other-config"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			g.Expect(findResource([]unstructured.Unstructured{c.candidate}, gvk, key)).To(BeNil())
		})
	}
}

// TestFindResourceRequiresExactNamespace pins that an empty namespace
// in the requested key matches only resources without a namespace:
// namespaced resources are not a fallback.
func TestFindResourceRequiresExactNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	key := types.NamespacedName{Name: "odh-config"}

	namespaced := []unstructured.Unstructured{resource(toolk8s.ConfigMap, "opendatahub", key.Name)}
	g.Expect(findResource(namespaced, toolk8s.ConfigMap, key)).
		To(BeNil(), "a namespaced resource never matches an empty namespace")

	cluster := []unstructured.Unstructured{resource(toolk8s.ConfigMap, "", key.Name)}
	g.Expect(findResource(cluster, toolk8s.ConfigMap, key)).
		To(BeIdenticalTo(&cluster[0]), "an empty namespace matches an empty namespace exactly")
}

// TestFindResourceRequiresStampedIdentity pins that a resource without
// a group/version/kind never matches — nothing is inferred — and an
// empty requested group/version/kind matches nothing either.
func TestFindResourceRequiresStampedIdentity(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	key := types.NamespacedName{Namespace: "opendatahub", Name: "odh-config"}

	unstamped := &unstructured.Unstructured{}
	unstamped.SetNamespace(key.Namespace)
	unstamped.SetName(key.Name)

	g.Expect(findResource([]unstructured.Unstructured{*unstamped}, toolk8s.ConfigMap, key)).
		To(BeNil(), "an unstamped resource never matches")

	g.Expect(findResource([]unstructured.Unstructured{resource(toolk8s.ConfigMap, key.Namespace, key.Name)}, schema.GroupVersionKind{}, key)).
		To(BeNil(), "an empty requested group/version/kind never matches")
}

// TestFindResourceAbsentReturnsNil pins that a nil slice, an empty
// slice, and a slice without the requested identity all return nil.
func TestFindResourceAbsentReturnsNil(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	key := types.NamespacedName{Namespace: "opendatahub", Name: "odh-config"}

	g.Expect(findResource(nil, toolk8s.ConfigMap, key)).To(BeNil(), "a nil slice returns nil")
	g.Expect(findResource([]unstructured.Unstructured{}, toolk8s.ConfigMap, key)).To(BeNil(), "an empty slice returns nil")

	other := []unstructured.Unstructured{resource(toolk8s.Deployment, key.Namespace, key.Name)}
	g.Expect(findResource(other, toolk8s.ConfigMap, key)).To(BeNil(), "a slice without the identity returns nil")
}

// TestFindResourceFirstDuplicateWinsWithoutMutation pins that exact
// duplicate identities resolve to the first element — the returned
// pointer aliases the original slice, never a copy — and that the
// lookup leaves the slice's content and order untouched, on a hit and
// on a miss alike.
func TestFindResourceFirstDuplicateWinsWithoutMutation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	gvk := toolk8s.ConfigMap
	key := types.NamespacedName{Namespace: "opendatahub", Name: "odh-config"}

	first := resource(gvk, key.Namespace, key.Name)
	first.SetLabels(map[string]string{"instance": "first"})
	second := resource(gvk, key.Namespace, key.Name)
	second.SetLabels(map[string]string{"instance": "second"})
	resources := []unstructured.Unstructured{
		first,
		second,
		resource(toolk8s.Deployment, key.Namespace, key.Name),
	}

	snapshot := make([]unstructured.Unstructured, len(resources))
	for i := range resources {
		snapshot[i] = *resources[i].DeepCopy()
	}

	found := findResource(resources, gvk, key)
	g.Expect(found).To(BeIdenticalTo(&resources[0]), "the first of exact duplicates wins, aliased to the original element")

	g.Expect(findResource(resources, toolk8s.Deployment, key)).To(BeIdenticalTo(&resources[2]))
	g.Expect(findResource(resources, toolk8s.Service, key)).To(BeNil())

	g.Expect(resources).To(Equal(snapshot), "lookup never mutates or reorders the slice")
}
