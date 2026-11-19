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

	moduleapi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// The removed metadata fields keep arbitrary values, and unknown fields
// keep any value shape: they are ignored rather than validated.
const yamlSimulatedConditions = `
- type: MockReady
  status: "False"
  reason: Maintenance
  observedGeneration: -3
  lastTransitionTime: not-a-timestamp
  lastHeartbeatTime: null
  labels:
    team: mock
  tags: [one, 2, true]
  enabled: true
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

// The first entry carries fields the last entry omits: the whole last
// entry wins, so its reason defaults and its severity resets.
const yamlRepeatedTypeTakesTheLastEntry = `
- type: Custom
  status: "True"
  reason: First
  message: first message
  severity: Warning
- type: Custom
  status: "False"
  reason: Second
`

const yamlDuplicateMappingKeysTakeTheLastValue = `
- type: MockReady
  status: "True"
  status: "False"
`

const yamlMergedAndAliasedEntries = `
- &healthy
  type: First
  status: "True"
- <<: *healthy
  type: Second
`

func TestReadAnnotationConditionsDecodesSupportedFieldsAndIgnoresTheRest(t *testing.T) {
	g := NewWithT(t)

	entries, err := readAnnotationConditions(map[string]string{conditionsAnnotation: yamlSimulatedConditions})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(entries).To(Equal([]annotationCondition{
		{Type: "MockReady", Status: metav1.ConditionFalse, Reason: "Maintenance"},
		{Type: "example.org/Custom", Status: metav1.ConditionUnknown, Severity: frameworkapi.ConditionSeverityInfo, Message: "waiting for observation"},
	}))
}

func TestReadAnnotationConditionsAcceptsJSONSequencesAndEmptyControls(t *testing.T) {
	g := NewWithT(t)

	entries, err := readAnnotationConditions(map[string]string{
		conditionsAnnotation: `[{"type":"Custom","status":"True","severity":"Warning","reason":"Configured","message":"configured","observedGeneration":99}]`,
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(entries).To(Equal([]annotationCondition{
		{Type: "Custom", Status: metav1.ConditionTrue, Severity: frameworkapi.ConditionSeverity("Warning"), Reason: "Configured", Message: "configured"},
	}))

	for name, raw := range map[string]string{
		"empty yaml sequence": "[]",
		"empty json sequence": "[  ]",
	} {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			entries, err := readAnnotationConditions(map[string]string{conditionsAnnotation: raw})

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(entries).To(BeEmpty())
		})
	}

	entries, err = readAnnotationConditions(nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(entries).To(BeEmpty())
}

func TestReadAnnotationConditionsFollowsOrdinaryDecoderSemantics(t *testing.T) {
	for name, scenario := range map[string]struct {
		raw  string
		want []annotationCondition
	}{
		"repeated type keeps both entries": {
			raw: yamlRepeatedTypeTakesTheLastEntry,
			want: []annotationCondition{
				{Type: "Custom", Status: metav1.ConditionTrue, Reason: "First", Message: "first message", Severity: frameworkapi.ConditionSeverity("Warning")},
				{Type: "Custom", Status: metav1.ConditionFalse, Reason: "Second"},
			},
		},
		"earlier entry with ignored invalid metadata": {
			raw: `
- type: Custom
  status: "True"
  lastTransitionTime: garbage
  observedGeneration: -1
- type: Custom
  status: "False"
`,
			want: []annotationCondition{
				{Type: "Custom", Status: metav1.ConditionTrue},
				{Type: "Custom", Status: metav1.ConditionFalse},
			},
		},
		"duplicate mapping keys take the last value": {
			raw:  yamlDuplicateMappingKeysTakeTheLastValue,
			want: []annotationCondition{{Type: "MockReady", Status: metav1.ConditionFalse}},
		},
		"merged and aliased entries": {
			raw: yamlMergedAndAliasedEntries,
			want: []annotationCondition{
				{Type: "First", Status: metav1.ConditionTrue},
				{Type: "Second", Status: metav1.ConditionTrue},
			},
		},
		"duplicate json keys take the last value": {
			raw:  `[{"type":"Custom","status":"True","status":"False"}]`,
			want: []annotationCondition{{Type: "Custom", Status: metav1.ConditionFalse}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			entries, err := readAnnotationConditions(map[string]string{conditionsAnnotation: scenario.raw})

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(entries).To(Equal(scenario.want))
		})
	}
}

func TestReadAnnotationConditionsRejectsUnusableSequences(t *testing.T) {
	for name, raw := range map[string]string{
		"blank":                 " ",
		"comment only":          "# no list",
		"null":                  "null",
		"mapping":               "{}",
		"scalar":                "42",
		"nonmapping entry":      "[42]",
		"null entry":            "[null]",
		"empty entry":           "[{}]",
		"malformed":             "[{",
		"multiple documents":    yamlMultipleDocuments,
		"field spelling":        `[{"Type":"Custom","status":"True"}]`,
		"missing type":          `[{"status":"True"}]`,
		"invalid type":          `[{"type":"not a name","status":"True"}]`,
		"invalid earlier entry": `[{"type":"Custom","status":"invalid"},{"type":"Custom","status":"True"}]`,
		"missing status":        `[{"type":"Custom"}]`,
		"wrong status type":     `[{"type":"Custom","status":true}]`,
		"yaml implicit boolean": "- type: Custom\n  status: yes",
		"invalid status":        `[{"type":"Custom","status":"true"}]`,
		"reserved ready":        `[{"type":"Ready","status":"True"}]`,
		"reserved provisioning": `[{"type":"ProvisioningSucceeded","status":"True"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			entries, err := readAnnotationConditions(map[string]string{conditionsAnnotation: raw})

			g.Expect(err).To(MatchError(moduleerrors.ErrInvalidMockAnnotation))
			g.Expect(entries).To(BeNil())
		})
	}
}

func TestCurrentConditionsApplyDefaultsOverridesAndLastEntry(t *testing.T) {
	g := NewWithT(t)

	current := currentConditions(nil, 7)

	g.Expect(current).To(HaveLen(2))
	g.Expect(current["MockReady"]).To(Equal(frameworkapi.Condition{
		Type: "MockReady", Status: metav1.ConditionTrue, ObservedGeneration: 7,
		Reason: "MockReadyTrue", Message: "MockReadyTrue",
	}))
	g.Expect(current["Degraded"]).To(Equal(frameworkapi.Condition{
		Type: "Degraded", Status: metav1.ConditionFalse, ObservedGeneration: 7,
		Reason: "DegradedFalse", Message: "DegradedFalse",
	}))

	injected := []annotationCondition{
		{Type: "MockReady", Status: metav1.ConditionFalse, Reason: "Maintenance"},
		{Type: "Custom", Status: metav1.ConditionUnknown, Severity: frameworkapi.ConditionSeverity("Warning"), Message: "superseded"},
		{Type: "Custom", Status: metav1.ConditionTrue, Reason: "Second"},
	}
	current = currentConditions(injected, 9)

	g.Expect(current).To(HaveLen(3))
	g.Expect(current["MockReady"]).To(Equal(frameworkapi.Condition{
		Type: "MockReady", Status: metav1.ConditionFalse, ObservedGeneration: 9,
		Reason: "Maintenance", Message: "Maintenance",
	}))
	// The whole last entry wins: the superseded message and severity are
	// not merged into it.
	g.Expect(current["Custom"]).To(Equal(frameworkapi.Condition{
		Type: "Custom", Status: metav1.ConditionTrue, ObservedGeneration: 9,
		Reason: "Second", Message: "Second",
	}))
}

func TestReadyAggregationUsesFrameworkPolarityAndSeverity(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		injected []annotationCondition
		want     metav1.ConditionStatus
	}{
		{name: "defaults are ready", want: metav1.ConditionTrue},
		{name: "mock readiness override", injected: []annotationCondition{{Type: "MockReady", Status: metav1.ConditionFalse}}, want: metav1.ConditionFalse},
		{name: "degraded is negative", injected: []annotationCondition{{Type: "Degraded", Status: metav1.ConditionTrue}}, want: metav1.ConditionFalse},
		{name: "degraded info is excluded", injected: []annotationCondition{{Type: "Degraded", Status: metav1.ConditionTrue, Severity: frameworkapi.ConditionSeverityInfo}}, want: metav1.ConditionTrue},
		{name: "custom unknown gates readiness", injected: []annotationCondition{{Type: "Custom", Status: metav1.ConditionUnknown}}, want: metav1.ConditionUnknown},
		{name: "custom false gates readiness", injected: []annotationCondition{{Type: "Custom", Status: metav1.ConditionFalse}}, want: metav1.ConditionFalse},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			g := NewWithT(t)
			obj := new(moduleapi.ModuleObject)
			current := currentConditions(scenario.injected, 0)
			aggregator, err := fwconditions.NewAggregator(frameworkapi.ConditionTypeReady, readyDependents(current)...)
			g.Expect(err).NotTo(HaveOccurred())
			manager := fwconditions.NewManager(obj, aggregator)
			manager.Reset()
			for _, condition := range current {
				manager.SetCondition(condition)
			}
			manager.MarkTrue(frameworkapi.ConditionTypeProvisioningSucceeded)
			manager.CleanupStaleConditions()
			manager.RecomputeHappiness()

			g.Expect(manager.GetTopLevelCondition()).To(gstruct.PointTo(HaveField("Status", scenario.want)))
		})
	}
}

func TestStampAutomaticMetadataComparesPersistedStatusValues(t *testing.T) {
	g := NewWithT(t)
	observedAt := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	next := observedAt.Add(time.Hour)
	obj := new(moduleapi.ModuleObject)
	persisted := []frameworkapi.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: 3, LastTransitionTime: metav1.NewTime(observedAt)},
		{Type: "MockReady", Status: metav1.ConditionTrue, ObservedGeneration: 3, LastTransitionTime: metav1.NewTime(observedAt), Reason: "OldReason"},
		{Type: "Degraded", Status: metav1.ConditionTrue, ObservedGeneration: 3, LastTransitionTime: metav1.NewTime(observedAt)},
		{Type: "Obsolete", Status: metav1.ConditionTrue, ObservedGeneration: 3, LastTransitionTime: metav1.NewTime(observedAt)},
	}
	obj.SetConditions(persisted)

	// Edited reasons, a new dependency, and unchanged status values.
	current := currentConditions([]annotationCondition{
		{Type: "MockReady", Status: metav1.ConditionTrue, Reason: "NewReason"},
		{Type: "Informational", Status: metav1.ConditionUnknown, Severity: frameworkapi.ConditionSeverityInfo},
	}, 7)
	aggregator, err := fwconditions.NewAggregator(frameworkapi.ConditionTypeReady, readyDependents(current)...)
	g.Expect(err).NotTo(HaveOccurred())
	manager := fwconditions.NewManager(obj, aggregator)
	manager.Reset()
	for _, condition := range current {
		manager.SetCondition(condition)
	}
	manager.MarkTrue(frameworkapi.ConditionTypeProvisioningSucceeded)
	manager.CleanupStaleConditions()
	manager.RecomputeHappiness()

	stampAutomaticMetadata(obj.GetStatus(), persisted, 7, next)

	g.Expect(obj.GetStatus().ObservedGeneration).To(Equal(int64(7)))
	g.Expect(manager.GetTopLevelCondition()).To(gstruct.PointTo(SatisfyAll(
		HaveField("Status", metav1.ConditionTrue),
		HaveField("ObservedGeneration", int64(7)),
		HaveField("LastTransitionTime", metav1.NewTime(observedAt)),
	)))
	g.Expect(manager.GetCondition(conditionTypeMockReady)).To(gstruct.PointTo(SatisfyAll(
		HaveField("ObservedGeneration", int64(7)),
		HaveField("Reason", "NewReason"),
		HaveField("LastTransitionTime", metav1.NewTime(observedAt)),
	)))
	g.Expect(manager.GetCondition(frameworkapi.ConditionTypeDegraded)).To(gstruct.PointTo(SatisfyAll(
		HaveField("Status", metav1.ConditionFalse),
		HaveField("ObservedGeneration", int64(7)),
		HaveField("LastTransitionTime", metav1.NewTime(next)),
	)))
	g.Expect(manager.GetCondition(frameworkapi.ConditionTypeProvisioningSucceeded)).To(gstruct.PointTo(SatisfyAll(
		HaveField("ObservedGeneration", int64(7)),
		HaveField("LastTransitionTime", metav1.NewTime(next)),
	)))
	g.Expect(manager.GetCondition("Informational")).To(gstruct.PointTo(SatisfyAll(
		HaveField("ObservedGeneration", int64(7)),
		HaveField("LastTransitionTime", metav1.NewTime(next)),
	)))
	g.Expect(manager.GetCondition("Obsolete")).To(BeNil())

	// A later pass with unchanged final status values keeps the transition
	// times even though the clock moved past a whole-second boundary.
	later := next.Add(time.Hour)
	stampAutomaticMetadata(obj.GetStatus(), obj.GetStatus().DeepCopy().Conditions, 7, later)
	g.Expect(manager.GetCondition(frameworkapi.ConditionTypeDegraded)).To(gstruct.PointTo(
		HaveField("LastTransitionTime", metav1.NewTime(next)),
	))
	g.Expect(manager.GetTopLevelCondition()).To(gstruct.PointTo(
		HaveField("LastTransitionTime", metav1.NewTime(observedAt)),
	))
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
	obj, ok := rawObject.(*moduleapi.ModuleObject)
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
	g.Expect(list).To(BeAssignableToTypeOf(new(moduleapi.ModuleObjectList)))
}
