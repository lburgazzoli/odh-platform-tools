package linter

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"
)

// crdTestBuilder assembles the synthetic module CRD in typed form.
type crdTestBuilder struct {
	crd *apiextensionsv1.CustomResourceDefinition
}

// newCRDTestBuilder returns a contract-conforming module CRD.
func newCRDTestBuilder() *crdTestBuilder {
	return &crdTestBuilder{
		crd: &apiextensionsv1.CustomResourceDefinition{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "apiextensions.k8s.io/v1",
				Kind:       "CustomResourceDefinition",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: testModuleName + "." + testModuleGroup,
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: testModuleGroup,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   testModuleName,
					Singular: "workbench",
					Kind:     testModuleKind,
					ListKind: testModuleKind + "List",
				},
				Scope: apiextensionsv1.ClusterScoped,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:    testModuleVersion,
						Served:  true,
						Storage: true,
						Subresources: &apiextensionsv1.CustomResourceSubresources{
							Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
						},
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: moduleSchema(),
						},
					},
				},
			},
		},
	}
}

// moduleSchema returns a structural schema demonstrating the fixed
// name and the status contract.
func moduleSchema() *apiextensionsv1.JSONSchemaProps {
	return &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		XValidations: []apiextensionsv1.ValidationRule{
			{Rule: "self.metadata.name == 'default-" + testModuleName + "'"},
		},
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"status": statusSchema(),
		},
	}
}

// statusSchema returns the platform status contract shape.
func statusSchema() apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"observedGeneration": {Type: "integer"},
			"conditions": {
				Type: "array",
				Items: &apiextensionsv1.JSONSchemaPropsOrArray{
					Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"type":               {Type: "string"},
							"status":             {Type: "string"},
							"reason":             {Type: "string"},
							"message":            {Type: "string"},
							"lastTransitionTime": {Type: "string", Format: "date-time"},
							"observedGeneration": {Type: "integer"},
						},
					},
				},
			},
			"releases": {
				Type: "array",
				Items: &apiextensionsv1.JSONSchemaPropsOrArray{
					Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"name":    {Type: "string"},
							"version": {Type: "string"},
						},
					},
				},
			},
		},
	}
}

// version returns the mutable configured version entry.
func (b *crdTestBuilder) version() *apiextensionsv1.CustomResourceDefinitionVersion {
	for i := range b.crd.Spec.Versions {
		if b.crd.Spec.Versions[i].Name == testModuleVersion {
			return &b.crd.Spec.Versions[i]
		}
	}

	return nil
}

func TestModuleCRDMissing(t *testing.T) {
	g := NewWithT(t)

	result := validate(t, []unstructured.Unstructured{
		controllerDeployment(testDeployment, testContainer),
	})

	g.Expect(diagnosticIDs(result)).To(ContainElement(CheckModuleCRDMissing))
	g.Expect(diagnosticsWithID(result, CheckModuleCRDMissing)).To(HaveLen(1))
}

func TestModuleCRDDuplicate(t *testing.T) {
	g := NewWithT(t)

	result := validate(t, []unstructured.Unstructured{
		moduleCRDObject(t, nil),
		moduleCRDObject(t, nil),
	})

	findings := diagnosticsWithID(result, CheckModuleCRDDuplicate)
	g.Expect(findings).To(HaveLen(1))
	g.Expect(findings[0].Message).To(ContainSubstring(testModuleName + "." + testModuleGroup))
}

func TestModuleCRDMalformed(t *testing.T) {
	g := NewWithT(t)

	malformed := object("apiextensions.k8s.io/v1", "CustomResourceDefinition", "",
		testModuleName+"."+testModuleGroup)
	malformed.Object["spec"] = map[string]any{
		"group": testModuleGroup,
		"names": map[string]any{"kind": testModuleKind, "plural": testModuleName},
		// versions must be a list, not a string: decoding fails.
		"versions": "not-a-list",
	}

	result := validate(t, []unstructured.Unstructured{malformed})

	g.Expect(diagnosticIDs(result)).To(ContainElement(CheckModuleCRDMalformed))
}

func TestModuleCRDScopeMustBeCluster(t *testing.T) {
	g := NewWithT(t)

	result := validate(t, []unstructured.Unstructured{
		moduleCRDObject(t, func(b *crdTestBuilder) {
			b.crd.Spec.Scope = apiextensionsv1.NamespaceScoped
		}),
	})

	g.Expect(diagnosticIDs(result)).To(ContainElement(CheckModuleCRDScope))
}

func TestModuleCRDNameMustBePluralDerived(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*crdTestBuilder)
		check  string
	}{
		{
			name: "name mismatch",
			mutate: func(b *crdTestBuilder) {
				b.crd.Name = "workbench." + testModuleGroup
			},
			check: CheckModuleCRDName,
		},
		{
			name: "plural missing",
			mutate: func(b *crdTestBuilder) {
				b.crd.Spec.Names.Plural = ""
			},
			check: CheckModuleCRDName,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			result := validate(t, []unstructured.Unstructured{
				moduleCRDObject(t, test.mutate),
			})

			g.Expect(diagnosticIDs(result)).To(ContainElement(test.check))
		})
	}
}

func TestModuleCRDVersionMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*crdTestBuilder)
		check  string
	}{
		{
			name: "duplicate version names",
			mutate: func(b *crdTestBuilder) {
				b.crd.Spec.Versions = append(b.crd.Spec.Versions,
					*b.version())
			},
			check: CheckModuleCRDVersionDuplicate,
		},
		{
			name: "no storage version",
			mutate: func(b *crdTestBuilder) {
				b.version().Storage = false
			},
			check: CheckModuleCRDStorage,
		},
		{
			name: "multiple storage versions",
			mutate: func(b *crdTestBuilder) {
				b.crd.Spec.Versions = append(b.crd.Spec.Versions,
					apiextensionsv1.CustomResourceDefinitionVersion{
						Name:    "v2",
						Served:  true,
						Storage: true,
					})
			},
			check: CheckModuleCRDStorage,
		},
		{
			name: "configured version absent",
			mutate: func(b *crdTestBuilder) {
				b.version().Name = "v2"
			},
			check: CheckModuleCRDVersionNotServed,
		},
		{
			name: "configured version not served",
			mutate: func(b *crdTestBuilder) {
				b.version().Served = false
			},
			check: CheckModuleCRDVersionNotServed,
		},
		{
			name: "status subresource missing",
			mutate: func(b *crdTestBuilder) {
				b.version().Subresources = nil
			},
			check: CheckModuleCRDStatusSubresource,
		},
		{
			name: "schema missing",
			mutate: func(b *crdTestBuilder) {
				b.version().Schema = nil
			},
			check: CheckModuleCRDSchemaStructural,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			result := validate(t, []unstructured.Unstructured{
				moduleCRDObject(t, test.mutate),
			})

			g.Expect(diagnosticIDs(result)).To(ContainElement(test.check))
		})
	}
}

func TestModuleCRDSchemaMustBeStructural(t *testing.T) {
	g := NewWithT(t)

	result := validate(t, []unstructured.Unstructured{
		moduleCRDObject(t, func(b *crdTestBuilder) {
			// properties and additionalProperties are mutually
			// exclusive in a structural schema.
			schema := b.version().Schema.OpenAPIV3Schema
			schema.AdditionalProperties = &apiextensionsv1.JSONSchemaPropsOrBool{
				Allows: true,
				Schema: &apiextensionsv1.JSONSchemaProps{Type: "string"},
			}
		}),
	})

	findings := diagnosticsWithID(result, CheckModuleCRDSchemaStructural)
	g.Expect(findings).To(HaveLen(1))
	g.Expect(findings[0].Message).To(ContainSubstring("structural"))
}

func TestModuleCRDStatusSchemaMatrix(t *testing.T) {
	t.Parallel()

	mutateStatus := func(mutate func(*apiextensionsv1.JSONSchemaProps)) func(*crdTestBuilder) {
		return func(b *crdTestBuilder) {
			status := b.version().Schema.OpenAPIV3Schema.Properties["status"]
			mutate(&status)
			b.version().Schema.OpenAPIV3Schema.Properties["status"] = status
		}
	}

	mutateConditionsItem := func(
		mutate func(item *apiextensionsv1.JSONSchemaProps),
	) func(*crdTestBuilder) {
		return mutateStatus(func(status *apiextensionsv1.JSONSchemaProps) {
			mutate(status.Properties["conditions"].Items.Schema)
		})
	}

	mutateReleasesItem := func(
		mutate func(item *apiextensionsv1.JSONSchemaProps),
	) func(*crdTestBuilder) {
		return mutateStatus(func(status *apiextensionsv1.JSONSchemaProps) {
			mutate(status.Properties["releases"].Items.Schema)
		})
	}

	tests := []struct {
		name   string
		mutate func(*crdTestBuilder)
	}{
		{
			name: "status absent",
			mutate: func(b *crdTestBuilder) {
				delete(b.version().Schema.OpenAPIV3Schema.Properties, "status")
			},
		},
		{
			name: "status not an object",
			mutate: mutateStatus(func(status *apiextensionsv1.JSONSchemaProps) {
				status.Type = "string"
			}),
		},
		{
			name: "observedGeneration not integer",
			mutate: mutateStatus(func(status *apiextensionsv1.JSONSchemaProps) {
				status.Properties["observedGeneration"] = apiextensionsv1.JSONSchemaProps{Type: "string"}
			}),
		},
		{
			name: "conditions absent",
			mutate: mutateStatus(func(status *apiextensionsv1.JSONSchemaProps) {
				delete(status.Properties, "conditions")
			}),
		},
		{
			name: "conditions not an array",
			mutate: mutateStatus(func(status *apiextensionsv1.JSONSchemaProps) {
				status.Properties["conditions"] = apiextensionsv1.JSONSchemaProps{Type: "object"}
			}),
		},
		{
			name: "conditions items not objects",
			mutate: mutateStatus(func(status *apiextensionsv1.JSONSchemaProps) {
				status.Properties["conditions"] = apiextensionsv1.JSONSchemaProps{
					Type:  "array",
					Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &apiextensionsv1.JSONSchemaProps{Type: "string"}},
				}
			}),
		},
		{
			name: "conditions items missing type",
			mutate: mutateConditionsItem(func(item *apiextensionsv1.JSONSchemaProps) {
				delete(item.Properties, "type")
			}),
		},
		{
			name: "conditions lastTransitionTime not date-time",
			mutate: mutateConditionsItem(func(item *apiextensionsv1.JSONSchemaProps) {
				item.Properties["lastTransitionTime"] = apiextensionsv1.JSONSchemaProps{Type: "string"}
			}),
		},
		{
			name: "conditions observedGeneration missing",
			mutate: mutateConditionsItem(func(item *apiextensionsv1.JSONSchemaProps) {
				delete(item.Properties, "observedGeneration")
			}),
		},
		{
			name: "releases absent",
			mutate: mutateStatus(func(status *apiextensionsv1.JSONSchemaProps) {
				delete(status.Properties, "releases")
			}),
		},
		{
			name: "releases items missing version",
			mutate: mutateReleasesItem(func(item *apiextensionsv1.JSONSchemaProps) {
				delete(item.Properties, "version")
			}),
		},
		{
			name: "preserve unknown hiding the contract",
			mutate: func(b *crdTestBuilder) {
				schema := b.version().Schema.OpenAPIV3Schema
				status := schema.Properties["status"]
				status.Properties = nil
				status.XPreserveUnknownFields = new(true)
				schema.Properties["status"] = status
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			result := validate(t, []unstructured.Unstructured{
				moduleCRDObject(t, test.mutate),
			})

			findings := diagnosticsWithID(result, CheckModuleCRDStatusSchema)
			g.Expect(findings).NotTo(BeEmpty())
		})
	}
}

func TestModuleCRDStatusPreserveUnknownStillDemonstrable(t *testing.T) {
	g := NewWithT(t)

	result := validate(t, []unstructured.Unstructured{
		controllerDeployment(testDeployment, testContainer),
		serviceAccountObject(testNamespace),
		moduleCRDObject(t, func(b *crdTestBuilder) {
			schema := b.version().Schema.OpenAPIV3Schema
			status := schema.Properties["status"]
			status.XPreserveUnknownFields = new(true)
			schema.Properties["status"] = status
		}),
	})

	g.Expect(result.Diagnostics).To(BeEmpty())
}
