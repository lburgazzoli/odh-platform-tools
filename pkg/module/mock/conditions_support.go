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
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	frameworkapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"k8s.io/apimachinery/pkg/api/validate/content"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	yamlstream "sigs.k8s.io/kustomize/kyaml/yaml"
	"sigs.k8s.io/yaml"
)

// decodeAnnotationConditions parses one YAML or JSON sequence of condition
// mappings. Duplicate mapping keys, merge keys, and aliases keep their
// ordinary decoder semantics, and unknown fields are ignored.
func decodeAnnotationConditions(raw string) ([]annotationCondition, error) {
	// Decoding into a raw node checks the syntax and the single-document
	// boundary without applying any duplicate-key policy.
	documents := yamlstream.NewDecoder(strings.NewReader(raw))
	var document yamlstream.Node
	if err := documents.Decode(&document); err != nil {
		return nil, invalidConditionsAnnotation(err)
	}
	var extra yamlstream.Node
	if err := documents.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, invalidConditionsAnnotation(errors.New("must contain a single YAML document"))
	}

	// YAMLToJSON resolves duplicate keys, merges, and aliases with
	// ordinary YAML semantics; JSON passes through unchanged.
	converted, err := yaml.YAMLToJSON([]byte(raw))
	if err != nil {
		return nil, invalidConditionsAnnotation(err)
	}

	var entries []annotationCondition
	if err := json.Unmarshal(converted, &entries); err != nil {
		return nil, invalidConditionsAnnotation(err)
	}
	if entries == nil {
		return nil, invalidConditionsAnnotation(errors.New("must contain a sequence of condition mappings"))
	}

	for index, entry := range entries {
		if err := validateAnnotationCondition(index, entry); err != nil {
			return nil, err
		}
	}

	return entries, nil
}

// validateAnnotationCondition enforces the checks that survive the
// permissive decoding: a usable condition type and a known status.
func validateAnnotationCondition(index int, entry annotationCondition) error {
	switch {
	case len(content.IsLabelKey(entry.Type)) != 0:
		return invalidAnnotationCondition(index, "type must be a valid Kubernetes condition name")
	case entry.Type == string(frameworkapi.ConditionTypeReady) || entry.Type == string(frameworkapi.ConditionTypeProvisioningSucceeded):
		return invalidAnnotationCondition(index, "Ready and ProvisioningSucceeded are reserved")
	case entry.Status != metav1.ConditionTrue && entry.Status != metav1.ConditionFalse && entry.Status != metav1.ConditionUnknown:
		return invalidAnnotationCondition(index, "status must be True, False, or Unknown")
	default:
		return nil
	}
}

func invalidConditionsAnnotation(cause error) error {
	return moduleerrors.New(moduleerrors.ErrInvalidMockAnnotation,
		conditionsAnnotation+" must contain one YAML or JSON sequence of condition mappings", moduleerrors.WithCause(cause))
}

func invalidAnnotationCondition(index int, problem string) error {
	return moduleerrors.New(moduleerrors.ErrInvalidMockAnnotation,
		fmt.Sprintf("%s condition %d: %s", conditionsAnnotation, index+1, problem))
}
