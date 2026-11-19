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
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fieldState classifies one observed field value. JSON null counts as
// absent, not malformed.
type fieldState int

const (
	fieldAbsent fieldState = iota
	fieldPresent
	fieldMalformed
)

// violation is one structural or semantic defect with its field path.
type violation struct {
	field   string
	message string
}

// message joins every violation message in order.
func violationsMessage(findings []violation) string {
	messages := make([]string, 0, len(findings))
	for i := range findings {
		messages = append(messages, findings[i].message)
	}

	return strings.Join(messages, "; ")
}

// firstField returns the first violation's field path, or empty.
func firstField(findings []violation) string {
	if len(findings) == 0 {
		return ""
	}

	return findings[0].field
}

// nestedValue walks path through nested objects; a missing key or a
// non-object container reports not-found.
func nestedValue(object map[string]any, path ...string) (any, bool) {
	var current any = object

	for _, key := range path {
		container, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}

		value, ok := container[key]
		if !ok {
			return nil, false
		}

		current = value
	}

	return current, true
}

// observedMap reads a nested object field.
func observedMap(object map[string]any, path ...string) (map[string]any, fieldState) {
	value, ok := nestedValue(object, path...)
	if !ok || value == nil {
		return nil, fieldAbsent
	}

	typed, ok := value.(map[string]any)
	if !ok {
		return nil, fieldMalformed
	}

	return typed, fieldPresent
}

// observedString reads a nested string field.
func observedString(object map[string]any, path ...string) (string, fieldState) {
	value, ok := nestedValue(object, path...)
	if !ok || value == nil {
		return "", fieldAbsent
	}

	typed, ok := value.(string)
	if !ok {
		return "", fieldMalformed
	}

	return typed, fieldPresent
}

// observedInt64 reads a nested integer field, accepting every numeric
// representation unstructured content uses: int64 from typed conversions
// and hand-built maps, int and integral float64 from JSON decoding, and
// json.Number from streaming decoders.
func observedInt64(object map[string]any, path ...string) (int64, fieldState) {
	value, ok := nestedValue(object, path...)
	if !ok || value == nil {
		return 0, fieldAbsent
	}

	switch typed := value.(type) {
	case int64:
		return typed, fieldPresent
	case int:
		return int64(typed), fieldPresent
	case int32:
		return int64(typed), fieldPresent
	case float64:
		// Reject fractions and values that cannot round-trip through
		// an int64 without overflow-driven distortion.
		if typed != math.Trunc(typed) || math.Abs(typed) >= 1<<62 {
			return 0, fieldMalformed
		}

		return int64(typed), fieldPresent
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fieldMalformed
		}

		return parsed, fieldPresent
	default:
		return 0, fieldMalformed
	}
}

// observedNonNegativeInt64 reads a nested nonnegative integer field; a
// negative value is malformed, not zero.
func observedNonNegativeInt64(object map[string]any, path ...string) (int64, fieldState) {
	value, state := observedInt64(object, path...)
	if state == fieldPresent && value < 0 {
		return 0, fieldMalformed
	}

	return value, state
}

// observedSlice reads a nested array field.
func observedSlice(object map[string]any, path ...string) ([]any, fieldState) {
	value, ok := nestedValue(object, path...)
	if !ok || value == nil {
		return nil, fieldAbsent
	}

	typed, ok := value.([]any)
	if !ok {
		return nil, fieldMalformed
	}

	return typed, fieldPresent
}

// terminating reports whether the object carries a nonempty deletion
// timestamp.
func terminating(object *unstructured.Unstructured) bool {
	deletion, state := observedString(object.Object, "metadata", "deletionTimestamp")

	return state == fieldPresent && deletion != ""
}

// conditionTypePattern is the condition type grammar from the framework
// condition schema; a type is valid when it matches and is not overlong.
var conditionTypePattern = regexp.MustCompile(
	`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])$`)

const conditionTypeMaxLength = 316

// validConditionType reports whether the condition type is a valid
// Kubernetes condition type.
func validConditionType(conditionType string) bool {
	return len(conditionType) <= conditionTypeMaxLength &&
		conditionTypePattern.MatchString(conditionType)
}

// validTimestamp reports whether a nonempty timestamp string parses as
// RFC3339.
func validTimestamp(value string) bool {
	_, err := time.Parse(time.RFC3339, value)

	return err == nil
}

// passed returns a passing check result.
func passed(identity CheckResult, message string) CheckResult {
	identity.Status = StatusPass
	identity.Message = message

	return identity
}

// failed returns a failing check result for one defect.
func failed(identity CheckResult, field string, message string) CheckResult {
	identity.Status = StatusFail
	identity.Field = field
	identity.Message = message

	return identity
}

// failedFindings returns a failing check result joining every violation
// message, with the first violation's field path.
func failedFindings(identity CheckResult, findings []violation) CheckResult {
	return failed(identity, firstField(findings), violationsMessage(findings))
}

// skipped returns a skipped check result whose reason names the missing
// prerequisite.
func skipped(identity CheckResult, reason string) CheckResult {
	identity.Status = StatusSkipped
	identity.Message = reason

	return identity
}

// errored returns an errored check result for an observation failure.
func errored(identity CheckResult, message string) CheckResult {
	identity.Status = StatusError
	identity.Message = message

	return identity
}

// describe returns the object reference text used in messages.
func describe(gvkKind string, name string) string {
	return fmt.Sprintf("%s %q", gvkKind, name)
}

// conditionObservation is one parsed status condition entry.
type conditionObservation struct {
	Type string

	Status string

	ObservedGeneration int64

	hasObservedGeneration bool
}

// releaseObservation is one parsed status release entry.
type releaseObservation struct {
	Name string

	Version string
}

// statusObservation carries the module status's per-branch structural
// validity and parsed values. Prerequisite validity is tracked per
// branch: a malformed condition entry suppresses only readiness, and a
// malformed release entry suppresses only consumption. An absent
// optional branch is structurally valid; its semantics belong to the
// readiness and consumption checks.
type statusObservation struct {
	// violations are the structural findings MODULE_CONTRACT reports.
	violations []violation

	phaseValid              bool
	observedGenerationValid bool
	conditionsValid         bool
	releasesValid           bool

	phase              string
	observedGeneration int64
	conditions         []conditionObservation
	releases           []releaseObservation
}

// observeStatus inspects the unstructured CR before typed conversion can
// erase absent, malformed, duplicate, or unknown-shaped data. Unknown
// unrelated status fields are allowed.
func observeStatus(cr *unstructured.Unstructured) statusObservation {
	// Absent optional branches are structurally valid until a malformed
	// value or container is observed.
	observation := statusObservation{
		phaseValid:              true,
		observedGenerationValid: true,
		conditionsValid:         true,
		releasesValid:           true,
	}

	status, state := observedMap(cr.Object, "status")
	switch state {
	case fieldAbsent:
		observation.violations = append(observation.violations, violation{
			field:   "status",
			message: "status is required but not set",
		})

		return observation
	case fieldMalformed:
		observation.violations = append(observation.violations, violation{
			field:   "status",
			message: "status must be an object",
		})
		observation.phaseValid = false
		observation.observedGenerationValid = false
		observation.conditionsValid = false
		observation.releasesValid = false

		return observation
	}

	phase, state := observedString(status, "phase")
	switch state {
	case fieldMalformed:
		observation.phaseValid = false
		observation.violations = append(observation.violations, violation{
			field:   "status.phase",
			message: "status.phase must be a string when set",
		})
	case fieldPresent:
		observation.phase = phase
	}

	generation, state := observedNonNegativeInt64(status, "observedGeneration")
	switch state {
	case fieldMalformed:
		observation.observedGenerationValid = false
		observation.violations = append(observation.violations, violation{
			field:   "status.observedGeneration",
			message: "status.observedGeneration must be a nonnegative integer when set",
		})
	case fieldPresent:
		observation.observedGeneration = generation
	}

	conditionEntries, state := observedSlice(status, "conditions")
	switch state {
	case fieldMalformed:
		observation.conditionsValid = false
		observation.violations = append(observation.violations, violation{
			field:   "status.conditions",
			message: "status.conditions must be an array of condition objects when set",
		})
	case fieldPresent:
		conditions, findings := observeConditions(conditionEntries)
		observation.conditions = conditions
		if len(findings) > 0 {
			observation.conditionsValid = false
			observation.violations = append(observation.violations, findings...)
		}
	}

	releaseEntries, state := observedSlice(status, "releases")
	switch state {
	case fieldMalformed:
		observation.releasesValid = false
		observation.violations = append(observation.violations, violation{
			field:   "status.releases",
			message: "status.releases must be an array of release objects when set",
		})
	case fieldPresent:
		releases, findings := observeReleases(releaseEntries)
		observation.releases = releases
		if len(findings) > 0 {
			observation.releasesValid = false
			observation.violations = append(observation.violations, findings...)
		}
	}

	return observation
}

// observeConditions parses every condition entry. Malformed entries stay
// visible as violations; they suppress only the readiness check.
func observeConditions(entries []any) ([]conditionObservation, []violation) {
	conditions := make([]conditionObservation, 0, len(entries))
	var findings []violation
	seen := make(map[string]struct{}, len(entries))

	for i, entry := range entries {
		condition, entryFindings := observeCondition(i, entry)
		findings = append(findings, entryFindings...)

		if condition.Type != "" {
			if _, duplicate := seen[condition.Type]; duplicate {
				findings = append(findings, violation{
					field:   fmt.Sprintf("status.conditions[%d].type", i),
					message: fmt.Sprintf("duplicate condition type %q", condition.Type),
				})
			}

			seen[condition.Type] = struct{}{}
		}

		conditions = append(conditions, condition)
	}

	return conditions, findings
}

func observeCondition(index int, entry any) (conditionObservation, []violation) {
	field := fmt.Sprintf("status.conditions[%d]", index)

	object, ok := entry.(map[string]any)
	if !ok {
		return conditionObservation{}, []violation{{field: field, message: field + " must be a condition object"}}
	}

	var findings []violation
	condition := conditionObservation{}

	conditionType, state := observedString(object, "type")
	switch state {
	case fieldAbsent:
		findings = append(findings, violation{field: field + ".type", message: field + ".type is required"})
	case fieldMalformed:
		findings = append(findings, violation{field: field + ".type", message: field + ".type must be a string"})
	default:
		if !validConditionType(conditionType) {
			findings = append(findings, violation{
				field:   field + ".type",
				message: fmt.Sprintf("%s.type %q is not a valid Kubernetes condition type", field, conditionType),
			})
		}

		condition.Type = conditionType
	}

	conditionStatus, state := observedString(object, "status")
	switch state {
	case fieldAbsent:
		findings = append(findings, violation{field: field + ".status", message: field + ".status is required"})
	case fieldMalformed:
		findings = append(findings, violation{field: field + ".status", message: field + ".status must be a string"})
	default:
		switch conditionStatus {
		case string(metav1.ConditionTrue), string(metav1.ConditionFalse), string(metav1.ConditionUnknown):
			condition.Status = conditionStatus
		default:
			findings = append(findings, violation{
				field:   field + ".status",
				message: fmt.Sprintf("%s.status %q must be one of True, False, Unknown", field, conditionStatus),
			})
		}
	}

	generation, state := observedNonNegativeInt64(object, "observedGeneration")
	switch state {
	case fieldMalformed:
		findings = append(findings, violation{
			field:   field + ".observedGeneration",
			message: field + ".observedGeneration must be a nonnegative integer when set",
		})
	case fieldPresent:
		condition.ObservedGeneration = generation
		condition.hasObservedGeneration = true
	}

	for _, name := range []string{"reason", "message", "severity"} {
		if _, state := observedString(object, name); state == fieldMalformed {
			findings = append(findings, violation{
				field:   field + "." + name,
				message: field + "." + name + " must be a string when set",
			})
		}
	}

	for _, name := range []string{"lastTransitionTime", "lastHeartbeatTime"} {
		value, state := observedString(object, name)
		switch {
		case state == fieldMalformed:
			findings = append(findings, violation{
				field:   field + "." + name,
				message: field + "." + name + " must be a string timestamp when set",
			})
		case state == fieldPresent && value != "" && !validTimestamp(value):
			findings = append(findings, violation{
				field:   field + "." + name,
				message: fmt.Sprintf("%s.%s %q is not an RFC3339 timestamp", field, name, value),
			})
		}
	}

	return condition, findings
}

// observeReleases parses every release entry. Malformed entries stay
// visible as violations; they suppress only the consumption check.
func observeReleases(entries []any) ([]releaseObservation, []violation) {
	releases := make([]releaseObservation, 0, len(entries))
	var findings []violation
	seen := make(map[string]struct{}, len(entries))

	for i, entry := range entries {
		release, entryFindings := observeRelease(i, entry)
		findings = append(findings, entryFindings...)

		if release.Name != "" {
			if _, duplicate := seen[release.Name]; duplicate {
				findings = append(findings, violation{
					field:   fmt.Sprintf("status.releases[%d].name", i),
					message: fmt.Sprintf("duplicate release name %q", release.Name),
				})
			}

			seen[release.Name] = struct{}{}
		}

		releases = append(releases, release)
	}

	return releases, findings
}

func observeRelease(index int, entry any) (releaseObservation, []violation) {
	field := fmt.Sprintf("status.releases[%d]", index)

	object, ok := entry.(map[string]any)
	if !ok {
		return releaseObservation{}, []violation{{field: field, message: field + " must be a release object"}}
	}

	var findings []violation
	release := releaseObservation{}

	name, state := observedString(object, "name")
	switch state {
	case fieldAbsent:
		findings = append(findings, violation{field: field + ".name", message: field + ".name is required"})
	case fieldMalformed:
		findings = append(findings, violation{field: field + ".name", message: field + ".name must be a string"})
	default:
		if strings.TrimSpace(name) == "" {
			findings = append(findings, violation{field: field + ".name", message: field + ".name must not be blank"})
		}

		release.Name = name
	}

	for _, key := range []string{"version", "repoUrl"} {
		value, state := observedString(object, key)
		if state == fieldMalformed {
			findings = append(findings, violation{
				field:   field + "." + key,
				message: field + "." + key + " must be a string when set",
			})

			continue
		}

		if key == "version" {
			release.Version = value
		}
	}

	return release, findings
}

// findCondition returns the parsed condition with the given type, or nil.
func findCondition(conditions []conditionObservation, conditionType string) *conditionObservation {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}

	return nil
}

// findRelease returns the parsed release with the given name, or nil.
func findRelease(releases []releaseObservation, name string) *releaseObservation {
	for i := range releases {
		if releases[i].Name == name {
			return &releases[i]
		}
	}

	return nil
}
