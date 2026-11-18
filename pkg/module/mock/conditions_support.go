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
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"k8s.io/apimachinery/pkg/api/validate/content"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	yamlstream "sigs.k8s.io/kustomize/kyaml/yaml"
	"sigs.k8s.io/yaml"
)

var conditionTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]+)?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)

func decodeConditions(raw string, generation int64) ([]frameworkapi.Condition, error) {
	document, err := observationJSON(raw)
	if err != nil {
		return nil, err
	}
	if document.Kind() != '[' {
		return nil, moduleerrors.New(moduleerrors.ErrInvalidMockAnnotation,
			conditionsAnnotation+" must contain a sequence of condition mappings")
	}

	var fields []map[string]jsontext.Value
	if err := json.Unmarshal(document, &fields); err != nil {
		return nil, moduleerrors.New(moduleerrors.ErrInvalidMockAnnotation,
			conditionsAnnotation+" must contain condition mappings with unique keys", moduleerrors.WithCause(err))
	}

	var conditions []frameworkapi.Condition
	if err := json.Unmarshal(document, &conditions, json.RejectUnknownMembers(true)); err != nil {
		return nil, moduleerrors.New(moduleerrors.ErrInvalidMockAnnotation,
			conditionsAnnotation+" contains an unknown field or a value of the wrong type", moduleerrors.WithCause(err))
	}

	seen := make(map[string]struct{}, len(conditions))
	for index, condition := range conditions {
		entry := fields[index]
		if err := validateObservation(index, condition, entry, seen); err != nil {
			return nil, err
		}

		// A plain framework Condition carries the resolved generation.
		if _, supplied := entry["observedGeneration"]; !supplied {
			conditions[index].ObservedGeneration = generation
		}

		seen[condition.Type] = struct{}{}
	}

	return conditions, nil
}

func observationJSON(raw string) (jsontext.Value, error) {
	document := jsontext.Value(raw)
	if document.IsValid(jsontext.AllowDuplicateNames(true)) {
		return document, nil
	}

	decoder := yamlstream.NewDecoder(strings.NewReader(raw))
	var parsed []map[string]any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, invalidConditionsDocument(err)
	}
	var extra yamlstream.Node
	switch err := decoder.Decode(&extra); {
	case err == nil:
		return nil, invalidConditionsDocument(errors.New("multiple YAML documents"))
	case !errors.Is(err, io.EOF):
		return nil, invalidConditionsDocument(err)
	}

	// The stream decoder detects repeated merge keys. Check the decoded
	// number type before YAML-to-JSON conversion can round floats to integers.
	for index, entry := range parsed {
		switch entry["observedGeneration"].(type) {
		case nil, int, int64, uint64:
		default:
			return nil, invalidObservation(index, "observedGeneration must be an integer")
		}
	}

	document, err := yaml.YAMLToJSONStrict([]byte(raw))
	if err != nil {
		return nil, invalidConditionsDocument(err)
	}

	return document, nil
}

func validateObservation(
	index int,
	entry frameworkapi.Condition,
	fields map[string]jsontext.Value,
	seen map[string]struct{},
) error {
	_, duplicate := seen[entry.Type]
	_, legacyField := fields["lastHeartbeatTime"]
	timestamp, transitionTimeSupplied := fields["lastTransitionTime"]
	switch {
	case legacyField:
		return invalidObservation(index, "lastHeartbeatTime is not a supported field")
	case len(content.IsLabelKey(entry.Type)) != 0:
		return invalidObservation(index, "type must be a valid Kubernetes condition name")
	case entry.Type == string(frameworkapi.ConditionTypeReady) || entry.Type == string(frameworkapi.ConditionTypeProvisioningSucceeded):
		return invalidObservation(index, "Ready and ProvisioningSucceeded are reserved")
	case duplicate:
		return invalidObservation(index, "condition type is repeated")
	case entry.Status != metav1.ConditionTrue && entry.Status != metav1.ConditionFalse && entry.Status != metav1.ConditionUnknown:
		return invalidObservation(index, "status must be True, False, or Unknown")
	case entry.ObservedGeneration < 0:
		return invalidObservation(index, "observedGeneration must be nonnegative")
	case transitionTimeSupplied && !validConditionTime(timestamp, entry.LastTransitionTime.Time):
		return invalidObservation(index, "lastTransitionTime must be a nonzero RFC3339 timestamp")
	}

	for _, value := range fields {
		if value.Kind() == 'n' {
			return invalidObservation(index, "fields must have non-null values")
		}
	}

	return nil
}

func validConditionTime(raw jsontext.Value, value time.Time) bool {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil || !conditionTimestamp.MatchString(text) {
		return false
	}

	normalized := conditionTime(value)
	return !normalized.IsZero() && normalized.Year() >= 0 && normalized.Year() <= 9999
}

func invalidConditionsDocument(cause error) error {
	return moduleerrors.New(moduleerrors.ErrInvalidMockAnnotation,
		conditionsAnnotation+" must contain one YAML or JSON sequence with unique keys", moduleerrors.WithCause(cause))
}

func invalidObservation(index int, problem string) error {
	return moduleerrors.New(moduleerrors.ErrInvalidMockAnnotation,
		fmt.Sprintf("%s condition %d: %s", conditionsAnnotation, index+1, problem))
}
