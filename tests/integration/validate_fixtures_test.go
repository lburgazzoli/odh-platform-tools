//go:build integration

// Validate CLI fixtures: one standalone definition with identities
// independent of every other suite fixture, the scripted object
// bodies the offline API harness serves, and the kubeconfig template
// pointing the real CLI process at that harness.
package integration

// The validate chain's fixed identities: definition, instance, GVK,
// controller Deployment, derived platform ConfigMap, namespace, and
// the platform release both sides normalize.
const (
	validateDefinitionName = "validate-module"
	validateInstanceName   = "validate-instance"
	validateModuleGroup    = "validation.opendatahub.io"
	validateModuleVersion  = "v1alpha1"
	validateModuleKind     = "ValidateModule"
	validateModulePlural   = "validatemodules"
	validateDeploymentName = "validate-controller"
	validateConfigMapName  = "odh-validate-module-config"
	validateNamespace      = "validate-ns"
	validatePlatform       = "2.3.4"
)

// validateDefinitionYAML is the validate chain's definition: a
// complete custom-resource and controller identity with no manifests
// source at all — validation loads the definition without rendering,
// so no render source needs to exist.
const validateDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: validate-module
spec:
  instance:
    name: validate-instance
    type:
      group: validation.opendatahub.io
      version: v1alpha1
      kind: ValidateModule
  controller:
    deployment:
      name: validate-controller
      container:
        image: quay.io/example/validate-controller:v1
`

// validateDefinitionNoTypeYAML drops the instance type, so the loaded
// definition has an incomplete module GVK and fails local selector
// validation before any cluster access.
const validateDefinitionNoTypeYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: validate-module
spec:
  controller:
    deployment:
      name: validate-controller
      container:
        image: quay.io/example/validate-controller:v1
`

// validateDiscoveryJSON is the served APIResourceList: one
// cluster-scoped module resource with its status subresource.
const validateDiscoveryJSON = `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"` +
	validateModuleGroup + `/` + validateModuleVersion + `","resources":[` +
	`{"name":"` + validateModulePlural + `","singularName":"validatemodule","namespaced":false,"kind":"` + validateModuleKind + `"},` +
	`{"name":"` + validateModulePlural + `/status","singularName":"validatemodule","namespaced":false,"kind":"` + validateModuleKind + `"}]}`

// validateAPIGroupListJSON and validateAPIVersionsJSON satisfy any
// client bootstrap path before the group/version discovery request.
const (
	validateAPIGroupListJSON = `{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"` +
		validateModuleGroup + `","versions":[{"groupVersion":"` + validateModuleGroup + `/` +
		validateModuleVersion + `","version":"` + validateModuleVersion +
		`"}],"preferredVersion":{"groupVersion":"` + validateModuleGroup + `/` +
		validateModuleVersion + `","version":"` + validateModuleVersion + `"}}]}`

	validateAPIVersionsJSON = `{"kind":"APIVersions","apiVersion":"v1","versions":["v1"]}`
)

// validateHealthyModuleJSON is the ready module custom resource: the
// status contract holds, generations are current, both required
// conditions are True, and the platform release is reported.
const validateHealthyModuleJSON = `{"apiVersion":"` + validateModuleGroup + `/` + validateModuleVersion +
	`","kind":"` + validateModuleKind + `","metadata":{"name":"` + validateInstanceName +
	`","generation":3},"status":{"phase":"Ready","observedGeneration":3,"conditions":[` +
	`{"type":"Ready","status":"True","observedGeneration":3,"lastTransitionTime":"2026-01-02T03:04:05Z"},` +
	`{"type":"ProvisioningSucceeded","status":"True","observedGeneration":3}],` +
	`"releases":[{"name":"platform","version":"` + validatePlatform + `"}]}}`

// validateUnreadyModuleJSON reports a not-yet-healthy controller: the
// Ready condition is False and the phase lags.
const validateUnreadyModuleJSON = `{"apiVersion":"` + validateModuleGroup + `/` + validateModuleVersion +
	`","kind":"` + validateModuleKind + `","metadata":{"name":"` + validateInstanceName +
	`","generation":3},"status":{"phase":"Pending","observedGeneration":3,"conditions":[` +
	`{"type":"Ready","status":"False","observedGeneration":3,"lastTransitionTime":"2026-01-02T03:04:05Z"},` +
	`{"type":"ProvisioningSucceeded","status":"True","observedGeneration":3}],` +
	`"releases":[{"name":"platform","version":"` + validatePlatform + `"}]}}`

// validateStaleModuleJSON lags the resource generation: the
// controller has not observed the latest spec.
const validateStaleModuleJSON = `{"apiVersion":"` + validateModuleGroup + `/` + validateModuleVersion +
	`","kind":"` + validateModuleKind + `","metadata":{"name":"` + validateInstanceName +
	`","generation":3},"status":{"phase":"Ready","observedGeneration":2,"conditions":[` +
	`{"type":"Ready","status":"True","observedGeneration":3,"lastTransitionTime":"2026-01-02T03:04:05Z"},` +
	`{"type":"ProvisioningSucceeded","status":"True","observedGeneration":3}],` +
	`"releases":[{"name":"platform","version":"` + validatePlatform + `"}]}}`

// validateStaleConditionModuleJSON keeps a current status generation
// but one stale condition generation.
const validateStaleConditionModuleJSON = `{"apiVersion":"` + validateModuleGroup + `/` + validateModuleVersion +
	`","kind":"` + validateModuleKind + `","metadata":{"name":"` + validateInstanceName +
	`","generation":3},"status":{"phase":"Ready","observedGeneration":3,"conditions":[` +
	`{"type":"Ready","status":"True","observedGeneration":2,"lastTransitionTime":"2026-01-02T03:04:05Z"},` +
	`{"type":"ProvisioningSucceeded","status":"True","observedGeneration":3}],` +
	`"releases":[{"name":"platform","version":"` + validatePlatform + `"}]}}`

// validateMalformedModuleJSON carries a structurally invalid status
// field: the phase is a number.
const validateMalformedModuleJSON = `{"apiVersion":"` + validateModuleGroup + `/` + validateModuleVersion +
	`","kind":"` + validateModuleKind + `","metadata":{"name":"` + validateInstanceName +
	`","generation":3},"status":{"phase":3,"observedGeneration":3,"conditions":[` +
	`{"type":"Ready","status":"True","observedGeneration":3},` +
	`{"type":"ProvisioningSucceeded","status":"True","observedGeneration":3}],` +
	`"releases":[{"name":"platform","version":"` + validatePlatform + `"}]}}`

// validateHealthyDeploymentJSON is a fully rolled-out controller.
const validateHealthyDeploymentJSON = `{"apiVersion":"apps/v1","kind":"Deployment",` +
	`"metadata":{"name":"` + validateDeploymentName + `","namespace":"` + validateNamespace +
	`","generation":2},"spec":{"replicas":1},"status":{"observedGeneration":2,` +
	`"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1}}`

// validateUnreadyDeploymentJSON is mid-rollout: one replica is not
// yet available.
const validateUnreadyDeploymentJSON = `{"apiVersion":"apps/v1","kind":"Deployment",` +
	`"metadata":{"name":"` + validateDeploymentName + `","namespace":"` + validateNamespace +
	`","generation":2},"spec":{"replicas":1},"status":{"observedGeneration":2,` +
	`"replicas":1,"updatedReplicas":1,"readyReplicas":0,"availableReplicas":0,"unavailableReplicas":1}}`

// validateScaledZeroDeploymentJSON is scaled to zero.
const validateScaledZeroDeploymentJSON = `{"apiVersion":"apps/v1","kind":"Deployment",` +
	`"metadata":{"name":"` + validateDeploymentName + `","namespace":"` + validateNamespace +
	`","generation":2},"spec":{"replicas":0},"status":{"observedGeneration":2,` +
	`"replicas":0,"updatedReplicas":0,"readyReplicas":0,"availableReplicas":0}}`

// validateHealthyConfigMapJSON reports the platform version with a
// leading v, so the passing handshake proves tolerant normalization
// on both sides.
const validateHealthyConfigMapJSON = `{"apiVersion":"v1","kind":"ConfigMap",` +
	`"metadata":{"name":"` + validateConfigMapName + `","namespace":"` + validateNamespace +
	`"},"data":{"platformVersion":"v` + validatePlatform + `"}}`

// validateMismatchedConfigMapJSON reports a different platform
// version than the module consumed.
const validateMismatchedConfigMapJSON = `{"apiVersion":"v1","kind":"ConfigMap",` +
	`"metadata":{"name":"` + validateConfigMapName + `","namespace":"` + validateNamespace +
	`"},"data":{"platformVersion":"9.9.9"}}`

// validateNotFoundJSON is the Kubernetes status body of a missing
// object or group.
const validateNotFoundJSON = `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
	`"code":404,"reason":"NotFound","message":"the server could not find the requested resource"}`

// validateForbiddenJSON is the Kubernetes status body of a denied
// read.
const validateForbiddenJSON = `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
	`"code":403,"reason":"Forbidden","message":"forbidden: User cannot get resource"}`

// validateKubeconfigYAMLFor renders the standalone kubeconfig whose
// context carries the validate namespace and points at one harness
// server URL. The CLI always receives it explicitly; ambient
// kubeconfig resolution never runs.
func validateKubeconfigYAMLFor(serverURL string) string {
	return `apiVersion: v1
kind: Config
clusters:
- name: validate-cluster
  cluster:
    server: ` + serverURL + `
contexts:
- name: validate-context
  context:
    cluster: validate-cluster
    user: validate-user
    namespace: ` + validateNamespace + `
current-context: validate-context
users:
- name: validate-user
  user: {}
`
}
