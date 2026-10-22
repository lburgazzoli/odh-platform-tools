package linter

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	jsonv2 "encoding/json/v2"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// The synthetic module used across the rule tests: a non-ray module
// so the SecurityContextConstraints exception never applies.
const (
	testModuleName    = "workbenches"
	testModuleGroup   = "workbenches.platform.opendatahub.io"
	testModuleVersion = "v1"
	testModuleKind    = "Workbenches"
	testDeployment    = "workbenches-controller"
	testContainer     = "manager"
	testNamespace     = "odh-applications"
)

// testDefinition returns the in-memory identity and selector context
// of the synthetic module. The main container name is omitted so the
// config default interpretation is exercised.
func testDefinition() config.PlatformModuleDefinition {
	return config.PlatformModuleDefinition{
		APIVersion: config.APIVersion,
		Kind:       config.Kind,
		Metadata:   config.Metadata{Name: testModuleName},
		Spec: config.Spec{
			Type: config.Type{
				Group:   testModuleGroup,
				Version: testModuleVersion,
				Kind:    testModuleKind,
			},
			Controller: config.Controller{
				Deployment: config.Deployment{
					Name:      testDeployment,
					Container: &config.ContainerDefinition{},
				},
			},
		},
	}
}

// object builds one in-memory object with an identity.
func object(apiVersion, kind, namespace, name string) unstructured.Unstructured {
	metadata := map[string]any{"name": name}
	if namespace != "" {
		metadata["namespace"] = namespace
	}

	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   metadata,
	}}
}

// withSpec attaches a spec to an object builder result.
func withSpec(object unstructured.Unstructured, spec map[string]any) unstructured.Unstructured {
	object.Object["spec"] = spec
	return object
}

// controllerDeployment builds a contract-conforming controller
// Deployment in the test namespace.
func controllerDeployment(name, containerName string) unstructured.Unstructured {
	containers := []any{}
	if containerName != "" {
		containers = append(containers, map[string]any{
			"name":  containerName,
			"image": "quay.io/example/controller:v1",
		})
	}

	return withSpec(object("apps/v1", "Deployment", testNamespace, name), map[string]any{
		"selector": map[string]any{
			"matchLabels": map[string]any{"app": testModuleName},
		},
		"template": map[string]any{
			"metadata": map[string]any{
				"labels": map[string]any{"app": testModuleName},
			},
			"spec": map[string]any{
				"containers": containers,
			},
		},
	})
}

// serviceAccountObject builds the bundled module ServiceAccount.
func serviceAccountObject(namespace string) unstructured.Unstructured {
	return object("v1", "ServiceAccount", namespace, testModuleName)
}

// toUnstructured converts a typed Kubernetes object into its
// in-memory unstructured form. It uses the v1 wire-format encoder on
// purpose: the k8s API types carry v1 custom marshalers and empty
// sentinel structs whose meaning json v2 omitempty semantics would
// silently drop (for example subresources.status).
func toUnstructured(t *testing.T, value any) unstructured.Unstructured {
	t.Helper()

	g := NewWithT(t)

	data, err := json.Marshal(value)
	g.Expect(err).NotTo(HaveOccurred())

	var content map[string]any
	g.Expect(json.Unmarshal(data, &content)).To(Succeed())

	return unstructured.Unstructured{Object: content}
}

// moduleCRDObject builds the synthetic module CRD in typed form and
// converts it, applying an optional mutation first.
func moduleCRDObject(t *testing.T, mutate func(*crdTestBuilder)) unstructured.Unstructured {
	t.Helper()

	builder := newCRDTestBuilder()
	if mutate != nil {
		mutate(builder)
	}

	return toUnstructured(t, builder.crd)
}

// diagnosticIDs collects the check IDs of a result.
func diagnosticIDs(result Result) []string {
	ids := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		ids = append(ids, diagnostic.ID)
	}

	return ids
}

// diagnosticsWithID filters a result by check ID.
func diagnosticsWithID(result Result, id string) []Diagnostic {
	var filtered []Diagnostic
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.ID == id {
			filtered = append(filtered, diagnostic)
		}
	}

	return filtered
}

// validate runs the validator with the synthetic definition and a
// live context.
func validate(t *testing.T, resources []unstructured.Unstructured) Result {
	t.Helper()

	result, err := Validate(t.Context(), testDefinition(), resources)

	NewWithT(t).Expect(err).NotTo(HaveOccurred())

	return result
}

// validBundle returns a minimal contract-conforming object set.
func validBundle(t *testing.T) []unstructured.Unstructured {
	t.Helper()

	return []unstructured.Unstructured{
		controllerDeployment(testDeployment, testContainer),
		serviceAccountObject(testNamespace),
		moduleCRDObject(t, nil),
	}
}

func TestValidatePassesMinimalValidBundle(t *testing.T) {
	g := NewWithT(t)

	result := validate(t, validBundle(t))

	g.Expect(result.Diagnostics).To(BeEmpty())
	g.Expect(result.HasErrors()).To(BeFalse())
	g.Expect(result.HasWarnings()).To(BeFalse())
}

func TestValidateReturnsErrorOnlyForCancellation(t *testing.T) {
	g := NewWithT(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := Validate(ctx, testDefinition(), validBundle(t))

	g.Expect(err).To(MatchError(context.Canceled))
	g.Expect(result.Diagnostics).To(BeEmpty())

	// Findings are results, not execution errors.
	findings := validate(t, []unstructured.Unstructured{})
	g.Expect(findings.HasErrors()).To(BeTrue())
}

func TestValidateDoesNotMutateInputs(t *testing.T) {
	g := NewWithT(t)

	definition := testDefinition()
	resources := validBundle(t)

	beforeDefinition, err := jsonv2.Marshal(definition)
	g.Expect(err).NotTo(HaveOccurred())

	beforeResources, err := jsonv2.Marshal(resources)
	g.Expect(err).NotTo(HaveOccurred())

	result, err := Validate(t.Context(), definition, resources)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Diagnostics).To(BeEmpty())

	afterDefinition, err := jsonv2.Marshal(definition)
	g.Expect(err).NotTo(HaveOccurred())

	afterResources, err := jsonv2.Marshal(resources)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(afterDefinition).To(MatchJSON(json.RawMessage(beforeDefinition)))
	g.Expect(afterResources).To(MatchJSON(json.RawMessage(beforeResources)))
}

func TestValidateIsOrderIndependent(t *testing.T) {
	g := NewWithT(t)

	// A bundle with findings from every phase, including duplicate
	// identities and list objects whose diagnostics must not depend
	// on input order.
	resources := []unstructured.Unstructured{
		object("v1", "Namespace", "", "odh-applications"),
		listObject(),
		controllerDeployment("some-other-deployment", testContainer),
		controllerDeployment(testDeployment, testContainer),
		serviceAccountObject(testNamespace),
		serviceAccountObject(testNamespace),
		withRoleRef(object("rbac.authorization.k8s.io/v1", "RoleBinding", testNamespace, "module-binding"), map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "Role",
			"name":     "missing-role",
		}),
	}

	var baseline []byte

	permutations := [][]unstructured.Unstructured{resources, slices.Clone(resources)}
	slices.Reverse(permutations[1])

	for i := range resources {
		permutations = append(permutations, slices.Concat(resources[i:], resources[:i]))
	}

	for _, permutation := range permutations {
		result := validate(t, permutation)

		g.Expect(result.HasErrors()).To(BeTrue())

		serialized, err := jsonv2.Marshal(result.Diagnostics)
		g.Expect(err).NotTo(HaveOccurred())

		if baseline == nil {
			baseline = serialized
			continue
		}

		g.Expect(serialized).To(MatchJSON(baseline))
	}

	// The expected independent findings are all present.
	result := validate(t, permutations[0])
	ids := diagnosticIDs(result)

	for _, id := range []string{
		CheckResourceKindDisallowed,
		CheckObjectListUnsupported,
		CheckDeploymentMultiple,
		CheckDuplicateObject,
		CheckRoleReferenceUnresolved,
	} {
		g.Expect(ids).To(ContainElement(id))
	}
}

// withRoleRef attaches a top-level roleRef to a binding builder
// result.
func withRoleRef(object unstructured.Unstructured, roleRef map[string]any) unstructured.Unstructured {
	object.Object["roleRef"] = roleRef
	return object
}

// listObject builds an unsupported v1 List object.
func listObject() unstructured.Unstructured {
	list := object("v1", "List", "", "bundled-list")
	list.Object["items"] = []any{
		map[string]any{"apiVersion": "v1", "kind": "ConfigMap"},
	}

	return list
}

func TestResultSeverityClassification(t *testing.T) {
	g := NewWithT(t)

	g.Expect((Result{}).HasErrors()).To(BeFalse())
	g.Expect((Result{}).HasWarnings()).To(BeFalse())

	warnings := Result{Diagnostics: []Diagnostic{{ID: "X", Severity: SeverityWarning}}}
	g.Expect(warnings.HasErrors()).To(BeFalse())
	g.Expect(warnings.HasWarnings()).To(BeTrue())

	errors := Result{Diagnostics: []Diagnostic{
		{ID: "X", Severity: SeverityInfo},
		{ID: "Y", Severity: SeverityError},
	}}
	g.Expect(errors.HasErrors()).To(BeTrue())
	g.Expect(errors.HasWarnings()).To(BeFalse())
}

func TestDiagnosticPathAndString(t *testing.T) {
	g := NewWithT(t)

	diagnostic := Diagnostic{
		ID:        CheckDuplicateObject,
		Severity:  SeverityError,
		Phase:     PhaseResources,
		GVK:       schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Namespace: testNamespace,
		Name:      testDeployment,
		Field:     "metadata.name",
		Message:   "duplicate object identity in the rendered set",
	}

	g.Expect(diagnostic.Path()).To(Equal(
		"apps/v1, Kind=Deployment/" + testNamespace + "/" + testDeployment + "/metadata.name"))
	g.Expect(diagnostic.String()).To(Equal(
		"error DUPLICATE_OBJECT apps/v1, Kind=Deployment/" + testNamespace + "/" + testDeployment +
			"/metadata.name: duplicate object identity in the rendered set"))

	moduleFinding := moduleDiagnostic(CheckModuleCRDMissing, SeverityError, PhaseCRD,
		newModuleContext(testDefinition()), "", "no CRD")
	g.Expect(moduleFinding.Path()).To(Equal("workbenches.platform.opendatahub.io/v1, Kind=Workbenches"))
}

func TestEmptyResourceListYieldsStructuralFindings(t *testing.T) {
	g := NewWithT(t)

	result := validate(t, nil)

	g.Expect(result.HasErrors()).To(BeTrue())
	g.Expect(diagnosticIDs(result)).To(ConsistOf(
		CheckDeploymentMissing,
		CheckModuleCRDMissing,
		CheckSingletonEvidenceMissing,
	))
}
