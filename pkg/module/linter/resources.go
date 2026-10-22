package linter

import (
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// validateObjectContract checks every object's identity, API version,
// scope, kind allowlist membership, and uniqueness. Malformed and
// unsupported objects are findings, never skips of the whole run.
func validateObjectContract(c *collector, module moduleContext, resources []unstructured.Unstructured) {
	seen := make(map[string]struct{}, len(resources))

	for i := range resources {
		object := &resources[i]

		if kind := object.GetKind(); kind == "List" || strings.HasSuffix(kind, "List") {
			c.add(objectDiagnostic(CheckObjectListUnsupported, PhaseResources, object,
				"", "list objects are not supported in module manifests; "+
					"render or unpack the list into individual objects"))
			continue
		}

		gvk := object.GroupVersionKind()
		name := object.GetName()

		var missing []string
		if gvk.Group == "" && gvk.Version == "" {
			missing = append(missing, "apiVersion")
		}

		if gvk.Kind == "" {
			missing = append(missing, "kind")
		}

		if name == "" {
			missing = append(missing, "metadata.name")
		}

		if len(missing) > 0 {
			c.add(objectDiagnostic(CheckObjectIdentityMissing, PhaseResources, object,
				"metadata", "object identity is incomplete; missing "+
					strings.Join(missing, ", ")))
			continue
		}

		rule, allowed := kindRuleFor(module.name, gvk.GroupKind())
		if !allowed {
			message := "resource kind is not in the module manifest allowlist"
			if gvk.GroupKind() == sccGroupKind {
				message = "SecurityContextConstraints is only allowed for the " +
					rayModuleName + " module"
			}

			c.add(objectDiagnostic(CheckResourceKindDisallowed, PhaseResources, object,
				"", message))
			continue
		}

		if gvk.Version != rule.version {
			c.add(objectDiagnostic(CheckResourceVersionUnsupported, PhaseResources, object,
				"apiVersion", "unsupported version; "+gvk.GroupKind().String()+
					" must be "+rule.version))
		}

		if rule.clusterScoped && object.GetNamespace() != "" {
			c.add(objectDiagnostic(CheckResourceScopeMismatch, PhaseResources, object,
				"metadata.namespace", "cluster-scoped kind must not set a namespace"))
		}

		key := gvk.String() + "/" + effectiveNamespace(object.GetNamespace()) + "/" + name
		if _, duplicate := seen[key]; duplicate {
			// The canonical effective namespace makes the finding
			// identical regardless of which spelling (omitted or
			// explicit default) appears second in the input.
			diagnostic := objectDiagnostic(CheckDuplicateObject, PhaseResources, object,
				"metadata.name", "duplicate object identity in the rendered set")
			diagnostic.Namespace = effectiveNamespace(object.GetNamespace())

			c.add(diagnostic)
			continue
		}

		seen[key] = struct{}{}
	}
}

// validateDeploymentContract checks that exactly one apps/v1
// Deployment carries the declared controller name, that every selected
// container appears exactly once in its correct list, and that the
// selector, template, and service account edges hold.
func validateDeploymentContract(
	c *collector,
	module moduleContext,
	resources []unstructured.Unstructured,
	index *objectIndex,
) {
	var deployments []*unstructured.Unstructured

	for i := range resources {
		object := &resources[i]
		if object.GroupVersionKind().GroupKind() == deploymentGroupKind &&
			object.GroupVersionKind().Version == "v1" {
			deployments = append(deployments, object)
		}
	}

	switch len(deployments) {
	case 0:
		c.add(Diagnostic{
			ID:       CheckDeploymentMissing,
			Severity: SeverityError,
			Phase:    PhaseDeployment,
			GVK:      schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Name:     module.deploymentName,
			Message: "no apps/v1 Deployment in the rendered set; the module must " +
				"bundle its controller Deployment named " + module.deploymentName,
		})
		return
	case 1:
	default:
		names := make([]string, 0, len(deployments))
		for _, deployment := range deployments {
			names = append(names, deployment.GetName())
		}
		slices.Sort(names)

		c.add(Diagnostic{
			ID:       CheckDeploymentMultiple,
			Severity: SeverityError,
			Phase:    PhaseDeployment,
			GVK:      schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Name:     module.deploymentName,
			Message: "expected exactly one apps/v1 Deployment, found " +
				strconv.Itoa(len(deployments)) + ": " + strings.Join(names, ", "),
		})
		return
	}

	deployment := deployments[0]

	if deployment.GetName() != module.deploymentName {
		c.add(objectDiagnostic(CheckDeploymentNameMismatch, PhaseDeployment, deployment,
			"metadata.name", "controller Deployment must be named "+module.deploymentName+
				" as declared by the definition"))
		return
	}

	validateContainerSelection(c, deployment, "main container", "containers", module.mainContainer)

	if module.initContainer != "" {
		validateContainerSelection(c, deployment,
			"init container", "initContainers", module.initContainer)
	}

	validateDeploymentSelector(c, deployment)
	validateDeploymentServiceAccount(c, deployment, index)
}

// validateContainerSelection checks that the selected container name
// appears exactly once in its correct pod spec list.
func validateContainerSelection(
	c *collector,
	deployment *unstructured.Unstructured,
	role, list, name string,
) {
	correctList := "spec.template.spec." + list
	wrongList := "initContainers"
	if list == "initContainers" {
		wrongList = "containers"
	}

	correct := countContainerName(deployment, list, name)
	wrong := countContainerName(deployment, wrongList, name)

	switch {
	case correct == 0 && wrong > 0:
		c.add(objectDiagnostic(CheckContainerListMismatch, PhaseDeployment, deployment,
			correctList, "selected "+role+" "+name+" appears in "+wrongList+
				" instead of "+list))
	case correct == 0:
		c.add(objectDiagnostic(CheckContainerMissing, PhaseDeployment, deployment,
			correctList, "selected "+role+" "+name+" is not present; the linter "+
				"never infers or creates missing containers"))
	case correct > 1:
		c.add(objectDiagnostic(CheckContainerDuplicate, PhaseDeployment, deployment,
			correctList, "selected "+role+" "+name+" appears "+strconv.Itoa(correct)+
				" times in "+list+", expected exactly once"))
	case wrong > 0:
		c.add(objectDiagnostic(CheckContainerListMismatch, PhaseDeployment, deployment,
			correctList, "selected "+role+" "+name+" also appears in "+wrongList+
				"; it belongs in exactly one list"))
	}
}

// countContainerName counts containers with the given name in one
// pod spec list of a Deployment.
func countContainerName(deployment *unstructured.Unstructured, list, name string) int {
	items, found, err := unstructured.NestedSlice(
		deployment.Object, "spec", "template", "spec", list)
	if err != nil || !found {
		return 0
	}

	count := 0
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}

		if entryName, _, err := unstructured.NestedString(entry, "name"); err == nil && entryName == name {
			count++
		}
	}

	return count
}

// validateDeploymentSelector checks the selector and template label
// edge: the selector must exist and its matchLabels must match the
// pod template labels.
func validateDeploymentSelector(c *collector, deployment *unstructured.Unstructured) {
	selector, found, err := unstructured.NestedStringMap(
		deployment.Object, "spec", "selector", "matchLabels")

	expressions, hasExpressions, _ := unstructured.NestedSlice(
		deployment.Object, "spec", "selector", "matchExpressions")

	if (err != nil || !found || len(selector) == 0) && (!hasExpressions || len(expressions) == 0) {
		c.add(objectDiagnostic(CheckDeploymentSelectorMissing, PhaseDeployment, deployment,
			"spec.selector", "Deployment must declare a non-empty pod selector"))
		return
	}

	if len(selector) == 0 {
		// Only matchExpressions: nothing to compare against labels.
		return
	}

	labels, _, _ := unstructured.NestedStringMap(
		deployment.Object, "spec", "template", "metadata", "labels")

	mismatched := make([]string, 0, len(selector))
	for key, want := range selector {
		// Kubernetes equality selectors require the key to exist:
		// a template without the key never matches, even when the
		// selector value is the empty string.
		value, found := labels[key]
		if !found || value != want {
			mismatched = append(mismatched, key)
		}
	}

	if len(mismatched) > 0 {
		slices.Sort(mismatched)

		c.add(objectDiagnostic(CheckDeploymentSelectorMismatch, PhaseDeployment, deployment,
			"spec.selector.matchLabels", "selector keys do not match the pod template labels: "+
				strings.Join(mismatched, ", ")))
	}
}

// validateDeploymentServiceAccount checks that a non-default
// serviceAccountName resolves to a bundled ServiceAccount in the same
// effective namespace.
func validateDeploymentServiceAccount(
	c *collector,
	deployment *unstructured.Unstructured,
	index *objectIndex,
) {
	serviceAccount, found, err := unstructured.NestedString(
		deployment.Object, "spec", "template", "spec", "serviceAccountName")
	if err != nil || !found || serviceAccount == "" || serviceAccount == "default" {
		return
	}

	if index.has(serviceAccountGroupKind, deployment.GetNamespace(), serviceAccount) {
		return
	}

	c.add(objectDiagnostic(CheckServiceAccountUnresolved, PhaseDeployment, deployment,
		"spec.template.spec.serviceAccountName", "serviceAccountName "+serviceAccount+
			" has no bundled ServiceAccount in namespace "+
			effectiveNamespace(deployment.GetNamespace())))
}

// validateRBACContract checks RoleBinding and ClusterRoleBinding role
// references and ServiceAccount subjects against bundled objects.
// Non-ServiceAccount subjects (users, groups) are not verifiable
// statically and are skipped.
func validateRBACContract(c *collector, resources []unstructured.Unstructured, index *objectIndex) {
	for i := range resources {
		object := &resources[i]

		switch object.GroupVersionKind().GroupKind() {
		case roleBindingGroupKind:
			validateBinding(c, object, index, false)
		case clusterRoleBindingGroupKind:
			validateBinding(c, object, index, true)
		}
	}
}

// validateBinding checks one RoleBinding or ClusterRoleBinding.
func validateBinding(
	c *collector,
	object *unstructured.Unstructured,
	index *objectIndex,
	cluster bool,
) {
	validateRoleReference(c, object, index, cluster)
	validateBindingSubjects(c, object, index)
}

// validateRoleReference checks that roleRef names a bundled Role or
// ClusterRole with the expected shape.
func validateRoleReference(
	c *collector,
	object *unstructured.Unstructured,
	index *objectIndex,
	cluster bool,
) {
	kind, kindFound, _ := unstructured.NestedString(object.Object, "roleRef", "kind")
	name, nameFound, _ := unstructured.NestedString(object.Object, "roleRef", "name")
	apiGroup, groupFound, _ := unstructured.NestedString(object.Object, "roleRef", "apiGroup")

	field := "roleRef"

	switch {
	case !kindFound || !nameFound || !groupFound,
		kind == "" || name == "" || apiGroup == "":
		c.add(objectDiagnostic(CheckRoleReferenceMalformed, PhaseRBAC, object, field,
			"roleRef must set kind, name, and apiGroup"))
		return
	case apiGroup != rbacAPIGroup:
		c.add(objectDiagnostic(CheckRoleReferenceMalformed, PhaseRBAC, object, field,
			"roleRef apiGroup must be "+rbacAPIGroup))
		return
	}

	resolved := false

	if cluster {
		if kind != "ClusterRole" {
			c.add(objectDiagnostic(CheckRoleReferenceMalformed, PhaseRBAC, object, field,
				"a ClusterRoleBinding roleRef must reference a ClusterRole, found "+kind))
			return
		}

		resolved = index.has(clusterRoleGroupKind, "", name)
	} else {
		switch kind {
		case "Role":
			resolved = index.has(roleGroupKind, object.GetNamespace(), name)
		case "ClusterRole":
			resolved = index.has(clusterRoleGroupKind, "", name)
		default:
			c.add(objectDiagnostic(CheckRoleReferenceMalformed, PhaseRBAC, object, field,
				"roleRef kind must be Role or ClusterRole, found "+kind))
			return
		}
	}

	if !resolved {
		c.add(objectDiagnostic(CheckRoleReferenceUnresolved, PhaseRBAC, object, field,
			"roleRef "+kind+" "+name+" has no bundled object"))
	}
}

// validateBindingSubjects checks ServiceAccount subjects against
// bundled ServiceAccounts. A subject without a namespace refers to
// the binding's own namespace.
func validateBindingSubjects(
	c *collector,
	object *unstructured.Unstructured,
	index *objectIndex,
) {
	subjects, found, err := unstructured.NestedSlice(object.Object, "subjects")
	if err != nil || !found {
		return
	}

	for _, entry := range subjects {
		subject, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		kind, _, _ := unstructured.NestedString(subject, "kind")
		if kind != "ServiceAccount" {
			continue
		}

		name, _, _ := unstructured.NestedString(subject, "name")
		if name == "" {
			continue
		}

		namespace, _, _ := unstructured.NestedString(subject, "namespace")
		if namespace == "" {
			namespace = object.GetNamespace()
		}

		if index.has(serviceAccountGroupKind, namespace, name) {
			continue
		}

		c.add(objectDiagnostic(CheckServiceAccountUnresolved, PhaseRBAC, object,
			"subjects", "subject ServiceAccount "+name+" has no bundled "+
				"ServiceAccount in namespace "+effectiveNamespace(namespace)))
	}
}
