package linter

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"
)

// crdWithRootRule replaces the module schema with a root-anchored
// fixed-name rule builder.
func crdWithRootRule(rule string) func(*crdTestBuilder) {
	return func(b *crdTestBuilder) {
		b.version().Schema.OpenAPIV3Schema = &apiextensionsv1.JSONSchemaProps{
			Type:         "object",
			XValidations: []apiextensionsv1.ValidationRule{{Rule: rule}},
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"status": statusSchema(),
			},
		}
	}
}

// crdWithMetadataRule anchors a fixed-name rule on the metadata
// property.
func crdWithMetadataRule(rule string) func(*crdTestBuilder) {
	return func(b *crdTestBuilder) {
		b.version().Schema.OpenAPIV3Schema = &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"metadata": {
					Type:         "object",
					XValidations: []apiextensionsv1.ValidationRule{{Rule: rule}},
				},
				"status": statusSchema(),
			},
		}
	}
}

// crdWithNamedRule builds a schema with nested properties under the
// anchor.
func crdWithNamedRule(mutate func(name *apiextensionsv1.JSONSchemaProps)) func(*crdTestBuilder) {
	return func(b *crdTestBuilder) {
		name := &apiextensionsv1.JSONSchemaProps{Type: "string"}
		if mutate != nil {
			mutate(name)
		}

		b.version().Schema.OpenAPIV3Schema = &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"metadata": {
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"name": *name,
					},
				},
				"status": statusSchema(),
			},
		}
	}
}

func TestSingletonFixedNameEvidenceAccepted(t *testing.T) {
	t.Parallel()

	fixedName := "default-" + testModuleName

	tests := []struct {
		name   string
		mutate func(*crdTestBuilder)
	}{
		{
			name:   "root rule",
			mutate: crdWithRootRule("self.metadata.name == '" + fixedName + "'"),
		},
		{
			name:   "root rule reversed operands",
			mutate: crdWithRootRule("'" + fixedName + "' == self.metadata.name"),
		},
		{
			name:   "root rule irregular whitespace",
			mutate: crdWithRootRule("  self.metadata.name\t==\n'" + fixedName + "'  "),
		},
		{
			name:   "root rule without spaces",
			mutate: crdWithRootRule("self.metadata.name=='" + fixedName + "'"),
		},
		{
			name:   "metadata rule",
			mutate: crdWithMetadataRule("self.name == '" + fixedName + "'"),
		},
		{
			name:   "metadata rule reversed operands",
			mutate: crdWithMetadataRule("'" + fixedName + "' == self.name"),
		},
		{
			name: "name field rule",
			mutate: crdWithNamedRule(func(name *apiextensionsv1.JSONSchemaProps) {
				name.XValidations = []apiextensionsv1.ValidationRule{
					{Rule: "self == '" + fixedName + "'"},
				}
			}),
		},
		{
			name: "name field rule reversed operands",
			mutate: crdWithNamedRule(func(name *apiextensionsv1.JSONSchemaProps) {
				name.XValidations = []apiextensionsv1.ValidationRule{
					{Rule: "'" + fixedName + "' == self"},
				}
			}),
		},
		{
			name: "single value enum",
			mutate: crdWithNamedRule(func(name *apiextensionsv1.JSONSchemaProps) {
				name.Enum = []apiextensionsv1.JSON{
					{Raw: []byte(`"` + fixedName + `"`)},
				}
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			result := validate(t, []unstructured.Unstructured{
				moduleCRDObject(t, test.mutate),
			})

			ids := diagnosticIDs(result)
			g.Expect(ids).NotTo(ContainElement(CheckSingletonEvidenceMissing))
			g.Expect(ids).NotTo(ContainElement(CheckSingletonWebhookUnverified))
		})
	}
}

func TestSingletonArbitraryEvidenceRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*crdTestBuilder)
	}{
		{
			name:   "prefix matching",
			mutate: crdWithRootRule("self.metadata.name.startsWith('default-')"),
		},
		{
			name:   "conditional expression",
			mutate: crdWithRootRule("self.metadata.name == 'a' || self.metadata.name == 'b'"),
		},
		{
			name:   "inequality guard",
			mutate: crdWithRootRule("self.metadata.name != 'other'"),
		},
		{
			name:   "invalid literal name",
			mutate: crdWithRootRule("self.metadata.name == 'Default_Workbenches'"),
		},
		{
			name:   "empty literal",
			mutate: crdWithRootRule("self.metadata.name == ''"),
		},
		{
			name: "multi value enum",
			mutate: crdWithNamedRule(func(name *apiextensionsv1.JSONSchemaProps) {
				name.Enum = []apiextensionsv1.JSON{
					{Raw: []byte(`"default-workbenches"`)},
					{Raw: []byte(`"other"`)},
				}
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			result := validate(t, []unstructured.Unstructured{
				moduleCRDObject(t, test.mutate),
			})

			g.Expect(diagnosticIDs(result)).To(ContainElement(CheckSingletonEvidenceMissing))
		})
	}
}

// singletonWebhook builds a validating webhook configuration and its
// supporting objects.
func singletonWebhook(mutate func(webhook map[string]any)) []unstructured.Unstructured {
	webhook := map[string]any{
		"name":          "v" + testModuleName + ".platform.opendatahub.io",
		"failurePolicy": "Fail",
		"rules": []any{
			map[string]any{
				"operations":  []any{"CREATE"},
				"apiGroups":   []any{testModuleGroup},
				"apiVersions": []any{testModuleVersion},
				"resources":   []any{testModuleName},
				"scope":       "Cluster",
			},
		},
		"clientConfig": map[string]any{
			"service": map[string]any{
				"name":      testModuleName + "-webhook-service",
				"namespace": testNamespace,
				"port":      int64(443),
				"path":      "/validate-" + testModuleName,
			},
		},
	}

	if mutate != nil {
		mutate(webhook)
	}

	configuration := object("admissionregistration.k8s.io/v1",
		"ValidatingWebhookConfiguration", "", testModuleName+"-validation")
	configuration.Object["webhooks"] = []any{webhook}

	return []unstructured.Unstructured{
		configuration,
		object("v1", "Service", testNamespace, testModuleName+"-webhook-service"),
	}
}

func TestSingletonWebhookEvidenceWarns(t *testing.T) {
	g := NewWithT(t)

	// The valid CRD with its CEL rule replaced by an inert one: only
	// the webhook can carry the singleton claim.
	resources := append(singletonWebhook(nil), moduleCRDObject(t,
		crdWithRootRule("self.spec.replicas >= 0")))

	result := validate(t, resources)

	findings := diagnosticsWithID(result, CheckSingletonWebhookUnverified)
	g.Expect(findings).To(HaveLen(1))
	g.Expect(findings[0].Severity).To(Equal(SeverityWarning))
	g.Expect(result.HasWarnings()).To(BeTrue())
	g.Expect(result.HasErrors()).To(BeTrue())
	g.Expect(diagnosticIDs(result)).NotTo(ContainElement(CheckSingletonEvidenceMissing))
}

func TestSingletonWebhookWildcardsWarn(t *testing.T) {
	g := NewWithT(t)

	resources := append(singletonWebhook(func(webhook map[string]any) {
		rule := webhook["rules"].([]any)[0].(map[string]any)
		rule["apiGroups"] = []any{"*"}
		rule["apiVersions"] = []any{"*"}
		rule["resources"] = []any{"*"}
	}), moduleCRDObject(t, crdWithRootRule("self.spec.replicas >= 0")))

	result := validate(t, resources)

	g.Expect(diagnosticIDs(result)).To(ContainElement(CheckSingletonWebhookUnverified))
}

func TestSingletonIrrelevantWebhooksRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(webhook map[string]any)
	}{
		{
			name: "failure policy ignores failures",
			mutate: func(webhook map[string]any) {
				webhook["failurePolicy"] = "Ignore"
			},
		},
		{
			name: "namespaced scope",
			mutate: func(webhook map[string]any) {
				webhook["rules"].([]any)[0].(map[string]any)["scope"] = "Namespaced"
			},
		},
		{
			name: "update operations only",
			mutate: func(webhook map[string]any) {
				webhook["rules"].([]any)[0].(map[string]any)["operations"] = []any{"UPDATE"}
			},
		},
		{
			name: "foreign api group",
			mutate: func(webhook map[string]any) {
				webhook["rules"].([]any)[0].(map[string]any)["apiGroups"] = []any{"other.io"}
			},
		},
		{
			name: "foreign api version",
			mutate: func(webhook map[string]any) {
				webhook["rules"].([]any)[0].(map[string]any)["apiVersions"] = []any{"v2"}
			},
		},
		{
			name: "foreign resource",
			mutate: func(webhook map[string]any) {
				webhook["rules"].([]any)[0].(map[string]any)["resources"] = []any{"others"}
			},
		},
		{
			name: "empty service namespace",
			mutate: func(webhook map[string]any) {
				webhook["clientConfig"].(map[string]any)["service"].(map[string]any)["namespace"] = ""
			},
		},
		{
			name: "service port out of range",
			mutate: func(webhook map[string]any) {
				webhook["clientConfig"].(map[string]any)["service"].(map[string]any)["port"] = int64(0)
			},
		},
		{
			name: "service path without slash",
			mutate: func(webhook map[string]any) {
				webhook["clientConfig"].(map[string]any)["service"].(map[string]any)["path"] = "validate"
			},
		},
		{
			name: "url instead of service",
			mutate: func(webhook map[string]any) {
				webhook["clientConfig"] = map[string]any{
					"url": "https://example.io/validate",
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			resources := append(singletonWebhook(test.mutate),
				moduleCRDObject(t, crdWithRootRule("self.spec.replicas >= 0")))

			result := validate(t, resources)

			ids := diagnosticIDs(result)
			g.Expect(ids).To(ContainElement(CheckSingletonEvidenceMissing))
			g.Expect(ids).NotTo(ContainElement(CheckSingletonWebhookUnverified))
		})
	}
}

func TestSingletonWebhookServiceMustBeBundled(t *testing.T) {
	g := NewWithT(t)

	resources := singletonWebhook(nil)
	// Drop the bundled Service: the webhook reference is dangling.
	resources = resources[:1]

	resources = append(resources, moduleCRDObject(t, crdWithRootRule("self.spec.replicas >= 0")))

	result := validate(t, resources)

	ids := diagnosticIDs(result)
	g.Expect(ids).To(ContainElement(CheckSingletonEvidenceMissing))
	g.Expect(ids).NotTo(ContainElement(CheckSingletonWebhookUnverified))
}

func TestSingletonSchemaAndWebhookTogetherPass(t *testing.T) {
	g := NewWithT(t)

	resources := append(singletonWebhook(nil), moduleCRDObject(t, nil))

	result := validate(t, resources)

	ids := diagnosticIDs(result)
	g.Expect(ids).NotTo(ContainElement(CheckSingletonEvidenceMissing))
	g.Expect(ids).NotTo(ContainElement(CheckSingletonWebhookUnverified))
}

func TestSingletonWithoutCRDYieldsNoEvidence(t *testing.T) {
	g := NewWithT(t)

	// A relevant webhook without the CRD: the plural is unknown, so
	// the webhook cannot be matched and no schema exists.
	result := validate(t, singletonWebhook(nil))

	ids := diagnosticIDs(result)
	g.Expect(ids).To(ContainElement(CheckModuleCRDMissing))
	g.Expect(ids).To(ContainElement(CheckSingletonEvidenceMissing))
}
