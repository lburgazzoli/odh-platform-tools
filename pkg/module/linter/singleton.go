package linter

import (
	jsonv2 "encoding/json/v2"
	"regexp"
	"slices"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// celSelfPosition identifies where an x-kubernetes-validations rule
// is anchored: the schema root (self is the whole object), the
// metadata property (self is metadata), or the metadata.name property
// (self is the name string).
type celSelfPosition int

const (
	celSelfRoot celSelfPosition = iota
	celSelfMetadata
	celSelfName
)

// celFixedNamePatterns recognizes, per anchor position, the only CEL
// forms accepted as fixed-name evidence: an unconditional equality
// between the anchored name expression and a string literal, in
// either operand order. Arbitrary logical or conditional expressions
// are never accepted as proof.
var celFixedNamePatterns = map[celSelfPosition][]*regexp.Regexp{
	celSelfRoot: {
		regexp.MustCompile(`^self\.metadata\.name=='([^']+)'$`),
		regexp.MustCompile(`^'([^']+)'==self\.metadata\.name$`),
	},
	celSelfMetadata: {
		regexp.MustCompile(`^self\.name=='([^']+)'$`),
		regexp.MustCompile(`^'([^']+)'==self\.name$`),
	},
	celSelfName: {
		regexp.MustCompile(`^self=='([^']+)'$`),
		regexp.MustCompile(`^'([^']+)'==self$`),
	},
}

// objectNamePattern matches valid nonempty object names (DNS-1123
// subdomains).
var objectNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// validateSingletonEvidence checks that the module's singleton name
// enforcement is demonstrable: recognized fixed-name schema evidence
// passes silently, otherwise only a relevant validating webhook yields
// an unverifiable warning, and the absence of both is an error.
//
// A relevant webhook enforces CREATE (or *) on the exact module
// group, version, and plural (or explicit wildcards) at cluster
// scope, fails closed, and references a bundled Service with a valid
// port and path. Webhook relevance needs the CRD's plural, so a
// missing or duplicate module CRD leaves no usable evidence.
func validateSingletonEvidence(
	c *collector,
	module moduleContext,
	resources []unstructured.Unstructured,
	index *objectIndex,
	crd *moduleCRD,
) {
	if crd != nil && crd.version != nil &&
		crd.version.Schema != nil && crd.version.Schema.OpenAPIV3Schema != nil {
		if _, ok := fixedNameEvidence(crd.version.Schema.OpenAPIV3Schema); ok {
			return
		}
	}

	if crd != nil && crd.plural != "" &&
		relevantSingletonWebhook(resources, index, module, crd.plural) {
		c.add(moduleDiagnostic(CheckSingletonWebhookUnverified, SeverityWarning,
			PhaseSingleton, module, "metadata.name",
			"singleton name enforcement is only evidenced by a validating "+
				"webhook, whose behavior cannot be verified offline"))
		return
	}

	c.add(moduleDiagnostic(CheckSingletonEvidenceMissing, SeverityError,
		PhaseSingleton, module, "metadata.name",
		"no fixed-name evidence for the singleton contract; the CRD schema "+
			"must constrain metadata.name to one literal via CEL or enum, or a "+
			"relevant validating webhook must be bundled"))
}

// fixedNameEvidence reports whether the CRD schema demonstrates the
// singleton name contract: a recognized CEL equality rule at one of
// the supported anchor positions, or a single-value enum on
// metadata.name.
func fixedNameEvidence(schema *apiextensionsv1.JSONSchemaProps) (string, bool) {
	if schema == nil {
		return "", false
	}

	for _, rule := range schema.XValidations {
		if name, ok := fixedNameRule(rule.Rule, celSelfRoot); ok {
			return name, true
		}
	}

	metadata := schema.Properties["metadata"]

	for _, rule := range metadata.XValidations {
		if name, ok := fixedNameRule(rule.Rule, celSelfMetadata); ok {
			return name, true
		}
	}

	nameSchema := metadata.Properties["name"]

	for _, rule := range nameSchema.XValidations {
		if name, ok := fixedNameRule(rule.Rule, celSelfName); ok {
			return name, true
		}
	}

	if len(nameSchema.Enum) == 1 {
		var value string
		if err := jsonv2.Unmarshal(nameSchema.Enum[0].Raw, &value); err == nil &&
			validObjectName(value) {
			return value, true
		}
	}

	return "", false
}

// fixedNameRule recognizes one x-kubernetes-validations rule string
// as a fixed-name equality at the given anchor. Whitespace and
// equality operand order do not matter; the literal must be a valid
// nonempty object name. A rule the CEL parser rejects is never
// evidence: it cannot be admitted with the CRD, so it cannot enforce
// anything.
func fixedNameRule(rule string, position celSelfPosition) (string, bool) {
	if parseCEL(rule) != nil {
		return "", false
	}

	normalized := strings.Join(strings.Fields(rule), "")

	for _, pattern := range celFixedNamePatterns[position] {
		if match := pattern.FindStringSubmatch(normalized); match != nil &&
			validObjectName(match[1]) {
			return match[1], true
		}
	}

	return "", false
}

// validObjectName reports whether name is a valid nonempty object
// name.
func validObjectName(name string) bool {
	return name != "" && len(name) <= 253 && objectNamePattern.MatchString(name)
}

// relevantSingletonWebhook reports whether any bundled
// ValidatingWebhookConfiguration entry could enforce the module's
// singleton name.
func relevantSingletonWebhook(
	resources []unstructured.Unstructured,
	index *objectIndex,
	module moduleContext,
	plural string,
) bool {
	for i := range resources {
		object := &resources[i]

		if object.GroupVersionKind().GroupKind() != validatingWebhookConfigurationGroupKind {
			continue
		}

		webhooks, found, err := unstructured.NestedSlice(object.Object, "webhooks")
		if err != nil || !found {
			continue
		}

		for _, entry := range webhooks {
			webhook, ok := entry.(map[string]any)
			if !ok {
				continue
			}

			if webhookMatchesModule(webhook, module, plural) &&
				webhookServiceBundled(webhook, index) {
				return true
			}
		}
	}

	return false
}

// webhookMatchesModule checks the rule-facing side of webhook
// relevance: fail-closed policy and at least one rule matching the
// module GVR at cluster scope, exactly or via explicit wildcards.
func webhookMatchesModule(webhook map[string]any, module moduleContext, plural string) bool {
	failurePolicy, _, _ := unstructured.NestedString(webhook, "failurePolicy")
	if failurePolicy != "Fail" {
		return false
	}

	rules, found, err := unstructured.NestedSlice(webhook, "rules")
	if err != nil || !found {
		return false
	}

	for _, entry := range rules {
		rule, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		scope, _, _ := unstructured.NestedString(rule, "scope")
		if scope != "Cluster" && scope != "*" {
			continue
		}

		if !matchesField(rule, "operations", "CREATE") ||
			!matchesField(rule, "apiGroups", module.typeGroup) ||
			!matchesField(rule, "apiVersions", module.typeVersion) ||
			!matchesField(rule, "resources", plural) {
			continue
		}

		return true
	}

	return false
}

// matchesField reports whether a rule field lists the exact value or
// an explicit wildcard.
func matchesField(rule map[string]any, field, exact string) bool {
	values, found, err := unstructured.NestedStringSlice(rule, field)
	if err != nil || !found {
		return false
	}

	return slices.ContainsFunc(values, func(value string) bool {
		return value == exact || value == "*"
	})
}

// webhookServiceBundled checks the reference-facing side of webhook
// relevance: a clientConfig.service with a name and namespace, a
// valid optional port and path, and a matching bundled Service.
func webhookServiceBundled(webhook map[string]any, index *objectIndex) bool {
	name, nameFound, err := unstructured.NestedString(webhook, "clientConfig", "service", "name")
	if err != nil || !nameFound || name == "" {
		return false
	}

	namespace, namespaceFound, err := unstructured.NestedString(webhook, "clientConfig", "service", "namespace")
	if err != nil || !namespaceFound || namespace == "" {
		return false
	}

	port, portFound, err := unstructured.NestedInt64(webhook, "clientConfig", "service", "port")
	if err != nil || (portFound && (port < 1 || port > 65535)) {
		return false
	}

	path, pathFound, err := unstructured.NestedString(webhook, "clientConfig", "service", "path")
	if err != nil || (pathFound && path != "" && !strings.HasPrefix(path, "/")) {
		return false
	}

	return index.has(serviceGroupKind, namespace, name)
}
