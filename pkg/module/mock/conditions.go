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

func desiredConditions(injected []frameworkapi.Condition, generation int64) []frameworkapi.Condition {
	desired := []frameworkapi.Condition{
		{Type: string(conditionTypeMockReady), Status: metav1.ConditionTrue, ObservedGeneration: generation},
		{Type: string(frameworkapi.ConditionTypeDegraded), Status: metav1.ConditionFalse, ObservedGeneration: generation},
	}

	for _, entry := range injected {
		switch frameworkapi.ConditionType(entry.Type) {
		case conditionTypeMockReady:
			desired[0] = entry
		case frameworkapi.ConditionTypeDegraded:
			desired[1] = entry
		default:
			desired = append(desired, entry)
		}
	}

	for i := range desired {
		condition := &desired[i]
		condition.Reason = cmp.Or(condition.Reason, condition.Type+string(condition.Status))
		condition.Message = cmp.Or(condition.Message, condition.Reason)
	}

	return desired
}

func conditionDependents(desired []frameworkapi.Condition) []fwconditions.DependentDefinition {
	dependents := make([]fwconditions.DependentDefinition, 0, len(desired)+1)
	dependents = append(dependents, fwconditions.Dependent(frameworkapi.ConditionTypeProvisioningSucceeded, fwconditions.HealthyWhenTrue))
	for _, entry := range desired {
		conditionType := frameworkapi.ConditionType(entry.Type)
		polarity := fwconditions.HealthyWhenTrue
		if conditionType == frameworkapi.ConditionTypeDegraded {
			polarity = fwconditions.HealthyWhenFalse
		}

		dependents = append(dependents, fwconditions.Dependent(conditionType, polarity))
	}

	return dependents
}

// normalizeConditionMetadata runs after aggregation so transient Ready states
// during the action cannot reset the persisted transition timestamp.
func normalizeConditionMetadata(
	status *frameworkapi.Status,
	previous []frameworkapi.Condition,
	desired []frameworkapi.Condition,
	generation int64,
	now time.Time,
) {
	priorByType := make(map[string]frameworkapi.Condition, len(previous))
	for _, condition := range previous {
		priorByType[condition.Type] = condition
	}

	desiredByType := make(map[string]frameworkapi.Condition, len(desired))
	for _, entry := range desired {
		desiredByType[entry.Type] = entry
	}

	status.ObservedGeneration = generation
	for i := range status.Conditions {
		condition := &status.Conditions[i]
		configured, hasConfigured := desiredByType[condition.Type]
		prior := priorByType[condition.Type]

		condition.ObservedGeneration = generation
		if hasConfigured {
			condition.ObservedGeneration = configured.ObservedGeneration
		}

		switch {
		case !configured.LastTransitionTime.IsZero():
			condition.LastTransitionTime = conditionTime(configured.LastTransitionTime.Time)
		case prior.Status == condition.Status && !prior.LastTransitionTime.IsZero():
			condition.LastTransitionTime = prior.LastTransitionTime
		default:
			condition.LastTransitionTime = conditionTime(now)
		}
	}
}

func conditionTime(value time.Time) metav1.Time {
	return metav1.NewTime(value.UTC().Truncate(time.Second))
}

func readConditions(annotations map[string]string, generation int64) ([]frameworkapi.Condition, error) {
	raw, present := annotations[conditionsAnnotation]
	if !present {
		return nil, nil
	}

	return decodeConditions(raw, generation)
}
