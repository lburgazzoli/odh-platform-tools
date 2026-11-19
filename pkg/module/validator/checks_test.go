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
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var testModuleGVK = schema.GroupVersionKind{
	Group:   "mock.opendatahub.io",
	Version: "v1alpha1",
	Kind:    "MockModule",
}

const (
	testNamespace    = "test-ns"
	testPlatform     = "1.2.3"
	testGeneration   = int64(3)
	testReadFailure  = "connection refused"
	testCheckMissing = "missing"
)

func testSelectors() Selectors {
	return Selectors{
		DefinitionName: "mock",
		Module:         testModuleGVK,
		ModuleName:     "mock",
		DeploymentName: "mock-controller",
		ConfigMapName:  "odh-mock-config",
	}
}

// testObject builds an unstructured object; body metadata merges with the
// identity fields. Numbers use int64 so unstructured accessors work.
func testObject(
	gvk schema.GroupVersionKind,
	namespace string,
	name string,
	body map[string]any,
) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: body}
	object.SetGroupVersionKind(gvk)
	object.SetNamespace(namespace)
	object.SetName(name)

	return object
}

func testDiscovery(namespaced bool, withStatus bool) APIDiscovery {
	resources := []metav1.APIResource{{
		Name:         "mockmodules",
		SingularName: "mockmodule",
		Kind:         "MockModule",
		Namespaced:   namespaced,
	}}

	if withStatus {
		resources = append(resources, metav1.APIResource{
			Name:       "mockmodules/status",
			Kind:       "MockModule",
			Namespaced: namespaced,
		})
	}

	return APIDiscovery{Resources: resources}
}

func healthyModuleCR() *unstructured.Unstructured {
	return testObject(testModuleGVK, "", "mock", map[string]any{
		"metadata": map[string]any{
			"generation": testGeneration,
		},
		"status": map[string]any{
			"phase":              "Ready",
			"observedGeneration": testGeneration,
			"conditions": []any{
				map[string]any{
					"type":               "Ready",
					"status":             "True",
					"observedGeneration": testGeneration,
					"lastTransitionTime": "2026-01-02T03:04:05Z",
					"reason":             "MockReady",
				},
				map[string]any{
					"type":               "ProvisioningSucceeded",
					"status":             "True",
					"observedGeneration": testGeneration,
				},
			},
			"releases": []any{
				map[string]any{"name": "platform", "version": testPlatform},
			},
		},
	})
}

func healthyDeployment() *unstructured.Unstructured {
	return testObject(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		testNamespace, "mock-controller", map[string]any{
			"metadata": map[string]any{
				"generation": int64(2),
			},
			"spec": map[string]any{
				"replicas": int64(1),
			},
			"status": map[string]any{
				"observedGeneration": int64(2),
				"replicas":           int64(1),
				"updatedReplicas":    int64(1),
				"readyReplicas":      int64(1),
				"availableReplicas":  int64(1),
			},
		})
}

func healthyConfigMap() *unstructured.Unstructured {
	return testObject(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"},
		testNamespace, "odh-mock-config", map[string]any{
			"data": map[string]any{
				"platformVersion": testPlatform,
			},
		})
}

func healthySnapshot() Snapshot {
	return Snapshot{
		Selectors:  testSelectors(),
		Namespace:  testNamespace,
		Discovery:  testDiscovery(false, true),
		Module:     ObjectObservation{Object: healthyModuleCR()},
		Deployment: ObjectObservation{Object: healthyDeployment()},
		ConfigMap:  ObjectObservation{Object: healthyConfigMap()},
	}
}

// check returns the named check, or a placeholder whose status fails
// every assertion when the check is missing.
func check(result Result, id string) CheckResult {
	for i := range result.Checks {
		if result.Checks[i].ID == id {
			return result.Checks[i]
		}
	}

	return CheckResult{ID: id, Status: testCheckMissing}
}

func healthyCRStatus() map[string]any {
	cr := healthyModuleCR()

	status, _, _ := unstructured.NestedMap(cr.Object, "status")

	return status
}

// TestEvaluateHealthyCycle checks the all-pass cycle: stable order, seven
// checks, object identities, and the resolved namespace.
func TestEvaluateHealthyCycle(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result := Evaluate(healthySnapshot())

	g.Expect(result.Namespace).To(Equal(testNamespace))
	g.Expect(result.Passed()).To(BeTrue())

	ids := make([]string, 0, len(result.Checks))
	for i := range result.Checks {
		ids = append(ids, result.Checks[i].ID)
	}

	g.Expect(ids).To(Equal([]string{
		CheckModuleAPI,
		CheckModuleExists,
		CheckModuleContract,
		CheckModuleReady,
		CheckDeployment,
		CheckPlatformConfig,
		CheckPlatformConsumed,
	}))

	api := check(result, CheckModuleAPI)
	g.Expect(api.Status).To(Equal(StatusPass))
	g.Expect(api.GVK).To(Equal(testModuleGVK))
	g.Expect(api.Name).To(BeEmpty())

	module := check(result, CheckModuleExists)
	g.Expect(module.Status).To(Equal(StatusPass))
	g.Expect(module.GVK).To(Equal(testModuleGVK))
	g.Expect(module.Name).To(Equal("mock"))
	g.Expect(module.Namespace).To(BeEmpty())

	deployment := check(result, CheckDeployment)
	g.Expect(deployment.Status).To(Equal(StatusPass))
	g.Expect(deployment.GVK).To(Equal(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}))
	g.Expect(deployment.Namespace).To(Equal(testNamespace))
	g.Expect(deployment.Name).To(Equal("mock-controller"))

	config := check(result, CheckPlatformConfig)
	g.Expect(config.Status).To(Equal(StatusPass))
	g.Expect(config.GVK).To(Equal(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}))
	g.Expect(config.Namespace).To(Equal(testNamespace))
	g.Expect(config.Name).To(Equal("odh-mock-config"))

	g.Expect(check(result, CheckModuleContract).Status).To(Equal(StatusPass))
	g.Expect(check(result, CheckModuleReady).Status).To(Equal(StatusPass))
	g.Expect(check(result, CheckPlatformConsumed).Status).To(Equal(StatusPass))
}

// TestModuleMapping checks discovery mapping selection: subresources are
// skipped, the plural comes from discovery, and a namespaced mapping is
// reported but never usable.
func TestModuleMapping(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	resource, found := ModuleMapping(testModuleGVK, testDiscovery(false, true).Resources)
	g.Expect(found).To(BeTrue())
	g.Expect(resource.Name).To(Equal("mockmodules"))
	g.Expect(resource.Namespaced).To(BeFalse())

	resource, found = ModuleMapping(testModuleGVK, testDiscovery(true, true).Resources)
	g.Expect(found).To(BeTrue())
	g.Expect(resource.Namespaced).To(BeTrue())

	other := schema.GroupVersionKind{Group: "mock.opendatahub.io", Version: "v1alpha1", Kind: "Other"}
	_, found = ModuleMapping(other, testDiscovery(false, true).Resources)
	g.Expect(found).To(BeFalse())
}

// TestEvaluateModuleAPI covers discovery outcomes and the usable-mapping
// rule: a mapping missing only the status endpoint still allows the CR to
// be fetched and evaluated, while unusable mappings skip only the checks
// that actually need the CR.
func TestEvaluateModuleAPI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		sn     func(snapshot *Snapshot)
		api    string
		exists string
	}{
		{
			name: "discovery error",
			sn: func(snapshot *Snapshot) {
				snapshot.Discovery = APIDiscovery{Err: errors.New(testReadFailure)}
			},
			api:    StatusError,
			exists: StatusSkipped,
		},
		{
			name: "group not found",
			sn: func(snapshot *Snapshot) {
				snapshot.Discovery = APIDiscovery{NotFound: true}
			},
			api:    StatusFail,
			exists: StatusSkipped,
		},
		{
			name: "kind not served",
			sn: func(snapshot *Snapshot) {
				snapshot.Discovery = APIDiscovery{
					Resources: []metav1.APIResource{{
						Name: "others", Kind: "Other", Namespaced: false,
					}},
				}
			},
			api:    StatusFail,
			exists: StatusSkipped,
		},
		{
			name: "namespaced mapping",
			sn: func(snapshot *Snapshot) {
				snapshot.Discovery = testDiscovery(true, true)
			},
			api:    StatusFail,
			exists: StatusSkipped,
		},
		{
			name: "missing status endpoint still fetches the CR",
			sn: func(snapshot *Snapshot) {
				snapshot.Discovery = testDiscovery(false, false)
			},
			api:    StatusFail,
			exists: StatusPass,
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			snapshot := healthySnapshot()
			test.sn(&snapshot)

			result := Evaluate(snapshot)

			g.Expect(result.Checks).To(HaveLen(7))
			g.Expect(check(result, CheckModuleAPI).Status).To(Equal(test.api))
			g.Expect(check(result, CheckModuleExists).Status).To(Equal(test.exists))

			// Independent checks observe the cluster regardless of the
			// module API outcome.
			g.Expect(check(result, CheckDeployment).Status).To(Equal(StatusPass))
			g.Expect(check(result, CheckPlatformConfig).Status).To(Equal(StatusPass))

			if test.exists == StatusPass {
				// A usable mapping with a missing status endpoint still
				// diagnoses the CR itself and the release handshake.
				g.Expect(check(result, CheckModuleContract).Status).To(Equal(StatusPass))
				g.Expect(check(result, CheckModuleReady).Status).To(Equal(StatusPass))
				g.Expect(check(result, CheckPlatformConsumed).Status).To(Equal(StatusPass))
			} else {
				g.Expect(check(result, CheckModuleContract).Status).To(Equal(StatusSkipped))
				g.Expect(check(result, CheckModuleReady).Status).To(Equal(StatusSkipped))

				// Without a fetched CR, consumption has no handshake to
				// read.
				g.Expect(check(result, CheckPlatformConsumed).Status).To(Equal(StatusSkipped))
			}
		})
	}
}

// TestEvaluateModuleExists covers the CR read outcomes. A terminating CR
// fails existence but its still-readable status is diagnosed.
func TestEvaluateModuleExists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sn       func(snapshot *Snapshot)
		exists   string
		contract string
		ready    string
	}{
		{
			name: "missing instance",
			sn: func(snapshot *Snapshot) {
				snapshot.Module = ObjectObservation{NotFound: true}
			},
			exists:   StatusFail,
			contract: StatusSkipped,
			ready:    StatusSkipped,
		},
		{
			name: "read error",
			sn: func(snapshot *Snapshot) {
				snapshot.Module = ObjectObservation{Err: errors.New(testReadFailure)}
			},
			exists:   StatusError,
			contract: StatusSkipped,
			ready:    StatusSkipped,
		},
		{
			name: "terminating instance still diagnoses status",
			sn: func(snapshot *Snapshot) {
				snapshot.Module.Object = healthyModuleCR()
				snapshot.Module.Object.Object["metadata"].(map[string]any)["deletionTimestamp"] =
					"2026-01-02T03:04:05Z"
			},
			exists:   StatusFail,
			contract: StatusPass,
			ready:    StatusPass,
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			snapshot := healthySnapshot()
			test.sn(&snapshot)

			result := Evaluate(snapshot)

			g.Expect(check(result, CheckModuleExists).Status).To(Equal(test.exists))
			g.Expect(check(result, CheckModuleContract).Status).To(Equal(test.contract))
			g.Expect(check(result, CheckModuleReady).Status).To(Equal(test.ready))
		})
	}
}

// TestEvaluateModuleContract covers raw status structure: malformed
// containers and field types, duplicate names, optional metadata, and the
// per-branch suppression rules.
func TestEvaluateModuleContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   any
		contract string
		field    string
		ready    string
		consumed string
	}{
		{
			name:     "absent status is a contract failure with valid branches",
			status:   nil,
			contract: StatusFail,
			field:    "status",
			ready:    StatusFail,
			consumed: StatusFail,
		},
		{
			name:     "non-object status suppresses dependent checks",
			status:   "broken",
			contract: StatusFail,
			field:    "status",
			ready:    StatusSkipped,
			consumed: StatusSkipped,
		},
		{
			name:     "malformed phase suppresses only readiness",
			status:   withStatus(map[string]any{"phase": int64(1)}),
			contract: StatusFail,
			field:    "status.phase",
			ready:    StatusSkipped,
			consumed: StatusPass,
		},
		{
			name:     "malformed observedGeneration suppresses only readiness",
			status:   withStatus(map[string]any{"observedGeneration": "three"}),
			contract: StatusFail,
			field:    "status.observedGeneration",
			ready:    StatusSkipped,
			consumed: StatusPass,
		},
		{
			name:     "negative observedGeneration is malformed",
			status:   withStatus(map[string]any{"observedGeneration": int64(-1)}),
			contract: StatusFail,
			field:    "status.observedGeneration",
			ready:    StatusSkipped,
			consumed: StatusPass,
		},
		{
			name:     "malformed conditions container suppresses only readiness",
			status:   withStatus(map[string]any{"conditions": "yes"}),
			contract: StatusFail,
			field:    "status.conditions",
			ready:    StatusSkipped,
			consumed: StatusPass,
		},
		{
			name: "malformed condition entry suppresses only readiness",
			status: withStatus(map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "Maybe"},
				},
			}),
			contract: StatusFail,
			field:    "status.conditions[0].status",
			ready:    StatusSkipped,
			consumed: StatusPass,
		},
		{
			name: "duplicate condition types suppress only readiness",
			status: withStatus(map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "True"},
					map[string]any{"type": "Ready", "status": "True"},
				},
			}),
			contract: StatusFail,
			ready:    StatusSkipped,
			consumed: StatusPass,
		},
		{
			name: "invalid condition type grammar",
			status: withStatus(map[string]any{
				"conditions": []any{
					map[string]any{"type": "not valid", "status": "True"},
				},
			}),
			contract: StatusFail,
			field:    "status.conditions[0].type",
			ready:    StatusSkipped,
			consumed: StatusPass,
		},
		{
			name: "malformed condition timestamp",
			status: withStatus(map[string]any{
				"conditions": []any{
					map[string]any{
						"type": "Ready", "status": "True",
						"lastTransitionTime": "not-a-timestamp",
					},
				},
			}),
			contract: StatusFail,
			field:    "status.conditions[0].lastTransitionTime",
			ready:    StatusSkipped,
			consumed: StatusPass,
		},
		{
			name: "null and empty condition timestamps are unset",
			status: withStatus(map[string]any{
				"conditions": []any{
					map[string]any{
						"type": "Ready", "status": "True",
						"lastTransitionTime": nil,
						"lastHeartbeatTime":  "",
					},
					map[string]any{"type": "ProvisioningSucceeded", "status": "True"},
				},
			}),
			contract: StatusPass,
			ready:    StatusPass,
			consumed: StatusPass,
		},
		{
			name: "unknown condition metadata fields are allowed",
			status: withStatus(map[string]any{
				"phase":              "Ready",
				"observedGeneration": testGeneration,
				"conditions": []any{
					map[string]any{
						"type": "Ready", "status": "True",
						"observedGeneration": testGeneration,
						"severity":           "Info",
						"unknownExtra":       map[string]any{"nested": true},
					},
					map[string]any{"type": "ProvisioningSucceeded", "status": "True"},
				},
			}),
			contract: StatusPass,
			ready:    StatusPass,
			consumed: StatusPass,
		},
		{
			name:     "malformed releases container suppresses only consumption",
			status:   withStatus(map[string]any{"releases": "none"}),
			contract: StatusFail,
			field:    "status.releases",
			ready:    StatusPass,
			consumed: StatusSkipped,
		},
		{
			name: "malformed release entry suppresses only consumption",
			status: withStatus(map[string]any{
				"releases": []any{
					map[string]any{"name": "platform", "version": int64(1)},
				},
			}),
			contract: StatusFail,
			field:    "status.releases[0].version",
			ready:    StatusPass,
			consumed: StatusSkipped,
		},
		{
			name: "duplicate release names suppress only consumption",
			status: withStatus(map[string]any{
				"releases": []any{
					map[string]any{"name": "platform", "version": testPlatform},
					map[string]any{"name": "platform", "version": testPlatform},
				},
			}),
			contract: StatusFail,
			field:    "status.releases[1].name",
			ready:    StatusPass,
			consumed: StatusSkipped,
		},
		{
			name: "blank release name is malformed",
			status: withStatus(map[string]any{
				"releases": []any{
					map[string]any{"name": " "},
				},
			}),
			contract: StatusFail,
			field:    "status.releases[0].name",
			ready:    StatusPass,
			consumed: StatusSkipped,
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			snapshot := healthySnapshot()
			snapshot.Module.Object = healthyModuleCR()

			if test.status == nil {
				delete(snapshot.Module.Object.Object, "status")
			} else {
				snapshot.Module.Object.Object["status"] = test.status
			}

			result := Evaluate(snapshot)

			contract := check(result, CheckModuleContract)
			g.Expect(contract.Status).To(Equal(test.contract))
			if test.field != "" {
				g.Expect(contract.Field).To(Equal(test.field))
			}
			g.Expect(check(result, CheckModuleReady).Status).To(Equal(test.ready))
			g.Expect(check(result, CheckPlatformConsumed).Status).To(Equal(test.consumed))

			// The invocation cannot pass while the contract fails.
			if test.contract != StatusPass {
				g.Expect(result.Passed()).To(BeFalse())
			}
		})
	}
}

// withStatus merges partial status fields over the healthy status, so a
// test case only names the branch it mutates.
func withStatus(overrides map[string]any) map[string]any {
	status := healthyCRStatus()

	for key, value := range overrides {
		status[key] = value
	}

	return status
}

// TestEvaluateModuleReady covers readiness semantics: generations, the
// required healthy conditions, phase, and their interaction with
// consumption.
func TestEvaluateModuleReady(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   map[string]any
		ready    string
		field    string
		contains string
		consumed string
	}{
		{
			name:     "stale observed generation",
			status:   withStatus(map[string]any{"observedGeneration": testGeneration - 1}),
			ready:    StatusFail,
			field:    "status.observedGeneration",
			consumed: StatusPass,
		},
		{
			name:     "future observed generation",
			status:   withStatus(map[string]any{"observedGeneration": testGeneration + 1}),
			ready:    StatusFail,
			field:    "status.observedGeneration",
			consumed: StatusPass,
		},
		{
			name:     "absent observed generation reads as zero",
			status:   withStatus(map[string]any{"observedGeneration": nil}),
			ready:    StatusFail,
			field:    "status.observedGeneration",
			consumed: StatusPass,
		},
		{
			name:     "wrong phase",
			status:   withStatus(map[string]any{"phase": "Not Ready"}),
			ready:    StatusFail,
			field:    "status.phase",
			consumed: StatusPass,
		},
		{
			name:     "absent phase",
			status:   withStatus(map[string]any{"phase": nil}),
			ready:    StatusFail,
			field:    "status.phase",
			consumed: StatusPass,
		},
		{
			name: "unready condition",
			status: withStatus(map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "False"},
					map[string]any{"type": "ProvisioningSucceeded", "status": "True"},
				},
			}),
			ready:    StatusFail,
			field:    "status.conditions",
			contains: `condition "Ready" is False`,
			consumed: StatusPass,
		},
		{
			name: "unknown condition status is a contract failure, not readiness",
			status: withStatus(map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "Unknown"},
					map[string]any{"type": "ProvisioningSucceeded", "status": "True"},
				},
			}),
			ready:    StatusFail,
			field:    "status.conditions",
			contains: `condition "Ready" is Unknown`,
			consumed: StatusPass,
		},
		{
			name: "missing required condition",
			status: withStatus(map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "True"},
				},
			}),
			ready:    StatusFail,
			field:    "status.conditions",
			contains: `condition "ProvisioningSucceeded" is missing`,
			consumed: StatusPass,
		},
		{
			name:     "empty conditions array is structurally valid",
			status:   withStatus(map[string]any{"conditions": []any{}}),
			ready:    StatusFail,
			field:    "status.conditions",
			consumed: StatusPass,
		},
		{
			name: "stale condition observed generation",
			status: withStatus(map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "True", "observedGeneration": testGeneration - 1},
					map[string]any{"type": "ProvisioningSucceeded", "status": "True"},
				},
			}),
			ready:    StatusFail,
			field:    "status.conditions",
			contains: `condition "Ready" observedGeneration is`,
			consumed: StatusPass,
		},
		{
			name: "omitted condition observed generation is permitted",
			status: withStatus(map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "True"},
					map[string]any{"type": "ProvisioningSucceeded", "status": "True"},
				},
			}),
			ready:    StatusPass,
			consumed: StatusPass,
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			snapshot := healthySnapshot()
			snapshot.Module.Object.Object["status"] = test.status

			result := Evaluate(snapshot)

			ready := check(result, CheckModuleReady)
			g.Expect(ready.Status).To(Equal(test.ready))
			g.Expect(ready.Field).To(Equal(test.field))
			if test.contains != "" {
				g.Expect(ready.Message).To(ContainSubstring(test.contains))
			}

			// Readiness failure never skips the release handshake.
			g.Expect(check(result, CheckPlatformConsumed).Status).To(Equal(test.consumed))
		})
	}
}

// TestEvaluateModuleReadyJSONNumbers exercises the numeric forms JSON
// decoding produces for condition metadata.
func TestEvaluateModuleReadyJSONNumbers(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	snapshot := healthySnapshot()
	snapshot.Module.Object.Object["status"] = map[string]any{
		"phase":              "Ready",
		"observedGeneration": float64(testGeneration),
		"conditions": []any{
			map[string]any{
				"type":               "Ready",
				"status":             "True",
				"observedGeneration": float64(testGeneration),
			},
			map[string]any{
				"type":               "ProvisioningSucceeded",
				"status":             "True",
				"observedGeneration": jsonNumber(testGeneration),
			},
		},
		"releases": []any{
			map[string]any{"name": "platform", "version": testPlatform},
		},
	}

	result := Evaluate(snapshot)

	g.Expect(check(result, CheckModuleContract).Status).To(Equal(StatusPass))
	g.Expect(check(result, CheckModuleReady).Status).To(Equal(StatusPass))
	g.Expect(check(result, CheckPlatformConsumed).Status).To(Equal(StatusPass))
}

// TestEvaluateDeployment covers rollout semantics against the configured
// controller Deployment.
func TestEvaluateDeployment(t *testing.T) {
	t.Parallel()

	deployment := func(mutate func(body map[string]any)) *unstructured.Unstructured {
		object := healthyDeployment()
		if mutate != nil {
			mutate(object.Object)
		}

		return object
	}

	setStatus := func(body map[string]any, status map[string]any) {
		body["status"] = status
	}

	tests := []struct {
		name     string
		object   *unstructured.Unstructured
		status   string
		field    string
		contains string
	}{
		{
			name:   "missing deployment",
			object: nil,
			status: StatusFail,
		},
		{
			name: "terminating deployment",
			object: deployment(func(body map[string]any) {
				body["metadata"].(map[string]any)["deletionTimestamp"] = "2026-01-02T03:04:05Z"
			}),
			status: StatusFail,
		},
		{
			name: "scaled to zero",
			object: deployment(func(body map[string]any) {
				body["spec"].(map[string]any)["replicas"] = int64(0)
				setStatus(body, map[string]any{
					"observedGeneration": int64(2),
					"replicas":           int64(0),
					"updatedReplicas":    int64(0),
					"readyReplicas":      int64(0),
					"availableReplicas":  int64(0),
				})
			}),
			status:   StatusFail,
			field:    "spec.replicas",
			contains: "scaled to zero",
		},
		{
			name: "omitted desired replicas defaults to one",
			object: deployment(func(body map[string]any) {
				delete(body["spec"].(map[string]any), "replicas")
				setStatus(body, map[string]any{
					"observedGeneration": int64(2),
					"replicas":           int64(1),
					"updatedReplicas":    int64(1),
					"readyReplicas":      int64(1),
					"availableReplicas":  int64(1),
				})
			}),
			status: StatusPass,
		},
		{
			name: "surge replicas",
			object: deployment(func(body map[string]any) {
				body["spec"].(map[string]any)["replicas"] = int64(2)
				setStatus(body, map[string]any{
					"observedGeneration": int64(2),
					"replicas":           int64(3),
					"updatedReplicas":    int64(2),
					"readyReplicas":      int64(2),
					"availableReplicas":  int64(2),
				})
			}),
			status:   StatusFail,
			field:    "status.replicas",
			contains: "status.replicas is 3; want 2",
		},
		{
			name: "old replicas during rollout",
			object: deployment(func(body map[string]any) {
				body["spec"].(map[string]any)["replicas"] = int64(2)
				setStatus(body, map[string]any{
					"observedGeneration": int64(2),
					"replicas":           int64(2),
					"updatedReplicas":    int64(1),
					"readyReplicas":      int64(2),
					"availableReplicas":  int64(2),
				})
			}),
			status:   StatusFail,
			field:    "status.updatedReplicas",
			contains: "status.updatedReplicas is 1; want 2",
		},
		{
			name: "unavailable replicas",
			object: deployment(func(body map[string]any) {
				setStatus(body, map[string]any{
					"observedGeneration":  int64(2),
					"replicas":            int64(1),
					"updatedReplicas":     int64(1),
					"readyReplicas":       int64(0),
					"availableReplicas":   int64(0),
					"unavailableReplicas": int64(1),
				})
			}),
			status:   StatusFail,
			field:    "status.readyReplicas",
			contains: "status.readyReplicas is 0; want 1",
		},
		{
			name: "stale observed generation",
			object: deployment(func(body map[string]any) {
				setStatus(body, map[string]any{
					"observedGeneration": int64(1),
					"replicas":           int64(1),
					"updatedReplicas":    int64(1),
					"readyReplicas":      int64(1),
					"availableReplicas":  int64(1),
				})
			}),
			status:   StatusFail,
			field:    "status.observedGeneration",
			contains: "status.observedGeneration is 1; want at least metadata.generation 2",
		},
		{
			name: "missing status counters mean zero",
			object: deployment(func(body map[string]any) {
				setStatus(body, map[string]any{"observedGeneration": int64(2)})
			}),
			status:   StatusFail,
			field:    "status.replicas",
			contains: "status.replicas is 0; want 1",
		},
		{
			name: "progress deadline exceeded",
			object: deployment(func(body map[string]any) {
				setStatus(body, map[string]any{
					"observedGeneration": int64(2),
					"replicas":           int64(1),
					"updatedReplicas":    int64(1),
					"readyReplicas":      int64(1),
					"availableReplicas":  int64(1),
					"conditions": []any{
						map[string]any{
							"type":   "Progressing",
							"status": "False",
							"reason": "ProgressDeadlineExceeded",
						},
					},
				})
			}),
			status:   StatusFail,
			field:    "status.conditions",
			contains: "progress deadline",
		},
		{
			name: "progressing false with another reason is not the deadline",
			object: deployment(func(body map[string]any) {
				setStatus(body, map[string]any{
					"observedGeneration": int64(2),
					"replicas":           int64(1),
					"updatedReplicas":    int64(1),
					"readyReplicas":      int64(1),
					"availableReplicas":  int64(1),
					"conditions": []any{
						map[string]any{
							"type":   "Progressing",
							"status": "False",
							"reason": "ReplicaSetCreateError",
						},
					},
				})
			}),
			status: StatusPass,
		},
		{
			name: "deadline reason on a progressing condition is not the deadline",
			object: deployment(func(body map[string]any) {
				setStatus(body, map[string]any{
					"observedGeneration": int64(2),
					"replicas":           int64(1),
					"updatedReplicas":    int64(1),
					"readyReplicas":      int64(1),
					"availableReplicas":  int64(1),
					"conditions": []any{
						map[string]any{
							"type":   "Progressing",
							"status": "True",
							"reason": "ProgressDeadlineExceeded",
						},
					},
				})
			}),
			status: StatusPass,
		},
		{
			name: "progress deadline not exceeded",
			object: deployment(func(body map[string]any) {
				setStatus(body, map[string]any{
					"observedGeneration": int64(2),
					"replicas":           int64(1),
					"updatedReplicas":    int64(1),
					"readyReplicas":      int64(1),
					"availableReplicas":  int64(1),
					"conditions": []any{
						map[string]any{
							"type":   "Progressing",
							"status": "True",
							"reason": "ReplicaSetUpdated",
						},
					},
				})
			}),
			status: StatusPass,
		},
		{
			name: "malformed replica counters",
			object: deployment(func(body map[string]any) {
				setStatus(body, map[string]any{
					"observedGeneration": int64(2),
					"replicas":           "one",
				})
			}),
			status:   StatusFail,
			field:    "status.replicas",
			contains: "status.replicas must be a nonnegative integer",
		},
		{
			name: "malformed desired replicas",
			object: deployment(func(body map[string]any) {
				body["spec"].(map[string]any)["replicas"] = "two"
			}),
			status:   StatusFail,
			field:    "spec.replicas",
			contains: "spec.replicas must be a nonnegative integer",
		},
		{
			name: "malformed condition container",
			object: deployment(func(body map[string]any) {
				setStatus(body, map[string]any{
					"observedGeneration": int64(2),
					"replicas":           int64(1),
					"updatedReplicas":    int64(1),
					"readyReplicas":      int64(1),
					"availableReplicas":  int64(1),
					"conditions":         "none",
				})
			}),
			status:   StatusFail,
			field:    "status.conditions",
			contains: "status.conditions must be an array",
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			snapshot := healthySnapshot()
			if test.object == nil {
				snapshot.Deployment = ObjectObservation{NotFound: true}
			} else {
				snapshot.Deployment = ObjectObservation{Object: test.object}
			}

			result := Evaluate(snapshot)

			deploymentCheck := check(result, CheckDeployment)
			g.Expect(deploymentCheck.Status).To(Equal(test.status))
			if test.field != "" {
				g.Expect(deploymentCheck.Field).To(Equal(test.field))
			}
			if test.contains != "" {
				g.Expect(deploymentCheck.Message).To(ContainSubstring(test.contains))
			}
		})
	}
}

// TestEvaluateDeploymentReadError covers observation failures, which are
// errors rather than findings, and do not suppress the module checks.
func TestEvaluateDeploymentReadError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	snapshot := healthySnapshot()
	snapshot.Deployment = ObjectObservation{Err: errors.New(testReadFailure)}

	result := Evaluate(snapshot)

	g.Expect(check(result, CheckDeployment).Status).To(Equal(StatusError))
	g.Expect(check(result, CheckModuleReady).Status).To(Equal(StatusPass))
	g.Expect(result.Passed()).To(BeFalse())
}

// TestEvaluatePlatformConfig covers the ConfigMap read and the
// data.platformVersion contract.
func TestEvaluatePlatformConfig(t *testing.T) {
	t.Parallel()

	configMap := func(mutate func(body map[string]any)) *unstructured.Unstructured {
		object := healthyConfigMap()
		if mutate != nil {
			mutate(object.Object)
		}

		return object
	}

	tests := []struct {
		name     string
		object   *unstructured.Unstructured
		status   string
		field    string
		contains string
	}{
		{
			name:   "missing config map",
			object: nil,
			status: StatusFail,
		},
		{
			name: "terminating config map",
			object: configMap(func(body map[string]any) {
				body["metadata"].(map[string]any)["deletionTimestamp"] = "2026-01-02T03:04:05Z"
			}),
			status: StatusFail,
		},
		{
			name: "absent data",
			object: configMap(func(body map[string]any) {
				delete(body, "data")
			}),
			status:   StatusFail,
			field:    "data.platformVersion",
			contains: "must set data.platformVersion",
		},
		{
			name: "blank version",
			object: configMap(func(body map[string]any) {
				body["data"].(map[string]any)["platformVersion"] = "  "
			}),
			status:   StatusFail,
			field:    "data.platformVersion",
			contains: "must set data.platformVersion",
		},
		{
			name: "non-string version",
			object: configMap(func(body map[string]any) {
				body["data"].(map[string]any)["platformVersion"] = int64(1)
			}),
			status:   StatusFail,
			field:    "data.platformVersion",
			contains: "must set data.platformVersion",
		},
		{
			name: "invalid version",
			object: configMap(func(body map[string]any) {
				body["data"].(map[string]any)["platformVersion"] = "not-semver"
			}),
			status:   StatusFail,
			field:    "data.platformVersion",
			contains: "invalid data.platformVersion",
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			snapshot := healthySnapshot()
			if test.object == nil {
				snapshot.ConfigMap = ObjectObservation{NotFound: true}
			} else {
				snapshot.ConfigMap = ObjectObservation{Object: test.object}
			}

			result := Evaluate(snapshot)

			configCheck := check(result, CheckPlatformConfig)
			g.Expect(configCheck.Status).To(Equal(test.status))
			if test.field != "" {
				g.Expect(configCheck.Field).To(Equal(test.field))
			}
			if test.contains != "" {
				g.Expect(configCheck.Message).To(ContainSubstring(test.contains))
			}
		})
	}
}

// TestEvaluatePlatformConsumed covers the platform release handshake:
// tolerant normalization, mismatch, missing or malformed entries, and the
// skip rules when prerequisites were not successfully read.
func TestEvaluatePlatformConsumed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sn       func(snapshot *Snapshot)
		status   string
		contains string
	}{
		{
			name: "tolerant normalization matches the release",
			sn: func(snapshot *Snapshot) {
				snapshot.ConfigMap.Object.Object["data"].(map[string]any)["platformVersion"] = "v1.2.3"
			},
			status: StatusPass,
		},
		{
			name: "prerelease and build components participate",
			sn: func(snapshot *Snapshot) {
				snapshot.ConfigMap.Object.Object["data"].(map[string]any)["platformVersion"] = "1.2.3-rc.1+build.2"
				snapshot.Module.Object.Object["status"].(map[string]any)["releases"] = []any{
					map[string]any{"name": "platform", "version": "1.2.3-rc.1+build.2"},
				}
			},
			status: StatusPass,
		},
		{
			name: "prerelease mismatch",
			sn: func(snapshot *Snapshot) {
				snapshot.Module.Object.Object["status"].(map[string]any)["releases"] = []any{
					map[string]any{"name": "platform", "version": "1.2.3-rc.1"},
				}
				snapshot.ConfigMap.Object.Object["data"].(map[string]any)["platformVersion"] = "1.2.3-rc.2"
			},
			status:   StatusFail,
			contains: "reports platform version",
		},
		{
			name: "version mismatch",
			sn: func(snapshot *Snapshot) {
				snapshot.Module.Object.Object["status"].(map[string]any)["releases"] = []any{
					map[string]any{"name": "platform", "version": "1.2.4"},
				}
			},
			status:   StatusFail,
			contains: "reports platform version",
		},
		{
			name: "omitted releases field means no release reported yet",
			sn: func(snapshot *Snapshot) {
				delete(snapshot.Module.Object.Object["status"].(map[string]any), "releases")
			},
			status:   StatusFail,
			contains: `no release named "platform" is reported`,
		},
		{
			name: "empty releases array means no release reported yet",
			sn: func(snapshot *Snapshot) {
				snapshot.Module.Object.Object["status"].(map[string]any)["releases"] = []any{}
			},
			status:   StatusFail,
			contains: `no release named "platform" is reported`,
		},
		{
			name: "other release entries do not satisfy the handshake",
			sn: func(snapshot *Snapshot) {
				snapshot.Module.Object.Object["status"].(map[string]any)["releases"] = []any{
					map[string]any{"name": "other", "version": testPlatform},
				}
			},
			status:   StatusFail,
			contains: `no release named "platform" is reported`,
		},
		{
			name: "blank platform release version",
			sn: func(snapshot *Snapshot) {
				snapshot.Module.Object.Object["status"].(map[string]any)["releases"] = []any{
					map[string]any{"name": "platform", "version": " "},
				}
			},
			status:   StatusFail,
			contains: `version " " is not a semantic version`,
		},
		{
			name: "unready module with a valid handshake still consumes",
			sn: func(snapshot *Snapshot) {
				snapshot.Module.Object.Object["status"].(map[string]any)["conditions"] = []any{
					map[string]any{"type": "Ready", "status": "False"},
					map[string]any{"type": "ProvisioningSucceeded", "status": "True"},
				}
			},
			status: StatusPass,
		},
		{
			name: "missing module skips consumption",
			sn: func(snapshot *Snapshot) {
				snapshot.Module = ObjectObservation{NotFound: true}
			},
			status:   StatusSkipped,
			contains: "requires the module custom resource",
		},
		{
			name: "invalid config version skips consumption",
			sn: func(snapshot *Snapshot) {
				snapshot.ConfigMap.Object.Object["data"].(map[string]any)["platformVersion"] = "broken"
			},
			status:   StatusSkipped,
			contains: "requires a valid data.platformVersion",
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			snapshot := healthySnapshot()
			test.sn(&snapshot)

			result := Evaluate(snapshot)

			consumed := check(result, CheckPlatformConsumed)
			g.Expect(consumed.Status).To(Equal(test.status))
			if test.contains != "" {
				g.Expect(consumed.Message).To(ContainSubstring(test.contains))
			}
			if test.status == StatusPass {
				g.Expect(consumed.Message).To(ContainSubstring("read-only consistency evidence"))
			}
		})
	}
}

// TestEvaluateUnresolvedNamespace covers the defensive skip when the
// namespace was not resolved before evaluation.
func TestEvaluateUnresolvedNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	snapshot := healthySnapshot()
	snapshot.Namespace = ""

	result := Evaluate(snapshot)

	g.Expect(check(result, CheckDeployment).Status).To(Equal(StatusSkipped))
	g.Expect(check(result, CheckPlatformConfig).Status).To(Equal(StatusSkipped))
	g.Expect(check(result, CheckPlatformConsumed).Status).To(Equal(StatusSkipped))
	g.Expect(check(result, CheckModuleReady).Status).To(Equal(StatusPass))
}

// TestEvaluateErrorPropagation checks that read errors surface as check
// errors while other independent checks still report their observations.
func TestEvaluateErrorPropagation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	snapshot := healthySnapshot()
	snapshot.Module = ObjectObservation{Err: errors.New(testReadFailure)}
	snapshot.ConfigMap = ObjectObservation{Err: errors.New(testReadFailure)}

	result := Evaluate(snapshot)

	g.Expect(check(result, CheckModuleExists).Status).To(Equal(StatusError))
	g.Expect(check(result, CheckPlatformConfig).Status).To(Equal(StatusError))
	g.Expect(check(result, CheckModuleContract).Status).To(Equal(StatusSkipped))
	g.Expect(check(result, CheckDeployment).Status).To(Equal(StatusPass))
	g.Expect(result.Passed()).To(BeFalse())
}

// jsonNumber builds a json.Number for the numeric form streaming
// decoders produce.
func jsonNumber(value int64) json.Number {
	return json.Number(strconv.FormatInt(value, 10))
}
