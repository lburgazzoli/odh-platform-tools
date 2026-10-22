package linter

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

func TestObjectIdentityMissing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource unstructured.Unstructured
		missing  string
	}{
		{
			name:     "no apiVersion",
			resource: object("", "ConfigMap", testNamespace, "cm"),
			missing:  "apiVersion",
		},
		{
			name:     "no kind",
			resource: object("v1", "", testNamespace, "cm"),
			missing:  "kind",
		},
		{
			name:     "no name",
			resource: object("v1", "ConfigMap", testNamespace, ""),
			missing:  "metadata.name",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			result := validate(t, []unstructured.Unstructured{test.resource})

			findings := diagnosticsWithID(result, CheckObjectIdentityMissing)
			g.Expect(findings).To(HaveLen(1))
			g.Expect(findings[0].Message).To(ContainSubstring(test.missing))
		})
	}
}

func TestObjectListUnsupported(t *testing.T) {
	g := NewWithT(t)

	result := validate(t, []unstructured.Unstructured{listObject()})

	g.Expect(diagnosticIDs(result)).To(ContainElement(CheckObjectListUnsupported))
}

func TestResourceKindDisallowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource unstructured.Unstructured
	}{
		{
			name:     "namespace",
			resource: object("v1", "Namespace", "", "odh-applications"),
		},
		{
			name:     "stateful set",
			resource: object("apps/v1", "StatefulSet", testNamespace, "controller"),
		},
		{
			name:     "invented CRD group",
			resource: object("example.io/v1", "Widget", testNamespace, "widget"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			result := validate(t, []unstructured.Unstructured{test.resource})

			findings := diagnosticsWithID(result, CheckResourceKindDisallowed)
			g.Expect(findings).To(HaveLen(1))
		})
	}
}

func TestResourceKindSCCRayException(t *testing.T) {
	t.Parallel()

	scc := func(namespace string) unstructured.Unstructured {
		return object("security.openshift.io/v1", "SecurityContextConstraints", namespace, "restricted")
	}

	t.Run("allowed for the ray module", func(t *testing.T) {
		g := NewWithT(t)

		definition := testDefinition()
		definition.Metadata.Name = "ray"

		result, err := Validate(t.Context(), definition, []unstructured.Unstructured{scc("")})
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckResourceKindDisallowed))
	})

	t.Run("disallowed for other modules", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{scc("")})

		findings := diagnosticsWithID(result, CheckResourceKindDisallowed)
		g.Expect(findings).To(HaveLen(1))
		g.Expect(findings[0].Message).To(ContainSubstring("ray"))
	})

	t.Run("scope still enforced for ray", func(t *testing.T) {
		g := NewWithT(t)

		definition := testDefinition()
		definition.Metadata.Name = "ray"

		result, err := Validate(t.Context(), definition, []unstructured.Unstructured{
			scc(testNamespace),
		})
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(diagnosticIDs(result)).To(ContainElement(CheckResourceScopeMismatch))
	})
}

func TestResourceVersionUnsupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource unstructured.Unstructured
	}{
		{
			name:     "network policy beta",
			resource: object("networking.k8s.io/v1beta1", "NetworkPolicy", testNamespace, "policy"),
		},
		{
			name:     "cert-manager alpha issuer",
			resource: object("cert-manager.io/v1alpha3", "Issuer", testNamespace, "issuer"),
		},
		{
			name:     "rbac role beta",
			resource: object("rbac.authorization.k8s.io/v1beta1", "Role", testNamespace, "role"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			result := validate(t, []unstructured.Unstructured{test.resource})

			g.Expect(diagnosticIDs(result)).To(ContainElement(CheckResourceVersionUnsupported))
		})
	}
}

func TestResourceScopeMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource unstructured.Unstructured
	}{
		{
			name:     "cluster role with namespace",
			resource: object("rbac.authorization.k8s.io/v1", "ClusterRole", testNamespace, "role"),
		},
		{
			name: "crd with namespace",
			resource: object("apiextensions.k8s.io/v1", "CustomResourceDefinition", testNamespace,
				"other.things.example.io"),
		},
		{
			name: "webhook configuration with namespace",
			resource: object("admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration",
				testNamespace, "validation"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			result := validate(t, []unstructured.Unstructured{test.resource})

			g.Expect(diagnosticIDs(result)).To(ContainElement(CheckResourceScopeMismatch))
		})
	}
}

func TestDuplicateObjects(t *testing.T) {
	g := NewWithT(t)

	result := validate(t, []unstructured.Unstructured{
		serviceAccountObject(testNamespace),
		serviceAccountObject(testNamespace),
		// An omitted namespace is the default namespace, distinct
		// from the explicit one above.
		serviceAccountObject(""),
		// ... but the same effective namespace as an explicit one.
		serviceAccountObject("default"),
		// A different namespace is not a duplicate.
		serviceAccountObject("other"),
	})

	findings := diagnosticsWithID(result, CheckDuplicateObject)
	g.Expect(findings).To(HaveLen(2))
}

func TestDeploymentCountMatrix(t *testing.T) {
	t.Parallel()

	t.Run("missing", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, nil)

		g.Expect(diagnosticIDs(result)).To(ContainElement(CheckDeploymentMissing))
	})

	t.Run("multiple", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			controllerDeployment(testDeployment, testContainer),
			controllerDeployment(testDeployment, testContainer),
		})

		g.Expect(diagnosticIDs(result)).To(ContainElement(CheckDeploymentMultiple))
		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckDeploymentMissing))
	})

	t.Run("name mismatch skips detail checks", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			controllerDeployment("renamed-controller", "sidecar"),
		})

		ids := diagnosticIDs(result)
		g.Expect(ids).To(ContainElement(CheckDeploymentNameMismatch))
		g.Expect(ids).NotTo(ContainElement(CheckContainerMissing))
	})
}

func TestContainerSelectionMatrix(t *testing.T) {
	deploymentWithLists := func(containers, initContainers []any) unstructured.Unstructured {
		deployment := controllerDeployment(testDeployment, "")
		podSpec := map[string]any{"containers": containers}
		if initContainers != nil {
			podSpec["initContainers"] = initContainers
		}

		deployment.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"] = podSpec

		return deployment
	}

	container := func(name string) map[string]any {
		return map[string]any{"name": name, "image": "quay.io/example/controller:v1"}
	}

	tests := []struct {
		name          string
		definition    config.PlatformModuleDefinition
		containers    []any
		initContainer []any
		check         string
	}{
		{
			name:       "main container missing",
			containers: []any{container("sidecar")},
			check:      CheckContainerMissing,
		},
		{
			name:       "main container duplicated",
			containers: []any{container(testContainer), container(testContainer)},
			check:      CheckContainerDuplicate,
		},
		{
			name:          "main container in init containers",
			containers:    []any{container("sidecar")},
			initContainer: []any{container(testContainer)},
			check:         CheckContainerListMismatch,
		},
		{
			name:          "main container in both lists",
			containers:    []any{container(testContainer)},
			initContainer: []any{container(testContainer)},
			check:         CheckContainerListMismatch,
		},
		{
			name: "init container missing",
			definition: func() config.PlatformModuleDefinition {
				definition := testDefinition()
				definition.Spec.Controller.Deployment.InitContainer = &config.ContainerDefinition{
					Name: new("startup"),
				}

				return definition
			}(),
			containers: []any{container(testContainer)},
			check:      CheckContainerMissing,
		},
		{
			name: "init container in main containers",
			definition: func() config.PlatformModuleDefinition {
				definition := testDefinition()
				definition.Spec.Controller.Deployment.InitContainer = &config.ContainerDefinition{
					Name: new("startup"),
				}

				return definition
			}(),
			containers: []any{container(testContainer), container("startup")},
			check:      CheckContainerListMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			definition := test.definition
			if definition.Spec.Controller.Deployment.Name == "" {
				definition = testDefinition()
			}

			result, err := Validate(t.Context(), definition, []unstructured.Unstructured{
				deploymentWithLists(test.containers, test.initContainer),
			})
			g.Expect(err).NotTo(HaveOccurred())

			g.Expect(diagnosticIDs(result)).To(ContainElement(test.check))
		})
	}
}

func TestContainerSelectionMalformedEntries(t *testing.T) {
	g := NewWithT(t)

	deployment := controllerDeployment(testDeployment, "")
	deployment.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"] = map[string]any{
		"containers": []any{"not-a-map"},
	}

	result := validate(t, []unstructured.Unstructured{deployment})

	g.Expect(diagnosticIDs(result)).To(ContainElement(CheckContainerMissing))
}

func TestDeploymentSelectorMatrix(t *testing.T) {
	deploymentWithSelector := func(selector, labels map[string]any) unstructured.Unstructured {
		deployment := controllerDeployment(testDeployment, testContainer)
		deployment.Object["spec"].(map[string]any)["selector"] = selector
		deployment.Object["spec"].(map[string]any)["template"].(map[string]any)["metadata"] =
			map[string]any{"labels": labels}

		return deployment
	}

	t.Run("missing", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			deploymentWithSelector(map[string]any{}, map[string]any{"app": testModuleName}),
		})

		g.Expect(diagnosticIDs(result)).To(ContainElement(CheckDeploymentSelectorMissing))
	})

	t.Run("mismatch", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			deploymentWithSelector(
				map[string]any{"matchLabels": map[string]any{"app": "other"}},
				map[string]any{"app": testModuleName},
			),
		})

		findings := diagnosticsWithID(result, CheckDeploymentSelectorMismatch)
		g.Expect(findings).To(HaveLen(1))
		g.Expect(findings[0].Message).To(ContainSubstring("app"))
	})

	t.Run("match expressions only", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			deploymentWithSelector(
				map[string]any{"matchExpressions": []any{
					map[string]any{"key": "app", "operator": "Exists"},
				}},
				map[string]any{"app": testModuleName},
			),
		})

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckDeploymentSelectorMissing))
		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckDeploymentSelectorMismatch))
	})
}

func TestDeploymentServiceAccountMatrix(t *testing.T) {
	deploymentWithServiceAccount := func(name string) unstructured.Unstructured {
		deployment := controllerDeployment(testDeployment, testContainer)
		deployment.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"] =
			map[string]any{
				"containers":         []any{map[string]any{"name": testContainer}},
				"serviceAccountName": name,
			}

		return deployment
	}

	t.Run("bundled service account resolves", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			deploymentWithServiceAccount(testModuleName),
			serviceAccountObject(testNamespace),
		})

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckServiceAccountUnresolved))
	})

	t.Run("unresolved service account", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			deploymentWithServiceAccount("missing-sa"),
		})

		findings := diagnosticsWithID(result, CheckServiceAccountUnresolved)
		g.Expect(findings).To(HaveLen(1))
		g.Expect(findings[0].Message).To(ContainSubstring("missing-sa"))
	})

	t.Run("default and empty names are skipped", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			deploymentWithServiceAccount("default"),
		})

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckServiceAccountUnresolved))
	})
}

func TestRBACRoleReferenceMatrix(t *testing.T) {
	roleBinding := func(roleRef map[string]any) unstructured.Unstructured {
		return withRoleRef(
			object("rbac.authorization.k8s.io/v1", "RoleBinding", testNamespace, "module-binding"),
			roleRef,
		)
	}

	clusterRoleBinding := func(roleRef map[string]any) unstructured.Unstructured {
		return withRoleRef(
			object("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", "", "module-binding"),
			roleRef,
		)
	}

	roleRef := func(kind, name, apiGroup string) map[string]any {
		return map[string]any{"kind": kind, "name": name, "apiGroup": apiGroup}
	}

	t.Run("bundled role resolves", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			roleBinding(roleRef("Role", "module-role", "rbac.authorization.k8s.io")),
			object("rbac.authorization.k8s.io/v1", "Role", testNamespace, "module-role"),
		})

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckRoleReferenceUnresolved))
	})

	t.Run("bundled cluster role resolves across namespaces", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			roleBinding(roleRef("ClusterRole", "module-role", "rbac.authorization.k8s.io")),
			object("rbac.authorization.k8s.io/v1", "ClusterRole", "", "module-role"),
		})

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckRoleReferenceUnresolved))
	})

	t.Run("unresolved role", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			roleBinding(roleRef("Role", "missing-role", "rbac.authorization.k8s.io")),
		})

		findings := diagnosticsWithID(result, CheckRoleReferenceUnresolved)
		g.Expect(findings).To(HaveLen(1))
		g.Expect(findings[0].Message).To(ContainSubstring("missing-role"))
	})

	t.Run("role in another namespace does not resolve", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			roleBinding(roleRef("Role", "module-role", "rbac.authorization.k8s.io")),
			object("rbac.authorization.k8s.io/v1", "Role", "other", "module-role"),
		})

		g.Expect(diagnosticIDs(result)).To(ContainElement(CheckRoleReferenceUnresolved))
	})

	t.Run("wrong api group is malformed", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			roleBinding(roleRef("Role", "module-role", "example.io")),
		})

		g.Expect(diagnosticIDs(result)).To(ContainElement(CheckRoleReferenceMalformed))
	})

	t.Run("unknown roleRef kind is malformed", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			roleBinding(roleRef("Widget", "module-role", "rbac.authorization.k8s.io")),
		})

		g.Expect(diagnosticIDs(result)).To(ContainElement(CheckRoleReferenceMalformed))
	})

	t.Run("cluster role binding must reference a cluster role", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			clusterRoleBinding(roleRef("Role", "module-role", "rbac.authorization.k8s.io")),
		})

		g.Expect(diagnosticIDs(result)).To(ContainElement(CheckRoleReferenceMalformed))
	})

	t.Run("cluster role binding resolves a bundled cluster role", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			clusterRoleBinding(roleRef("ClusterRole", "module-role", "rbac.authorization.k8s.io")),
			object("rbac.authorization.k8s.io/v1", "ClusterRole", "", "module-role"),
		})

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckRoleReferenceUnresolved))
	})
}

func TestRBACSubjectMatrix(t *testing.T) {
	bindingWithSubjects := func(namespace string, subjects []any) unstructured.Unstructured {
		binding := withRoleRef(
			object("rbac.authorization.k8s.io/v1", "RoleBinding", namespace, "module-binding"),
			map[string]any{
				"kind":     "Role",
				"name":     "module-role",
				"apiGroup": "rbac.authorization.k8s.io",
			},
		)
		binding.Object["subjects"] = subjects

		return binding
	}

	subject := func(kind, name, namespace string) map[string]any {
		entry := map[string]any{"kind": kind, "name": name}
		if namespace != "" {
			entry["namespace"] = namespace
		}

		return entry
	}

	t.Run("bundled service account with explicit namespace resolves", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			bindingWithSubjects(testNamespace, []any{
				subject("ServiceAccount", testModuleName, testNamespace),
			}),
			serviceAccountObject(testNamespace),
			object("rbac.authorization.k8s.io/v1", "Role", testNamespace, "module-role"),
		})

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckServiceAccountUnresolved))
	})

	t.Run("subject namespace defaults to the binding namespace", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			bindingWithSubjects("", []any{
				subject("ServiceAccount", testModuleName, ""),
			}),
			// Same effective namespace as the binding, which omits
			// its own namespace.
			serviceAccountObject(""),
		})

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckServiceAccountUnresolved))
	})

	t.Run("unresolved service account subject", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			bindingWithSubjects(testNamespace, []any{
				subject("ServiceAccount", "missing-sa", testNamespace),
			}),
		})

		findings := diagnosticsWithID(result, CheckServiceAccountUnresolved)
		g.Expect(findings).To(HaveLen(1))
		g.Expect(findings[0].Field).To(Equal("subjects"))
	})

	t.Run("non service account subjects are skipped", func(t *testing.T) {
		g := NewWithT(t)

		result := validate(t, []unstructured.Unstructured{
			bindingWithSubjects(testNamespace, []any{
				subject("User", "alice@example.io", ""),
				subject("Group", "admins", ""),
			}),
		})

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckServiceAccountUnresolved))
	})

	t.Run("malformed subjects are skipped", func(t *testing.T) {
		g := NewWithT(t)

		binding := withRoleRef(
			object("rbac.authorization.k8s.io/v1", "RoleBinding", testNamespace, "module-binding"),
			map[string]any{
				"kind":     "Role",
				"name":     "module-role",
				"apiGroup": "rbac.authorization.k8s.io",
			},
		)
		binding.Object["subjects"] = "not-a-list"

		result := validate(t, []unstructured.Unstructured{binding})

		g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckServiceAccountUnresolved))
	})
}

func TestDeploymentSelectorRequiresKeyPresence(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	// An empty-valued selector key never matches a template that
	// omits the label, even though the missing key reads as "".
	deployment := controllerDeployment(testDeployment, testContainer)

	spec := deployment.Object["spec"].(map[string]any)
	spec["selector"] = map[string]any{"matchLabels": map[string]any{"app": ""}}

	template := spec["template"].(map[string]any)
	template["metadata"] = map[string]any{"labels": map[string]any{}}

	result := validate(t, []unstructured.Unstructured{deployment})

	findings := diagnosticsWithID(result, CheckDeploymentSelectorMismatch)
	g.Expect(findings).To(HaveLen(1))
	g.Expect(findings[0].Message).To(ContainSubstring("app"))
}

func TestDuplicateObjectFindingIsInputOrderIndependent(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	// The same identity spelled with an omitted and an explicit
	// default namespace is a duplicate in either input order, and the
	// finding is byte-identical in both.
	base := func() []unstructured.Unstructured {
		return []unstructured.Unstructured{
			controllerDeployment(testDeployment, testContainer),
			moduleCRDObject(t, nil),
		}
	}

	omitted := object("v1", "ServiceAccount", "", "shared-sa")
	explicit := object("v1", "ServiceAccount", "default", "shared-sa")

	forward := validate(t, append(base(), omitted, explicit))
	reverse := validate(t, append(base(), explicit, omitted))

	g.Expect(forward.Diagnostics).To(Equal(reverse.Diagnostics))
	g.Expect(diagnosticIDs(forward)).To(ConsistOf(CheckDuplicateObject))
	g.Expect(forward.Diagnostics[0].Namespace).To(Equal("default"))
}
