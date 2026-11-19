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

package values

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// YAML tags accepted by the shared JSON-compatible values domain.
// yaml v3 has no exported tag constants, so they are mirrored here.
const (
	tagString   = "!!str"
	tagNull     = "!!null"
	tagBoolean  = "!!bool"
	tagInteger  = "!!int"
	tagFloat    = "!!float"
	tagMapping  = "!!map"
	tagSequence = "!!seq"
)

// parseDocument decodes data as exactly one YAML mapping document and
// converts it to JSON-compatible values. Conversion walks the raw node
// tree so unsupported constructs fail before generic decoding loses
// the distinction: extra documents, non-mapping roots, duplicate keys,
// non-string keys, unsupported tags, timestamps, binary data,
// nonfinite or out-of-range numbers, and cyclic aliases.
func parseDocument(data []byte) (map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))

	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("values document is empty")
		}

		return nil, err
	}

	var trailing yaml.Node
	trailingErr := decoder.Decode(&trailing)
	if trailingErr == nil {
		return nil, errors.New("values document must contain exactly one mapping")
	}
	if !errors.Is(trailingErr, io.EOF) {
		return nil, trailingErr
	}

	converted, err := convertNode(&document, make(map[*yaml.Node]bool), newConversionBudget(&document))
	if err != nil {
		return nil, err
	}

	mapping, ok := converted.(map[string]any)
	if !ok {
		return nil, errors.New("values document root must be a mapping")
	}

	return mapping, nil
}

// conversionBudget bounds node conversions during alias expansion.
// The visiting set only guards cycles, so an acyclic alias graph can
// otherwise expand exponentially; the allowance is generous linear
// headroom over the document's distinct node count, keeping moderate
// anchor reuse working while exploding graphs fail fast with a
// message that carries no document content.
type conversionBudget struct {
	remaining int
}

// conversionHeadroom multiplies the document's node count to form
// the allowance; conversionBase keeps tiny documents usable.
const (
	conversionHeadroom = 20
	conversionBase     = 1000
)

// newConversionBudget sizes one document's conversion allowance.
func newConversionBudget(node *yaml.Node) *conversionBudget {
	counted := make(map[*yaml.Node]bool)
	count := 0

	var countNodes func(node *yaml.Node)
	countNodes = func(node *yaml.Node) {
		if node == nil || counted[node] {
			return
		}

		counted[node] = true
		count++

		countNodes(node.Alias)

		for _, child := range node.Content {
			countNodes(child)
		}
	}
	countNodes(node)

	return &conversionBudget{remaining: count*conversionHeadroom + conversionBase}
}

// spend consumes one conversion slot.
func (b *conversionBudget) spend() error {
	if b.remaining <= 0 {
		return errors.New("values document expands beyond the YAML alias budget")
	}

	b.remaining--

	return nil
}

// convertNode converts one YAML node to a JSON-compatible value. The
// visiting set expands aliases while guarding against cycles; the
// budget bounds acyclic alias expansion.
func convertNode(
	node *yaml.Node,
	visiting map[*yaml.Node]bool,
	budget *conversionBudget,
) (any, error) {
	if err := budget.spend(); err != nil {
		return nil, err
	}

	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return nil, errors.New("malformed YAML document node")
		}

		return convertNode(node.Content[0], visiting, budget)
	case yaml.AliasNode:
		if node.Alias == nil {
			return nil, errors.New("unresolved YAML alias")
		}

		return convertNode(node.Alias, visiting, budget)
	case yaml.MappingNode:
		return convertMapping(node, visiting, budget)
	case yaml.SequenceNode:
		return convertSequence(node, visiting, budget)
	case yaml.ScalarNode:
		return convertScalar(node)
	default:
		return nil, fmt.Errorf("unsupported YAML node kind %d", node.Kind)
	}
}

// convertMapping converts a mapping node, rejecting non-string and
// duplicate keys. Merge keys carry a non-string tag and are rejected
// with them.
func convertMapping(
	node *yaml.Node,
	visiting map[*yaml.Node]bool,
	budget *conversionBudget,
) (map[string]any, error) {
	if node.Tag != tagMapping {
		return nil, tagRejected(node)
	}

	if visiting[node] {
		return nil, errors.New("cyclic YAML alias")
	}

	visiting[node] = true
	defer delete(visiting, node)

	result := make(map[string]any, len(node.Content)/2)

	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]

		if key.Kind != yaml.ScalarNode || key.Tag != tagString {
			return nil, fmt.Errorf("mapping key at line %d must be a string", key.Line)
		}

		if _, exists := result[key.Value]; exists {
			return nil, fmt.Errorf("duplicate mapping key at line %d", key.Line)
		}

		converted, err := convertNode(value, visiting, budget)
		if err != nil {
			return nil, err
		}

		result[key.Value] = converted
	}

	return result, nil
}

// convertSequence converts a sequence node.
func convertSequence(
	node *yaml.Node,
	visiting map[*yaml.Node]bool,
	budget *conversionBudget,
) ([]any, error) {
	if node.Tag != tagSequence {
		return nil, tagRejected(node)
	}

	if visiting[node] {
		return nil, errors.New("cyclic YAML alias")
	}

	visiting[node] = true
	defer delete(visiting, node)

	result := make([]any, 0, len(node.Content))

	for _, item := range node.Content {
		converted, err := convertNode(item, visiting, budget)
		if err != nil {
			return nil, err
		}

		result = append(result, converted)
	}

	return result, nil
}

// convertScalar converts a scalar node, enforcing the supported
// JSON-compatible tag domain.
func convertScalar(node *yaml.Node) (any, error) {
	switch node.Tag {
	case tagString:
		return node.Value, nil
	case tagNull:
		return nil, nil
	case tagBoolean:
		return convertBoolean(node)
	case tagInteger:
		return convertInteger(node)
	case tagFloat:
		return convertFloat(node)
	default:
		return nil, tagRejected(node)
	}
}

// convertBoolean resolves the boolean literals yaml v3 recognizes.
func convertBoolean(node *yaml.Node) (bool, error) {
	if strings.EqualFold(node.Value, "true") {
		return true, nil
	}

	if strings.EqualFold(node.Value, "false") {
		return false, nil
	}

	return false, fmt.Errorf("invalid boolean at line %d", node.Line)
}

// convertInteger keeps integers exact. yaml v3 strips underscores
// when resolving, so the literal is normalized the same way here.
func convertInteger(node *yaml.Node) (any, error) {
	plain := strings.ReplaceAll(node.Value, "_", "")

	if parsed, err := strconv.ParseInt(plain, 0, 64); err == nil {
		return parsed, nil
	}

	if parsed, err := strconv.ParseUint(plain, 0, 64); err == nil {
		return parsed, nil
	}

	return nil, integerRejected(node)
}

// convertFloat converts a float node, rejecting nonfinite values and
// decimal integer literals yaml v3 resolved as floats because they
// exceed the 64-bit integer range.
func convertFloat(node *yaml.Node) (any, error) {
	plain := strings.ReplaceAll(node.Value, "_", "")

	if isIntegralLiteral(plain) {
		return nil, integerRejected(node)
	}

	parsed, err := strconv.ParseFloat(plain, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return nil, fmt.Errorf("float at line %d must be a finite 64-bit number", node.Line)
	}

	return parsed, nil
}

// isIntegralLiteral reports whether plain is decimal integer text.
func isIntegralLiteral(plain string) bool {
	digits := strings.TrimLeft(plain, "+-")
	if digits == "" {
		return false
	}

	for _, char := range digits {
		if char < '0' || char > '9' {
			return false
		}
	}

	return true
}

// integerRejected reports an integer outside the exact 64-bit range.
func integerRejected(node *yaml.Node) error {
	return fmt.Errorf("integer at line %d exceeds the 64-bit range", node.Line)
}

// tagRejected reports a tag outside the JSON-compatible domain. The
// message names the tag and line, never the value.
func tagRejected(node *yaml.Node) error {
	return fmt.Errorf("unsupported YAML tag %s at line %d", node.Tag, node.Line)
}
