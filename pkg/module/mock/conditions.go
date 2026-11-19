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
	"cmp"
	"time"

	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	fwconditions "github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const conditionsAnnotation = "mock.platform.opendatahub.io/conditions"
const conditionTypeMockReady frameworkapi.ConditionType = "MockReady"

// annotationCondition holds the five annotation fields that shape the
// simulated status. The controller owns generations and transition
// timestamps, so every other annotation field is ignored.
type annotationCondition struct {
	Type     string                         `json:"type"`
	Status   metav1.ConditionStatus         `json:"status"`
	Reason   string                         `json:"reason"`
	Message  string                         `json:"message"`
	Severity frameworkapi.ConditionSeverity `json:"severity"`
}

// readAnnotationConditions returns the conditions configured by the
// annotation. A missing annotation or an empty sequence means none.
func readAnnotationConditions(annotations map[string]string) ([]annotationCondition, error) {
	raw, present := annotations[conditionsAnnotation]
	if !present {
		return nil, nil
	}

	return decodeAnnotationConditions(raw)
}

// currentConditions assembles the condition set for one reconcile: the
// healthy MockReady and Degraded defaults, then the annotation entries
// with the last entry per type winning. A missing reason defaults to
// Type+Status and a missing message to the resolved reason.
func currentConditions(injected []annotationCondition, generation int64) map[string]frameworkapi.Condition {
	mockReady, degraded := string(conditionTypeMockReady), string(frameworkapi.ConditionTypeDegraded)
	current := map[string]frameworkapi.Condition{
		mockReady: {Type: mockReady, Status: metav1.ConditionTrue},
		degraded:  {Type: degraded, Status: metav1.ConditionFalse},
	}

	for _, entry := range injected {
		current[entry.Type] = frameworkapi.Condition{
			Type:     entry.Type,
			Status:   entry.Status,
			Reason:   entry.Reason,
			Message:  entry.Message,
			Severity: entry.Severity,
		}
	}

	for typ, condition := range current {
		condition.ObservedGeneration = generation
		condition.Reason = cmp.Or(condition.Reason, condition.Type+string(condition.Status))
		condition.Message = cmp.Or(condition.Message, condition.Reason)
		current[typ] = condition
	}

	return current
}

// readyDependents defines what the framework aggregates into Ready: the
// framework-managed ProvisioningSucceeded plus every current condition.
// Degraded counts against readiness when True; all other conditions count
// for it when True.
func readyDependents(current map[string]frameworkapi.Condition) []fwconditions.DependentDefinition {
	dependents := make([]fwconditions.DependentDefinition, 0, len(current)+1)
	dependents = append(dependents, fwconditions.Dependent(frameworkapi.ConditionTypeProvisioningSucceeded, fwconditions.HealthyWhenTrue))
	for name := range current {
		polarity := fwconditions.HealthyWhenTrue
		if name == string(frameworkapi.ConditionTypeDegraded) {
			polarity = fwconditions.HealthyWhenFalse
		}
		dependents = append(dependents, fwconditions.Dependent(frameworkapi.ConditionType(name), polarity))
	}

	return dependents
}

// stampAutomaticMetadata runs after the framework aggregated Ready and
// cleaned up stale conditions. The controller owns generations and
// transition timestamps: every condition reports the processed
// generation, and a condition keeps its persisted transition time while
// its status value is unchanged, even when its reason, message, or
// dependency membership changed.
func stampAutomaticMetadata(
	status *frameworkapi.Status,
	persisted []frameworkapi.Condition,
	generation int64,
	now time.Time,
) {
	previous := make(map[string]frameworkapi.Condition, len(persisted))
	for _, condition := range persisted {
		previous[condition.Type] = condition
	}

	status.ObservedGeneration = generation
	for i := range status.Conditions {
		condition := &status.Conditions[i]
		condition.ObservedGeneration = generation

		prior := previous[condition.Type]
		if prior.Status == condition.Status && !prior.LastTransitionTime.IsZero() {
			condition.LastTransitionTime = prior.LastTransitionTime
		} else {
			condition.LastTransitionTime = metav1.NewTime(now.UTC().Truncate(time.Second))
		}
	}
}
