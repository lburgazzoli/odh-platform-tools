/*
Copyright 2026 Luca Burgazzoli

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package validator

import (
	"fmt"
	"strings"

	"github.com/blang/semver/v4"
	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/images"
)

// phaseReady is the framework reconciler's ready phase name.
const phaseReady = "Ready"

// deploymentProgressDeadlineExceeded names the rollout failure reason
// the apps/v1 Deployment controller reports on the Progressing
// condition: a failed rollout is Progressing=False with this reason; no
// condition of type ProgressDeadlineExceeded exists.
const deploymentProgressDeadlineExceeded = "ProgressDeadlineExceeded"

// deploymentProgressingCondition names the rollout condition the apps/v1
// Deployment controller maintains.
const deploymentProgressingCondition = "Progressing"

// platformVersionKey is the single platform ConfigMap data key that
// participates in validation.
const platformVersionKey = "platformVersion"

// requiredConditionTypes are the conditions module readiness requires,
// from the framework contract.
var requiredConditionTypes = []string{
	string(frameworkapi.ConditionTypeReady),
	string(frameworkapi.ConditionTypeProvisioningSucceeded),
}

// APIDiscovery carries one discovery outcome for the module
// group/version. NotFound means the server reported the group absent;
// Err carries any other discovery failure.
type APIDiscovery struct {
	// Resources is the discovered resource list for the group/version.
	Resources []metav1.APIResource

	// NotFound reports a missing group/version, which is a finding.
	NotFound bool

	// Err carries a non-NotFound discovery failure.
	Err error
}

// ObjectObservation carries one object read outcome. Err carries a
// non-NotFound read failure; NotFound and a nil Object both mean missing.
type ObjectObservation struct {
	// Object is the read object, nil when missing.
	Object *unstructured.Unstructured

	// NotFound reports the server's not-found verdict.
	NotFound bool

	// Err carries a non-NotFound read failure.
	Err error
}

// Snapshot is one complete observation cycle: everything Evaluate needs,
// with no Kubernetes calls of its own. A later polling layer populates a
// fresh snapshot per cycle.
type Snapshot struct {
	// Selectors identify the observed objects.
	Selectors Selectors

	// Namespace is the resolved namespace for namespaced objects.
	Namespace string

	// Discovery is the module group/version discovery outcome.
	Discovery APIDiscovery

	// Module is the module custom resource read outcome, populated only
	// when a usable cluster-scoped mapping exists.
	Module ObjectObservation

	// Deployment is the controller Deployment read outcome.
	Deployment ObjectObservation

	// ConfigMap is the platform ConfigMap read outcome.
	ConfigMap ObjectObservation
}

// ModuleMapping selects the non-subresource APIResource serving the module
// kind from one discovery document. found is false when no such resource
// exists; a namespaced mapping is returned with found true so callers can
// report it distinctly and never treat it as a usable cluster-scoped
// mapping.
func ModuleMapping(gvk schema.GroupVersionKind, resources []metav1.APIResource) (metav1.APIResource, bool) {
	for _, resource := range resources {
		if resource.Kind != gvk.Kind || strings.Contains(resource.Name, "/") {
			continue
		}

		return resource, true
	}

	return metav1.APIResource{}, false
}

// hasStatusEndpoint reports whether the discovery document advertises the
// resource's /status subresource.
func hasStatusEndpoint(resources []metav1.APIResource, resourceName string) bool {
	statusName := resourceName + "/status"

	for _, resource := range resources {
		if resource.Name == statusName {
			return true
		}
	}

	return false
}

// Evaluate judges one complete observation cycle. It performs no I/O and
// no mutation; every read outcome is already in the snapshot. Independent
// checks are evaluated even when another branch fails, and a prerequisite
// failure skips a check only when its required input is actually
// unavailable.
func Evaluate(snapshot Snapshot) Result {
	result := Result{Namespace: snapshot.Namespace}
	checks := make([]CheckResult, 0, 10)

	api := evaluateModuleAPI(snapshot.Selectors, snapshot.Discovery)
	checks = append(checks, api.check)

	moduleIdentity := func(id string) CheckResult {
		return CheckResult{ID: id, GVK: snapshot.Selectors.Module, Name: snapshot.Selectors.ModuleName}
	}

	moduleFetched := false
	var status statusObservation

	if !api.usable {
		checks = append(checks,
			skipped(moduleIdentity(CheckModuleExists), "module existence requires a usable cluster-scoped API mapping"),
			skipped(moduleIdentity(CheckModuleContract), "status validation requires a usable cluster-scoped API mapping"),
			skipped(moduleIdentity(CheckModuleReady), "readiness requires a usable cluster-scoped API mapping"))
	} else {
		exists, fetched := evaluateModuleExists(snapshot.Selectors, snapshot.Module)
		checks = append(checks, exists)

		if fetched {
			moduleFetched = true
			status = observeStatus(snapshot.Module.Object)

			checks = append(checks,
				evaluateModuleContract(snapshot.Selectors, status),
				evaluateModuleReady(snapshot.Selectors, snapshot.Module.Object, status))
		} else {
			checks = append(checks,
				skipped(moduleIdentity(CheckModuleContract), "status validation requires the module custom resource"),
				skipped(moduleIdentity(CheckModuleReady), "readiness requires the module custom resource"))
		}
	}

	checks = append(checks, evaluateDeployment(snapshot.Namespace, snapshot.Selectors, snapshot.Deployment))

	checks = append(checks,
		evaluateControllerImage(snapshot.Namespace, snapshot.Selectors, snapshot.Deployment),
		evaluateInitContainer(snapshot.Namespace, snapshot.Selectors, snapshot.Deployment),
		evaluateRelatedImages(snapshot.Namespace, snapshot.Selectors, snapshot.Deployment))

	configCheck, platformVersion, platformVersionValid :=
		evaluatePlatformConfig(snapshot.Namespace, snapshot.Selectors, snapshot.ConfigMap)
	checks = append(checks, configCheck)

	checks = append(checks, evaluatePlatformConsumed(
		snapshot.Selectors, moduleFetched, status,
		snapshot.Selectors.ConfigMapName, platformVersion, platformVersionValid))

	result.Checks = checks

	return result
}

// moduleAPIEvaluation pairs the API check with the mapping usability the
// CR fetch depends on. A mapping missing only the /status endpoint is
// usable: the check fails, yet the CR is still fetched and evaluated.
type moduleAPIEvaluation struct {
	check CheckResult

	// usable reports a discovered cluster-scoped mapping.
	usable bool
}

func evaluateModuleAPI(selectors Selectors, discovery APIDiscovery) moduleAPIEvaluation {
	identity := CheckResult{ID: CheckModuleAPI, GVK: selectors.Module}
	groupVersion := selectors.Module.GroupVersion()

	switch {
	case discovery.Err != nil:
		return moduleAPIEvaluation{check: errored(identity,
			fmt.Sprintf("discover API %s: %v", groupVersion, discovery.Err))}
	case discovery.NotFound:
		return moduleAPIEvaluation{check: failed(identity, "",
			fmt.Sprintf("API %s is not discoverable; the module CRD must be installed first", groupVersion))}
	}

	resource, found := ModuleMapping(selectors.Module, discovery.Resources)
	switch {
	case !found:
		return moduleAPIEvaluation{check: failed(identity, "",
			fmt.Sprintf("API %s does not serve a resource for kind %q", groupVersion, selectors.Module.Kind))}
	case resource.Namespaced:
		return moduleAPIEvaluation{check: failed(identity, "",
			fmt.Sprintf("API %s serves kind %q as namespaced; the module custom resource must be cluster-scoped",
				groupVersion, selectors.Module.Kind))}
	}

	if !hasStatusEndpoint(discovery.Resources, resource.Name) {
		return moduleAPIEvaluation{
			usable: true,
			check: failed(identity, "",
				fmt.Sprintf("API %s does not advertise a /status endpoint for %q", groupVersion, resource.Name)),
		}
	}

	return moduleAPIEvaluation{
		usable: true,
		check: passed(identity, fmt.Sprintf(
			"API %s serves cluster-scoped %q with a /status endpoint", groupVersion, resource.Name)),
	}
}

// evaluateModuleExists reports the CR read outcome. The second result is
// true whenever the object was read, including a terminating one whose
// status may still be diagnosed.
func evaluateModuleExists(selectors Selectors, observation ObjectObservation) (CheckResult, bool) {
	identity := CheckResult{ID: CheckModuleExists, GVK: selectors.Module, Name: selectors.ModuleName}
	target := describe(selectors.Module.Kind, selectors.ModuleName)

	switch {
	case observation.Err != nil:
		return errored(identity, fmt.Sprintf("read module %s: %v", target, observation.Err)), false
	case observation.NotFound || observation.Object == nil:
		return failed(identity, "", fmt.Sprintf(
			"module %s was not found; create the module instance (deploy does not create it)", target)), false
	case terminating(observation.Object):
		return failed(identity, "", fmt.Sprintf("module %s is terminating", target)), true
	default:
		return passed(identity, fmt.Sprintf("module %s exists", target)), true
	}
}

func evaluateModuleContract(selectors Selectors, status statusObservation) CheckResult {
	identity := CheckResult{ID: CheckModuleContract, GVK: selectors.Module, Name: selectors.ModuleName}

	if len(status.violations) > 0 {
		return failedFindings(identity, status.violations)
	}

	return passed(identity, "module status conforms to the platform contract")
}

func evaluateModuleReady(
	selectors Selectors,
	cr *unstructured.Unstructured,
	status statusObservation,
) CheckResult {
	identity := CheckResult{ID: CheckModuleReady, GVK: selectors.Module, Name: selectors.ModuleName}

	if !status.phaseValid || !status.observedGenerationValid || !status.conditionsValid {
		return skipped(identity,
			"readiness requires a structurally valid status phase, observedGeneration, and conditions")
	}

	generation := cr.GetGeneration()
	var findings []violation

	if status.observedGeneration != generation {
		findings = append(findings, violation{
			field: "status.observedGeneration",
			message: fmt.Sprintf("status.observedGeneration is %d; want metadata.generation %d",
				status.observedGeneration, generation),
		})
	}

	if status.phase != phaseReady {
		findings = append(findings, violation{
			field:   "status.phase",
			message: fmt.Sprintf("status.phase is %q; want %q", status.phase, phaseReady),
		})
	}

	for _, required := range requiredConditionTypes {
		condition := findCondition(status.conditions, required)
		switch {
		case condition == nil:
			findings = append(findings, violation{
				field:   "status.conditions",
				message: fmt.Sprintf("required condition %q is missing", required),
			})
		case condition.Status != string(metav1.ConditionTrue):
			findings = append(findings, violation{
				field:   "status.conditions",
				message: fmt.Sprintf("condition %q is %s; want True", required, condition.Status),
			})
		case condition.hasObservedGeneration && condition.ObservedGeneration != generation:
			findings = append(findings, violation{
				field: "status.conditions",
				message: fmt.Sprintf("condition %q observedGeneration is %d; want metadata.generation %d",
					required, condition.ObservedGeneration, generation),
			})
		}
	}

	if len(findings) == 0 {
		return passed(identity, fmt.Sprintf("module is ready at generation %d", generation))
	}

	return failedFindings(identity, findings)
}

func evaluateDeployment(namespace string, selectors Selectors, observation ObjectObservation) CheckResult {
	identity := CheckResult{
		ID:        CheckDeployment,
		GVK:       k8s.Deployment,
		Namespace: namespace,
		Name:      selectors.DeploymentName,
	}

	if namespace == "" {
		return skipped(identity, "deployment validation requires a resolved namespace")
	}

	target := fmt.Sprintf("Deployment %q", selectors.DeploymentName)

	switch {
	case observation.Err != nil:
		return errored(identity, fmt.Sprintf("read %s in namespace %q: %v", target, namespace, observation.Err))
	case observation.NotFound || observation.Object == nil:
		return failed(identity, "", fmt.Sprintf(
			"%s was not found in namespace %q; deploy the module controller first", target, namespace))
	case terminating(observation.Object):
		return failed(identity, "", fmt.Sprintf("%s in namespace %q is terminating", target, namespace))
	}

	object := observation.Object
	generation := object.GetGeneration()
	spec, _ := observedMap(object.Object, "spec")
	status, _ := observedMap(object.Object, "status")

	var findings []violation

	// Effective desired replicas: spec.replicas defaults to one when
	// omitted; malformed types are findings, never silent defaults.
	desired, state := observedNonNegativeInt64(spec, "replicas")
	switch state {
	case fieldAbsent:
		desired = 1
	case fieldMalformed:
		findings = append(findings, violation{
			field:   "spec.replicas",
			message: "spec.replicas must be a nonnegative integer when set",
		})
	default:
		if desired == 0 {
			findings = append(findings, violation{
				field:   "spec.replicas",
				message: fmt.Sprintf("%s in namespace %q is scaled to zero; want at least one replica", target, namespace),
			})
		}
	}

	observedGeneration, state := observedNonNegativeInt64(status, "observedGeneration")
	if state == fieldMalformed {
		findings = append(findings, violation{
			field:   "status.observedGeneration",
			message: "status.observedGeneration must be a nonnegative integer when set",
		})
	}

	// Nil or missing ordinary replica counters mean zero.
	counters := []string{"replicas", "updatedReplicas", "readyReplicas", "availableReplicas", "unavailableReplicas"}
	values := make(map[string]int64, len(counters))

	for _, counter := range counters {
		value, state := observedNonNegativeInt64(status, counter)
		if state == fieldMalformed {
			findings = append(findings, violation{
				field:   "status." + counter,
				message: "status." + counter + " must be a nonnegative integer when set",
			})

			continue
		}

		values[counter] = value
	}

	deadlineExceeded := false

	conditionEntries, state := observedSlice(status, "conditions")
	switch state {
	case fieldMalformed:
		findings = append(findings, violation{
			field:   "status.conditions",
			message: "status.conditions must be an array of condition objects when set",
		})
	case fieldPresent:
		var conditionFindings []violation

		deadlineExceeded, conditionFindings = observeDeploymentConditions(conditionEntries)
		findings = append(findings, conditionFindings...)
	}

	if deadlineExceeded {
		findings = append(findings, violation{
			field: "status.conditions",
			message: "rollout has exceeded its progress deadline " +
				"(condition Progressing is False with reason ProgressDeadlineExceeded)",
		})
	}

	if len(findings) == 0 {
		if observedGeneration < generation {
			findings = append(findings, violation{
				field: "status.observedGeneration",
				message: fmt.Sprintf("status.observedGeneration is %d; want at least metadata.generation %d",
					observedGeneration, generation),
			})
		}

		for _, counter := range counters[:4] {
			if values[counter] != desired {
				findings = append(findings, violation{
					field:   "status." + counter,
					message: fmt.Sprintf("status.%s is %d; want %d", counter, values[counter], desired),
				})
			}
		}

		if values["unavailableReplicas"] != 0 {
			findings = append(findings, violation{
				field:   "status.unavailableReplicas",
				message: fmt.Sprintf("status.unavailableReplicas is %d; want 0", values["unavailableReplicas"]),
			})
		}
	}

	if len(findings) == 0 {
		return passed(identity, fmt.Sprintf(
			"%s in namespace %q is rolled out with %d ready replica(s)", target, namespace, desired))
	}

	return failedFindings(identity, findings)
}

// deploymentTemplate resolves the pod template the controller checks
// observe, or a skip reason naming the missing prerequisite.
func deploymentTemplate(namespace string, observation ObjectObservation) (podTemplateObservation, string) {
	switch {
	case namespace == "":
		return podTemplateObservation{}, "requires a resolved namespace"
	case observation.Err != nil:
		return podTemplateObservation{}, "requires a readable controller Deployment"
	case observation.NotFound || observation.Object == nil:
		return podTemplateObservation{}, "requires the controller Deployment"
	case terminating(observation.Object):
		return podTemplateObservation{}, "requires a non-terminating controller Deployment"
	}

	return observePodTemplate(observation.Object), ""
}

// evaluateControllerImage proves the controller container runs the
// defined controller image: a literal must match exactly; a whole
// {{SOURCE}} token must have been resolved at deploy time to a
// nonempty image that is not the raw token.
func evaluateControllerImage(
	namespace string,
	selectors Selectors,
	observation ObjectObservation,
) CheckResult {
	identity := CheckResult{
		ID:        CheckControllerImage,
		GVK:       k8s.Deployment,
		Namespace: namespace,
		Name:      selectors.DeploymentName,
	}

	expectations := selectors.Controller

	if expectations.ContainerImage == "" {
		return skipped(identity, "controller image validation requires a defined controller image")
	}

	template, reason := deploymentTemplate(namespace, observation)
	if reason != "" {
		return skipped(identity, "controller image validation "+reason)
	}

	findings := append([]violation{}, template.containerFindings...)

	container := findContainer(template.containers, expectations.ContainerName)
	if container == nil {
		findings = append(findings, violation{
			field: "spec.template.spec.containers",
			message: fmt.Sprintf("container %q was not found in Deployment %q; the definition selects it as the controller container",
				expectations.ContainerName, selectors.DeploymentName),
		})
	} else {
		findings = append(findings, controllerImageFindings(
			fmt.Sprintf("spec.template.spec.containers[%d].image", container.Index),
			"container", container.Name, container.Image, expectations.ContainerImage)...)
	}

	if len(findings) == 0 {
		if _, token := images.ImageSourceKey(expectations.ContainerImage); token {
			return passed(identity, fmt.Sprintf(
				"container %q runs %s, the deploy-time resolution of the definition's %s image source",
				expectations.ContainerName, container.Image, expectations.ContainerImage))
		}

		return passed(identity, fmt.Sprintf(
			"container %q runs the defined image %s", expectations.ContainerName, container.Image))
	}

	return failedFindings(identity, findings)
}

// evaluateInitContainer proves the configured init container exists and
// runs the controller image, unless the definition gives it its own. A
// definition that configures no init container skips the check.
func evaluateInitContainer(
	namespace string,
	selectors Selectors,
	observation ObjectObservation,
) CheckResult {
	identity := CheckResult{
		ID:        CheckInitContainer,
		GVK:       k8s.Deployment,
		Namespace: namespace,
		Name:      selectors.DeploymentName,
	}

	expectations := selectors.Controller

	if expectations.InitContainerName == "" {
		return skipped(identity, "definition configures no init container")
	}

	expectedImage := expectations.InitContainerImage
	if expectedImage == "" {
		expectedImage = expectations.ContainerImage
	}

	if expectedImage == "" {
		return skipped(identity, "init container validation requires a defined controller image")
	}

	template, reason := deploymentTemplate(namespace, observation)
	if reason != "" {
		return skipped(identity, "init container validation "+reason)
	}

	findings := append([]violation{}, template.initContainerFindings...)

	init := findContainer(template.initContainers, expectations.InitContainerName)
	if init == nil {
		findings = append(findings, violation{
			field: "spec.template.spec.initContainers",
			message: fmt.Sprintf("init container %q was not found in Deployment %q; the definition selects it as the controller init container",
				expectations.InitContainerName, selectors.DeploymentName),
		})
	} else {
		findings = append(findings, controllerImageFindings(
			fmt.Sprintf("spec.template.spec.initContainers[%d].image", init.Index),
			"init container", init.Name, init.Image, expectedImage)...)
	}

	if len(findings) == 0 {
		if _, token := images.ImageSourceKey(expectedImage); token {
			return passed(identity, fmt.Sprintf(
				"init container %q runs %s, the deploy-time resolution of the definition's %s image source",
				expectations.InitContainerName, init.Image, expectedImage))
		}

		return passed(identity, fmt.Sprintf(
			"init container %q runs the controller image %s", expectations.InitContainerName, init.Image))
	}

	return failedFindings(identity, findings)
}

// evaluateRelatedImages proves every related image the definition
// declares is set as a nonempty env var on the controller container,
// matching the deployer's injection. A definition that declares none
// skips the check.
func evaluateRelatedImages(
	namespace string,
	selectors Selectors,
	observation ObjectObservation,
) CheckResult {
	identity := CheckResult{
		ID:        CheckRelatedImages,
		GVK:       k8s.Deployment,
		Namespace: namespace,
		Name:      selectors.DeploymentName,
	}

	expectations := selectors.Controller

	if len(expectations.RelatedImages) == 0 {
		return skipped(identity, "definition declares no related images")
	}

	template, reason := deploymentTemplate(namespace, observation)
	if reason != "" {
		return skipped(identity, "related images validation "+reason)
	}

	findings := append([]violation{}, template.containerFindings...)

	container := findContainer(template.containers, expectations.ContainerName)
	if container == nil {
		findings = append(findings, violation{
			field: "spec.template.spec.containers",
			message: fmt.Sprintf("container %q was not found in Deployment %q; the definition selects it as the controller container",
				expectations.ContainerName, selectors.DeploymentName),
		})
	} else {
		for _, name := range expectations.RelatedImages {
			field := fmt.Sprintf("spec.template.spec.containers[%d].env", container.Index)

			value, found := container.Env[name]
			switch {
			case !found:
				findings = append(findings, violation{
					field: field,
					message: fmt.Sprintf("related image %q is not set on controller container %q",
						name, container.Name),
				})
			case value == "":
				findings = append(findings, violation{
					field: field,
					message: fmt.Sprintf("related image %q is set on controller container %q with an empty value",
						name, container.Name),
				})
			}
		}
	}

	if len(findings) == 0 {
		return passed(identity, fmt.Sprintf(
			"controller container %q sets every declared related image env var (%s)",
			expectations.ContainerName, strings.Join(expectations.RelatedImages, ", ")))
	}

	return failedFindings(identity, findings)
}

// controllerImageFindings judges one live container image against a
// defined image expectation. A literal must match exactly; a whole
// {{SOURCE}} token must have been resolved at deploy time to a
// nonempty image that is not the raw token — the exact resolved value
// is not observable read-only. The role names the container kind in
// messages.
func controllerImageFindings(field string, role string, name string, live string, expected string) []violation {
	if key, token := images.ImageSourceKey(expected); token {
		switch live {
		case "":
			return []violation{{
				field: field,
				message: fmt.Sprintf("%s %q has no image; the definition's image source {{%s}} must be resolved at deploy time",
					role, name, key),
			}}
		case expected:
			return []violation{{
				field: field,
				message: fmt.Sprintf("%s %q image is the unresolved token {{%s}}; deploy must resolve it",
					role, name, key),
			}}
		default:
			return nil
		}
	}

	if live != expected {
		return []violation{{
			field: field,
			message: fmt.Sprintf("%s %q image is %q; want the defined image %q",
				role, name, live, expected),
		}}
	}

	return nil
}

// observeDeploymentConditions scans Deployment status conditions for the
// progress-deadline failure. Malformed entries are findings.
func observeDeploymentConditions(entries []any) (bool, []violation) {
	deadlineExceeded := false
	var findings []violation

	for i, entry := range entries {
		field := fmt.Sprintf("status.conditions[%d]", i)

		object, ok := entry.(map[string]any)
		if !ok {
			findings = append(findings, violation{field: field, message: field + " must be a condition object"})

			continue
		}

		conditionType, typeState := observedString(object, "type")
		if typeState == fieldMalformed {
			findings = append(findings, violation{
				field:   field + ".type",
				message: field + ".type must be a string when set",
			})
		}

		conditionStatus, statusState := observedString(object, "status")
		if statusState == fieldMalformed {
			findings = append(findings, violation{
				field:   field + ".status",
				message: field + ".status must be a string when set",
			})
		}

		conditionReason, reasonState := observedString(object, "reason")
		if reasonState == fieldMalformed {
			findings = append(findings, violation{
				field:   field + ".reason",
				message: field + ".reason must be a string when set",
			})
		}

		if typeState == fieldPresent && statusState == fieldPresent &&
			conditionType == deploymentProgressingCondition &&
			conditionStatus == string(metav1.ConditionFalse) &&
			reasonState == fieldPresent &&
			conditionReason == deploymentProgressDeadlineExceeded {
			deadlineExceeded = true
		}
	}

	return deadlineExceeded, findings
}

// evaluatePlatformConfig returns the check result plus the normalized
// platform version and its validity, which the consumption check
// compares.
func evaluatePlatformConfig(
	namespace string,
	selectors Selectors,
	observation ObjectObservation,
) (CheckResult, string, bool) {
	identity := CheckResult{
		ID:        CheckPlatformConfig,
		GVK:       k8s.ConfigMap,
		Namespace: namespace,
		Name:      selectors.ConfigMapName,
	}

	if namespace == "" {
		return skipped(identity, "platform configuration validation requires a resolved namespace"), "", false
	}

	target := fmt.Sprintf("ConfigMap %q", selectors.ConfigMapName)

	switch {
	case observation.Err != nil:
		return errored(identity,
			fmt.Sprintf("read %s in namespace %q: %v", target, namespace, observation.Err)), "", false
	case observation.NotFound || observation.Object == nil:
		return failed(identity, "", fmt.Sprintf(
			"%s was not found in namespace %q; the platform deploys it with data.platformVersion",
			target, namespace)), "", false
	case terminating(observation.Object):
		return failed(identity, "", fmt.Sprintf(
			"%s in namespace %q is terminating", target, namespace)), "", false
	}

	data, _ := observedMap(observation.Object.Object, "data")
	version, state := observedString(data, platformVersionKey)

	if state != fieldPresent || strings.TrimSpace(version) == "" {
		return failed(identity, "data."+platformVersionKey, fmt.Sprintf(
			"%s in namespace %q must set data.%s to a semantic version",
			target, namespace, platformVersionKey)), "", false
	}

	parsed, err := semver.ParseTolerant(version)
	if err != nil {
		return failed(identity, "data."+platformVersionKey, fmt.Sprintf(
			"%s in namespace %q has an invalid data.%s %q; it must be a semantic version",
			target, namespace, platformVersionKey, version)), "", false
	}

	normalized := parsed.String()

	return passed(identity, fmt.Sprintf(
			"%s in namespace %q reports %s %s", target, namespace, platformVersionKey, normalized)),
		normalized, true
}

func evaluatePlatformConsumed(
	selectors Selectors,
	moduleFetched bool,
	status statusObservation,
	configMapName string,
	platformVersion string,
	platformVersionValid bool,
) CheckResult {
	identity := CheckResult{ID: CheckPlatformConsumed, GVK: selectors.Module, Name: selectors.ModuleName}

	platform := string(frameworkapi.ReleasePlatform)

	switch {
	case !moduleFetched:
		return skipped(identity, "platform consumption requires the module custom resource")
	case !status.releasesValid:
		return skipped(identity, "platform consumption requires a structurally valid status releases field")
	case !platformVersionValid:
		return skipped(identity, fmt.Sprintf(
			"platform consumption requires a valid data.%s in ConfigMap %q", platformVersionKey, configMapName))
	}

	release := findRelease(status.releases, platform)
	if release == nil {
		return failed(identity, "status.releases", fmt.Sprintf(
			"no release named %q is reported; the module has not published the platform release it reconciled against",
			platform))
	}

	parsed, err := semver.ParseTolerant(release.Version)
	if err != nil {
		return failed(identity, "status.releases", fmt.Sprintf(
			"release %q version %q is not a semantic version", platform, release.Version))
	}

	moduleVersion := parsed.String()
	if moduleVersion != platformVersion {
		return failed(identity, "status.releases", fmt.Sprintf(
			"module reports platform version %s; ConfigMap %q reports %s",
			moduleVersion, configMapName, platformVersion))
	}

	return passed(identity, fmt.Sprintf(
		"module reports platform release %s, matching ConfigMap %q; "+
			"this is read-only consistency evidence, not proof that the module watches the ConfigMap or consumes every key",
		moduleVersion, configMapName))
}
