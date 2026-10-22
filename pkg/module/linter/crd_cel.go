package linter

import (
	"strconv"

	celgo "github.com/google/cel-go/cel"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// celParseEnv is the parse-only CEL environment. Parsing checks rule
// syntax with the same expression engine the API machinery uses,
// without compiling against schema types and without ever executing
// an expression. Env construction cannot fail for the built-in
// standard library.
var celParseEnv = mustNewCELParseEnv()

// mustNewCELParseEnv builds the parse-only CEL environment.
func mustNewCELParseEnv() *celgo.Env {
	env, err := celgo.NewEnv(celgo.StdLib())
	if err != nil {
		panic("linter: build cel parse environment: " + err.Error())
	}

	return env
}

// validateCELSyntax parses every x-kubernetes-validations rule and
// message expression in the configured version's schema. A rule the
// CEL parser rejects cannot be admitted with the CRD, so it is an
// error finding even when the rest of the schema is structural and
// the singleton pattern matcher recognizes the rule text.
func validateCELSyntax(
	c *collector,
	object *unstructured.Unstructured,
	schema *apiextensionsv1.JSONSchemaProps,
) {
	const prefix = "schema.openAPIV3Schema"

	var paths, rules []string

	collectCELRules(prefix, schema, &paths, &rules)

	for i, rule := range rules {
		if err := parseCEL(rule); err != nil {
			c.add(objectDiagnostic(CheckModuleCRDSchemaStructural, PhaseCRD, object,
				paths[i], "x-kubernetes-validations rule does not parse: "+err.Error()))
		}
	}
}

// collectCELRules walks one schema subtree, appending the location
// path and text of every declared validation rule.
func collectCELRules(
	path string,
	schema *apiextensionsv1.JSONSchemaProps,
	paths, rules *[]string,
) {
	if schema == nil {
		return
	}

	for i, rule := range schema.XValidations {
		*paths = append(*paths,
			path+".x-kubernetes-validations["+strconv.Itoa(i)+"].rule")
		*rules = append(*rules, rule.Rule)

		if rule.MessageExpression != "" {
			*paths = append(*paths,
				path+".x-kubernetes-validations["+strconv.Itoa(i)+"].messageExpression")
			*rules = append(*rules, rule.MessageExpression)
		}
	}

	for name, property := range schema.Properties {
		collectCELRules(path+".properties."+name, &property, paths, rules)
	}

	// Tuple-form items and boolean additionalProperties cannot appear
	// in a structural schema, so only the single-schema forms carry
	// validation rules.
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		collectCELRules(path+".additionalProperties",
			schema.AdditionalProperties.Schema, paths, rules)
	}

	if schema.Items != nil && schema.Items.Schema != nil {
		collectCELRules(path+".items", schema.Items.Schema, paths, rules)
	}
}

// parseCEL parses one expression and reports syntax failures.
func parseCEL(expression string) error {
	_, issues := celParseEnv.Parse(expression)
	if issues == nil {
		return nil
	}

	return issues.Err()
}
