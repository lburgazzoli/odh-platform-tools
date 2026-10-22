// Package jq evaluates jq expressions against decoded JSON values,
// such as unstructured Kubernetes objects. Evaluation failures are
// returned as errors: callers never have to guess between a failed
// match and a failed expression.
package jq

import (
	"fmt"
	"math"

	"github.com/itchyny/gojq"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Matchesf reports whether the jq expression evaluates to true for
// the input. The input may be an unstructured.Unstructured, a
// *unstructured.Unstructured, or any decoded JSON value; the
// unstructured forms are evaluated against their content.
//
// The expression is a format string: the args are interpolated with
// fmt verbs before the expression is parsed, so a literal percent
// sign must be written %%.
//
// The expression must yield exactly one boolean value. An expression
// that fails to parse, fails to evaluate, yields no value, more than
// one value, or a non-boolean, is an error.
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

// Countf evaluates the jq expression and reports the non-negative
// integer it yields. The input and format-string rules match
// Matchesf. An expression that fails to parse or evaluate, yields no
// value, more than one value, or a non-negative-integer value, is an
// error.
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

// evaluate parses the formatted expression, runs it against the
// input, and returns its single output value. The unstructured forms
// are evaluated against their content, not the Go struct.
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
