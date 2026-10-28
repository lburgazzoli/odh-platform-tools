// Package jq evaluates expressions against JSON values and unstructured objects.
package jq

import (
	"fmt"
	"math"

	"github.com/itchyny/gojq"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Matchesf evaluates a fmt-formatted jq expression and requires exactly one
// boolean result. Literal percent signs must be escaped as %%.
func Matchesf(input any, format string, args ...any) (bool, error) {
	value, expression, err := evaluate(input, format, args...)
	if err != nil {
		return false, err
	}

	result, isBool := value.(bool)
	if !isBool {
		return false, fmt.Errorf("jq expression %q yielded %T, want a boolean", expression, value)
	}

	return result, nil
}

// Countf follows Matchesf's input and formatting rules, requiring exactly one
// non-negative integer result.
func Countf(input any, format string, args ...any) (int, error) {
	value, expression, err := evaluate(input, format, args...)
	if err != nil {
		return 0, err
	}

	switch count := value.(type) {
	case int:
		if count >= 0 {
			return count, nil
		}
	case int64:
		if count >= 0 {
			return int(count), nil
		}
	case float64:
		if count >= 0 && count == math.Trunc(count) {
			return int(count), nil
		}
	}

	return 0, fmt.Errorf("jq expression %q yielded %T, want a non-negative integer", expression, value)
}

func evaluate(input any, format string, args ...any) (any, string, error) {
	switch value := input.(type) {
	case unstructured.Unstructured:
		input = value.Object
	case *unstructured.Unstructured:
		input = value.Object
	}

	expression := fmt.Sprintf(format, args...)

	query, err := gojq.Parse(expression)
	if err != nil {
		return nil, expression, fmt.Errorf("parse jq expression %q: %w", expression, err)
	}

	iterator := query.Run(input)

	value, ok := iterator.Next()
	if !ok {
		return nil, expression, fmt.Errorf("jq expression %q yielded no value", expression)
	}

	if err, isErr := value.(error); isErr {
		return nil, expression, fmt.Errorf("evaluate jq expression %q: %w", expression, err)
	}

	if _, more := iterator.Next(); more {
		return nil, expression, fmt.Errorf("jq expression %q yielded more than one value", expression)
	}

	return value, expression, nil
}
