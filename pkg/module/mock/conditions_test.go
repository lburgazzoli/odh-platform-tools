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

package mock

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"

	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	fwconditions "github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

const yamlConditionControls = `
- type: MockReady
  status: "False"
  reason: Maintenance
  observedGeneration: 0
  lastTransitionTime: 2026-11-18T10:00:00.123Z
- type: example.org/Custom
  status: "Unknown"
  severity: Info
  message: waiting for observation
`

const yamlMultipleDocuments = `
- type: MockReady
  status: "True"
---
[]
`

const yamlDuplicateMappingKeys = `
- type: MockReady
  status: "True"
  status: "False"
`

const yamlDuplicateMergeKeys = `
- <<: {type: MockReady}
  <<: {status: "False"}
`

const yamlFloatingPointGeneration = `
- type: MockReady
  status: "True"
  observedGeneration: 1.0
`

func TestReadConditionsDecodesYAMLAndPreservesExplicitFields(t *testing.T) {
	g := NewWithT(t)

	entries, err := readConditions(map[string]string{conditionsAnnotation: yamlConditionControls}, 7)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(entries).To(HaveLen(2))
	g.Expect(entries[0].ObservedGeneration).To(BeZero())
	g.Expect(entries[0].LastTransitionTime.UTC()).To(Equal(time.Date(2026, 11, 18, 10, 0, 0, 123000000, time.UTC)))
	g.Expect(entries[1].ObservedGeneration).To(Equal(int64(7)))
	g.Expect(entries[1].LastTransitionTime).To(BeZero())

	desired := desiredConditions(entries, 7)
	g.Expect(desired).To(HaveKeyWithValue("MockReady", HaveField("Message", "Maintenance")))
	g.Expect(desired).To(HaveKeyWithValue("example.org/Custom", SatisfyAll(
		HaveField("Status", metav1.ConditionUnknown),
		HaveField("Reason", "example.org/CustomUnknown"),
		HaveField("Message", "waiting for observation"),
		HaveField("Severity", frameworkapi.ConditionSeverityInfo),
	)))
}

func TestReadConditionsDecodesJSONAndEmptyControls(t *testing.T) {
	g := NewWithT(t)

	entries, err := readConditions(map[string]string{
		conditionsAnnotation: `[{"type":"Custom","status":"True","severity":"Warning"}]`,
	}, 12)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(entries).To(ConsistOf(SatisfyAll(
		HaveField("Type", "Custom"),
		HaveField("Status", metav1.ConditionTrue),
		HaveField("Severity", frameworkapi.ConditionSeverity("Warning")),
		HaveField("ObservedGeneration", int64(12)),
	)))

	entries, err = readConditions(map[string]string{conditionsAnnotation: "[]"}, 7)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(entries).To(BeEmpty())

	entries, err = readConditions(nil, 7)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(entries).To(BeEmpty())
}

func TestReadConditionsRejectsTheWholeInvalidSequence(t *testing.T) {
	for name, raw := range map[string]string{
		"blank":                    " ",
		"comment only":             "# no list",
		"null":                     "null",
		"mapping":                  "{}",
		"nonmapping entry":         "[42]",
		"null entry":               "[null]",
		"malformed":                "[{",
		"multiple documents":       yamlMultipleDocuments,
		"unknown field":            `[{"type":"Custom","status":"True","other":"value"}]`,
		"legacy framework field":   `[{"type":"Custom","status":"True","lastHeartbeatTime":"2026-11-18T10:00:00Z"}]`,
		"field spelling":           `[{"Type":"Custom","status":"True"}]`,
		"duplicate JSON key":       `[{"type":"Custom","status":"True","status":"False"}]`,
		"duplicate YAML key":       yamlDuplicateMappingKeys,
		"duplicate YAML merge":     yamlDuplicateMergeKeys,
		"duplicate type":           `[{"type":"Custom","status":"True"},{"type":"Custom","status":"False"}]`,
		"missing type":             `[{"status":"True"}]`,
		"invalid type":             `[{"type":"not a name","status":"True"}]`,
		"missing status":           `[{"type":"Custom"}]`,
		"wrong status type":        `[{"type":"Custom","status":true}]`,
		"invalid status":           `[{"type":"Custom","status":"true"}]`,
		"reserved ready":           `[{"type":"Ready","status":"True"}]`,
		"reserved provisioning":    `[{"type":"ProvisioningSucceeded","status":"True"}]`,
		"negative generation":      `[{"type":"Custom","status":"True","observedGeneration":-1}]`,
		"floating YAML generation": yamlFloatingPointGeneration,
		"null generation":          `[{"type":"Custom","status":"True","observedGeneration":null}]`,
		"bad timestamp":            `[{"type":"Custom","status":"True","lastTransitionTime":"today"}]`,
		"one-digit hour":           `[{"type":"Custom","status":"True","lastTransitionTime":"2026-11-18T1:00:00Z"}]`,
		"comma fraction":           `[{"type":"Custom","status":"True","lastTransitionTime":"2026-11-18T10:00:00,1Z"}]`,
		"invalid zone offset":      `[{"type":"Custom","status":"True","lastTransitionTime":"2026-11-18T10:00:00+24:00"}]`,
		"zero timestamp":           `[{"type":"Custom","status":"True","lastTransitionTime":"0001-01-01T00:00:00Z"}]`,
		"UTC year overflow":        `[{"type":"Custom","status":"True","lastTransitionTime":"9999-12-31T23:59:59-01:00"}]`,
		"null timestamp":           `[{"type":"Custom","status":"True","lastTransitionTime":null}]`,
	} {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			entries, err := readConditions(map[string]string{conditionsAnnotation: raw}, 7)

			g.Expect(err).To(MatchError(moduleerrors.ErrInvalidMockAnnotation))
			g.Expect(entries).To(BeNil())
		})
	}
}

func TestConditionsUseFrameworkReadinessPolarityAndSeverity(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		injected []frameworkapi.Condition
		want     metav1.ConditionStatus
	}{
		{name: "defaults are ready", want: metav1.ConditionTrue},
		{name: "mock readiness override", injected: []frameworkapi.Condition{{Type: "MockReady", Status: metav1.ConditionFalse}}, want: metav1.ConditionFalse},
		{name: "degraded is negative", injected: []frameworkapi.Condition{{Type: "Degraded", Status: metav1.ConditionTrue}}, want: metav1.ConditionFalse},
		{name: "degraded Info is excluded", injected: []frameworkapi.Condition{{Type: "Degraded", Status: metav1.ConditionTrue, Severity: frameworkapi.ConditionSeverityInfo}}, want: metav1.ConditionTrue},
		{name: "custom unknown gates readiness", injected: []frameworkapi.Condition{{Type: "Custom", Status: metav1.ConditionUnknown}}, want: metav1.ConditionUnknown},
		{name: "custom false gates readiness", injected: []frameworkapi.Condition{{Type: "Custom", Status: metav1.ConditionFalse}}, want: metav1.ConditionFalse},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			g := NewWithT(t)
			obj := new(moduleObject)
			desired := desiredConditions(scenario.injected, 0)
			aggregator, err := fwconditions.NewAggregator(frameworkapi.ConditionTypeReady, conditionDependents(desired)...)
			g.Expect(err).NotTo(HaveOccurred())
			manager := fwconditions.NewManager(obj, aggregator)
			manager.Reset()
			for _, entry := range desired {
				manager.SetCondition(entry)
			}
			manager.MarkTrue(frameworkapi.ConditionTypeProvisioningSucceeded)
			manager.CleanupStaleConditions()
			manager.RecomputeHappiness()

			g.Expect(manager.GetTopLevelCondition()).To(gstruct.PointTo(HaveField("Status", scenario.want)))
		})
	}
}

func TestNormalizeConditionMetadataUsesPersistedFinalStatuses(t *testing.T) {
	g := NewWithT(t)
	observedAt := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	next := observedAt.Add(time.Hour)
	supplied := next.Add(50*time.Second + 123*time.Millisecond)
	obj := new(moduleObject)
	previous := []frameworkapi.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(observedAt)},
		{Type: "MockReady", Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(observedAt)},
		{Type: "Degraded", Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(observedAt)},
		{Type: "Obsolete", Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(observedAt)},
	}
	obj.SetConditions(previous)
	desired := desiredConditions([]frameworkapi.Condition{
		{Type: "MockReady", Status: metav1.ConditionTrue, ObservedGeneration: 0, LastTransitionTime: metav1.NewTime(supplied)},
		{Type: "Informational", Status: metav1.ConditionUnknown, Severity: frameworkapi.ConditionSeverityInfo, ObservedGeneration: 7},
	}, 7)
	aggregator, err := fwconditions.NewAggregator(frameworkapi.ConditionTypeReady, conditionDependents(desired)...)
	g.Expect(err).NotTo(HaveOccurred())
	manager := fwconditions.NewManager(obj, aggregator)
	manager.Reset()
	for _, entry := range desired {
		manager.SetCondition(entry)
	}
	manager.MarkTrue(frameworkapi.ConditionTypeProvisioningSucceeded)
	manager.CleanupStaleConditions()
	manager.RecomputeHappiness()

	normalizeConditionMetadata(obj.GetStatus(), previous, desired, 7, next)

	g.Expect(obj.GetStatus().ObservedGeneration).To(Equal(int64(7)))
	g.Expect(manager.GetTopLevelCondition()).To(gstruct.PointTo(SatisfyAll(
		HaveField("Status", metav1.ConditionTrue),
		HaveField("ObservedGeneration", int64(7)),
		HaveField("LastTransitionTime", metav1.NewTime(observedAt)),
	)))
	g.Expect(manager.GetCondition(conditionTypeMockReady)).To(gstruct.PointTo(SatisfyAll(
		HaveField("ObservedGeneration", int64(0)),
		HaveField("LastTransitionTime", metav1.NewTime(supplied.Truncate(time.Second))),
	)))
	g.Expect(manager.GetCondition(frameworkapi.ConditionTypeDegraded)).To(gstruct.PointTo(SatisfyAll(
		HaveField("Status", metav1.ConditionFalse),
		HaveField("LastTransitionTime", metav1.NewTime(next)),
	)))
	g.Expect(manager.GetCondition("Obsolete")).To(BeNil())
}

func TestModuleSchemeIsolatesRuntimeKindsAndExposesStatusToFramework(t *testing.T) {
	g := NewWithT(t)
	first := schema.GroupVersionKind{Group: "one.test", Version: "v1alpha1", Kind: "FirstModule"}
	second := schema.GroupVersionKind{Group: "two.test", Version: "v1beta1", Kind: "SecondModule"}
	firstScheme := newModuleScheme(first)
	secondScheme := newModuleScheme(second)
	g.Expect(firstScheme.Recognizes(second)).To(BeFalse())
	g.Expect(secondScheme.Recognizes(first)).To(BeFalse())

	rawObject, err := secondScheme.New(second)
	g.Expect(err).NotTo(HaveOccurred())
	obj, ok := rawObject.(*moduleObject)
	g.Expect(ok).To(BeTrue())
	obj.Name = "default-second-module"
	release := frameworkapi.ComponentReleaseStatus{
		Releases: []frameworkapi.ComponentRelease{{Name: frameworkapi.ReleasePlatform, Version: "3.0.0"}},
	}
	obj.SetReleaseStatus(release)
	release.Releases[0].Version = "4.0.0"

	rendered, err := fwresources.ObjectToUnstructured(secondScheme, obj)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(rendered.GroupVersionKind()).To(Equal(second))
	g.Expect(rendered.Object["status"]).To(HaveKeyWithValue("releases", ConsistOf(
		HaveKeyWithValue("version", "3.0.0"),
	)))

	list, err := secondScheme.New(second.GroupVersion().WithKind(second.Kind + "List"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(list).To(BeAssignableToTypeOf(new(moduleObjectList)))
}
