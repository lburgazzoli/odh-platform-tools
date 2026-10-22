package linter

import (
	"slices"
	"strconv"
	"strings"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// crdScheme decodes and converts custom resource definitions using
// the supported apiextensions machinery. No cluster or network access
// is involved.
var crdScheme = func() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(apiextensions.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))

	return scheme
}()

// crdCodecs builds the decoder for bundled CRD objects.
var crdCodecs = serializer.NewCodecFactory(crdScheme)

// moduleCRD is the selected Module CRD and the configured version
// entry the singleton evidence check builds on.
type moduleCRD struct {
	crd     *apiextensionsv1.CustomResourceDefinition
	version *apiextensionsv1.CustomResourceDefinitionVersion
	plural  string
}

// validateModuleCRD finds the CRD matching the module's declared
// group and kind and checks the Module CRD contract: cluster scope,
// plural-derived name, unique version names, exactly one storage
// version, the configured version served, status subresource, a
// structural OpenAPI schema, and the demonstrable status shape. The
// configured served version entry is checked, not unrelated versions.
func validateModuleCRD(
	c *collector,
	module moduleContext,
	resources []unstructured.Unstructured,
) *moduleCRD {
	var matches []*unstructured.Unstructured

	for i := range resources {
		object := &resources[i]

		if object.GroupVersionKind().GroupKind() != customResourceDefinitionGroupKind {
			continue
		}

		group, _, _ := unstructured.NestedString(object.Object, "spec", "group")
		kind, _, _ := unstructured.NestedString(object.Object, "spec", "names", "kind")

		if group == module.typeGroup && kind == module.typeKind {
			matches = append(matches, object)
		}
	}

	switch len(matches) {
	case 0:
		c.add(moduleDiagnostic(CheckModuleCRDMissing, SeverityError, PhaseCRD, module, "",
			"no CustomResourceDefinition for "+module.moduleGVK().String()+
				" in the rendered set"))
		return nil
	case 1:
	default:
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.GetName())
		}
		slices.Sort(names)

		c.add(moduleDiagnostic(CheckModuleCRDDuplicate, SeverityError, PhaseCRD, module, "",
			"multiple CustomResourceDefinitions for "+module.moduleGVK().String()+
				": "+strings.Join(names, ", ")))
		return nil
	}

	object := matches[0]

	crd, err := decodeCRD(object)
	if err != nil {
		c.add(objectDiagnostic(CheckModuleCRDMalformed, PhaseCRD, object, "",
			"cannot decode CustomResourceDefinition: "+err.Error()))
		return nil
	}

	selected := &moduleCRD{crd: crd, plural: crd.Spec.Names.Plural}

	if crd.Spec.Scope != apiextensionsv1.ClusterScoped {
		c.add(objectDiagnostic(CheckModuleCRDScope, PhaseCRD, object, "spec.scope",
			"Module CRD must be cluster scoped, found "+string(crd.Spec.Scope)))
	}

	if crd.Spec.Names.Plural == "" {
		c.add(objectDiagnostic(CheckModuleCRDName, PhaseCRD, object, "spec.names.plural",
			"Module CRD must declare spec.names.plural to derive its name"))
	} else if expected := crd.Spec.Names.Plural + "." + crd.Spec.Group; crd.Name != expected {
		c.add(objectDiagnostic(CheckModuleCRDName, PhaseCRD, object, "metadata.name",
			"Module CRD name must be "+expected+", found "+crd.Name))
	}

	validateCRDVersions(c, object, module, crd, selected)

	return selected
}

// validateCRDVersions checks version uniqueness, the storage
// invariant, and the configured version's contract entries.
func validateCRDVersions(
	c *collector,
	object *unstructured.Unstructured,
	module moduleContext,
	crd *apiextensionsv1.CustomResourceDefinition,
	selected *moduleCRD,
) {
	seenVersions := make(map[string]struct{}, len(crd.Spec.Versions))
	storageVersions := 0

	for i := range crd.Spec.Versions {
		version := &crd.Spec.Versions[i]

		if _, duplicate := seenVersions[version.Name]; duplicate {
			c.add(objectDiagnostic(CheckModuleCRDVersionDuplicate, PhaseCRD, object,
				"spec.versions", "duplicate version name "+version.Name))
		}

		seenVersions[version.Name] = struct{}{}

		if version.Storage {
			storageVersions++
		}
	}

	if storageVersions != 1 {
		c.add(objectDiagnostic(CheckModuleCRDStorage, PhaseCRD, object, "spec.versions",
			"Module CRD must declare exactly one storage version, found "+
				strconv.Itoa(storageVersions)))
	}

	var configured *apiextensionsv1.CustomResourceDefinitionVersion

	for i := range crd.Spec.Versions {
		if crd.Spec.Versions[i].Name == module.typeVersion {
			configured = &crd.Spec.Versions[i]
			break
		}
	}

	switch {
	case configured == nil:
		c.add(objectDiagnostic(CheckModuleCRDVersionNotServed, PhaseCRD, object,
			"spec.versions", "configured version "+module.typeVersion+
				" is not declared by the CRD"))
		return
	case !configured.Served:
		c.add(objectDiagnostic(CheckModuleCRDVersionNotServed, PhaseCRD, object,
			"spec.versions", "configured version "+module.typeVersion+" is not served"))
		return
	}

	selected.version = configured

	if configured.Subresources == nil || configured.Subresources.Status == nil {
		c.add(objectDiagnostic(CheckModuleCRDStatusSubresource, PhaseCRD, object,
			"spec.versions", "configured version "+module.typeVersion+
				" must enable the status subresource"))
	}

	validateCRDSchema(c, object, module, configured)
}

// validateCRDSchema checks the configured version's OpenAPI schema:
// it must exist, be structural per the apiextensions machinery, and
// demonstrate the status contract shape. Shapes are inspected, not
// whether live optional status fields exist or properties are
// required.
func validateCRDSchema(
	c *collector,
	object *unstructured.Unstructured,
	module moduleContext,
	version *apiextensionsv1.CustomResourceDefinitionVersion,
) {
	if version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
		c.add(objectDiagnostic(CheckModuleCRDSchemaStructural, PhaseCRD, object,
			"spec.versions", "configured version "+module.typeVersion+
				" must declare an OpenAPI schema"))
		return
	}

	schema := version.Schema.OpenAPIV3Schema

	internal := &apiextensions.JSONSchemaProps{}
	if err := crdScheme.Convert(schema, internal, nil); err != nil {
		c.add(objectDiagnostic(CheckModuleCRDMalformed, PhaseCRD, object,
			"spec.versions", "cannot convert schema: "+err.Error()))
		return
	}

	structural, err := structuralschema.NewStructural(internal)
	if err != nil {
		c.add(objectDiagnostic(CheckModuleCRDSchemaStructural, PhaseCRD, object,
			"spec.versions", "schema is not structural: "+err.Error()))
		return
	}

	if errs := structuralschema.ValidateStructural(
		field.NewPath("openAPIV3Schema"), structural); len(errs) > 0 {
		messages := make([]string, 0, len(errs))
		for _, validationError := range errs {
			messages = append(messages, validationError.Error())
		}

		c.add(objectDiagnostic(CheckModuleCRDSchemaStructural, PhaseCRD, object,
			"spec.versions", "schema is not structural: "+
				strings.Join(messages, "; ")))
	}

	validateStatusSchemaShape(c, object, schema)
}

// validateStatusSchemaShape checks that the status property
// demonstrably exposes the platform status contract: an object with
// integer observedGeneration, a conditions array of objects exposing
// type, status, reason, message, lastTransitionTime (date-time
// string), and observedGeneration, and a releases array of objects
// exposing name and version strings. A schema that hides these behind
// preserve-unknown fields cannot demonstrate the contract and is
// flagged; no distribution or repoUrl property is required.
func validateStatusSchemaShape(
	c *collector,
	object *unstructured.Unstructured,
	schema *apiextensionsv1.JSONSchemaProps,
) {
	const fieldPrefix = "schema.openAPIV3Schema.properties.status"

	status, found := schema.Properties["status"]

	preserved := found && status.XPreserveUnknownFields != nil && *status.XPreserveUnknownFields
	note := ""
	if preserved {
		note = " (status preserves unknown fields, which cannot demonstrate the contract)"
	}

	report := func(field, message string) {
		c.add(objectDiagnostic(CheckModuleCRDStatusSchema, PhaseCRD,
			object, field, message))
	}

	expect := func(parent *apiextensionsv1.JSONSchemaProps, prefix, name, typeName string) {
		property, found := parent.Properties[name]
		if !found || property.Type != typeName {
			report(prefix+"."+name, "the status contract requires "+name+
				" as "+typeName+note)
		}
	}

	if !found || status.Type != "object" {
		report(fieldPrefix, "status must be declared as an object"+note)
		return
	}

	expect(&status, fieldPrefix+".properties", "observedGeneration", "integer")

	conditions, found := status.Properties["conditions"]
	const conditionsPrefix = fieldPrefix + ".properties.conditions"

	switch {
	case !found || conditions.Type != "array":
		report(conditionsPrefix, "status must expose conditions as an array"+note)
	default:
		item := itemsObject(conditions)
		if item == nil {
			report(conditionsPrefix+".items", "conditions items must be objects"+note)
			break
		}

		for _, name := range []string{"type", "status", "reason", "message"} {
			expect(item, conditionsPrefix+".items.properties", name, "string")
		}

		expect(item, conditionsPrefix+".items.properties", "observedGeneration", "integer")

		lastTransition, found := item.Properties["lastTransitionTime"]
		if !found || lastTransition.Type != "string" {
			report(conditionsPrefix+".items.properties.lastTransitionTime",
				"conditions items must expose lastTransitionTime as a string"+note)
		} else if lastTransition.Format != "date-time" {
			report(conditionsPrefix+".items.properties.lastTransitionTime",
				"conditions lastTransitionTime must use the date-time format"+note)
		}
	}

	releases, found := status.Properties["releases"]
	const releasesPrefix = fieldPrefix + ".properties.releases"

	switch {
	case !found || releases.Type != "array":
		report(releasesPrefix, "status must expose releases as an array"+note)
	default:
		item := itemsObject(releases)
		if item == nil {
			report(releasesPrefix+".items", "releases items must be objects"+note)
			break
		}

		expect(item, releasesPrefix+".items.properties", "name", "string")
		expect(item, releasesPrefix+".items.properties", "version", "string")
	}
}

// itemsObject returns the single object schema of an array property,
// or nil when the items shape is not a plain object schema.
func itemsObject(array apiextensionsv1.JSONSchemaProps) *apiextensionsv1.JSONSchemaProps {
	if array.Items == nil || array.Items.Schema == nil {
		return nil
	}

	if array.Items.Schema.Type != "object" {
		return nil
	}

	return array.Items.Schema
}

// decodeCRD decodes one bundled CRD into its typed v1 form.
func decodeCRD(object *unstructured.Unstructured) (*apiextensionsv1.CustomResourceDefinition, error) {
	data, err := object.MarshalJSON()
	if err != nil {
		return nil, err
	}

	decoded, _, err := crdCodecs.UniversalDeserializer().Decode(
		data, nil, &apiextensionsv1.CustomResourceDefinition{})
	if err != nil {
		return nil, err
	}

	return decoded.(*apiextensionsv1.CustomResourceDefinition), nil
}
