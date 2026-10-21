package loader

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	jsonv2 "encoding/json/v2"

	"go.yaml.in/yaml/v3"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// readDefinitionFile reads the definition file at path and returns its
// canonical, symlink-resolved location together with its bytes. The
// path may be relative to the working directory or absolute.
func readDefinitionFile(path string) (string, []byte, error) {
	if path == "" {
		return "", nil, config.NewDefinitionError(errors.New("definition file path is required"))
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, config.NewDefinitionError(fmt.Errorf("resolve definition file %s: %w", path, err))
	}

	file, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", nil, config.NewDefinitionError(fmt.Errorf("resolve definition file %s: %w", path, err))
	}

	info, err := os.Stat(file)
	if err != nil {
		return "", nil, config.NewDefinitionError(fmt.Errorf("stat definition file %s: %w", path, err))
	}
	if !info.Mode().IsRegular() {
		return "", nil, config.NewDefinitionError(fmt.Errorf("definition file %s is not a regular file", path))
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return "", nil, config.NewDefinitionError(fmt.Errorf("read definition file %s: %w", path, err))
	}

	return file, data, nil
}

// buildDocument parses, decodes, validates, and resolves the definition
// read from file.
func buildDocument(file string, data []byte) (config.Document, error) {
	value, err := parseDocument(data)
	if err != nil {
		return config.Document{}, sourceError(file, err)
	}

	definition, err := decodeDefinition(value)
	if err != nil {
		return config.Document{}, sourceError(file, err)
	}

	normalized, err := definition.Normalize()
	if err != nil {
		// Validation errors already carry the ErrInvalidDefinition
		// sentinel; only the source file is added.
		return config.Document{}, fmt.Errorf("%s: %w", file, err)
	}

	root := filepath.Dir(file)

	entry, err := resolveRendererEntry(normalized, root)
	if err != nil {
		return config.Document{}, err
	}

	return config.Document{
		Definition:     normalized,
		DefinitionFile: file,
		ModuleRoot:     root,
		RendererEntry:  entry,
	}, nil
}

// sourceError marks err as a definition failure and names the source
// file. The cause stays reachable through errors.Is and errors.As.
func sourceError(file string, err error) error {
	return config.NewDefinitionError(fmt.Errorf("%s: %w", file, err))
}

// parseDocument parses data as a single YAML (or JSON) object document
// and converts it to JSON-compatible values. It rejects malformed
// input, multiple nonempty documents, non-object roots, duplicate and
// non-string keys, aliases, merge keys, and unsupported scalar forms.
func parseDocument(data []byte) (any, error) {
	var documents []any

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var node yaml.Node
		err := decoder.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(node.Content) == 0 {
			continue
		}

		value, err := convertNode(node.Content[0])
		if err != nil {
			return nil, err
		}
		if value != nil {
			documents = append(documents, value)
		}
	}

	if len(documents) == 0 {
		return nil, errors.New("definition must contain one object document")
	}
	if len(documents) > 1 {
		return nil, errors.New("definition must contain exactly one document")
	}

	root := documents[0]
	if _, ok := root.(map[string]any); !ok {
		return nil, errors.New("definition document must be an object")
	}

	return root, nil
}

// convertNode converts a YAML node to a JSON-compatible value.
func convertNode(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.MappingNode:
		return convertMapping(node)
	case yaml.SequenceNode:
		return convertSequence(node)
	case yaml.ScalarNode:
		return convertScalar(node)
	case yaml.AliasNode:
		return nil, fmt.Errorf("line %d: YAML aliases are not supported", node.Line)
	default:
		return nil, fmt.Errorf("line %d: unsupported YAML node", node.Line)
	}
}

// convertMapping converts a mapping node, requiring plain string keys
// and rejecting duplicate keys. Merge keys fail the string-key rule
// because they resolve to the !!merge tag.
func convertMapping(node *yaml.Node) (any, error) {
	mapping := make(map[string]any, len(node.Content)/2)

	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]

		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, fmt.Errorf("line %d: mapping keys must be plain strings", key.Line)
		}
		if _, duplicate := mapping[key.Value]; duplicate {
			return nil, fmt.Errorf("line %d: duplicate mapping key %q", key.Line, key.Value)
		}

		value, err := convertNode(node.Content[i+1])
		if err != nil {
			return nil, err
		}

		mapping[key.Value] = value
	}

	return mapping, nil
}

// convertSequence converts a sequence node.
func convertSequence(node *yaml.Node) (any, error) {
	sequence := make([]any, 0, len(node.Content))

	for _, item := range node.Content {
		value, err := convertNode(item)
		if err != nil {
			return nil, err
		}

		sequence = append(sequence, value)
	}

	return sequence, nil
}

// convertScalar converts a resolved YAML scalar to a JSON-compatible
// value. Timestamps keep their literal text: a value such as 2026-01-02
// is a string, never a parsed date. Numbers JSON cannot represent are
// rejected.
func convertScalar(node *yaml.Node) (any, error) {
	switch node.Tag {
	case "!!str", "!!timestamp":
		return node.Value, nil
	case "!!null":
		return nil, nil
	case "!!bool":
		return convertBool(node)
	case "!!int":
		return convertInt(node)
	case "!!float":
		return convertFloat(node)
	default:
		return nil, fmt.Errorf("line %d: unsupported value %s", node.Line, node.Tag)
	}
}

func convertBool(node *yaml.Node) (any, error) {
	switch node.Value {
	case "true", "True", "TRUE":
		return true, nil
	case "false", "False", "FALSE":
		return false, nil
	default:
		return nil, fmt.Errorf("line %d: unsupported boolean %q", node.Line, node.Value)
	}
}

func convertInt(node *yaml.Node) (any, error) {
	value, base := node.Value, 10

	if after, found := strings.CutPrefix(value, "0x"); found {
		value, base = after, 16
	} else if after, found := strings.CutPrefix(value, "0o"); found {
		value, base = after, 8
	}

	if number, err := strconv.ParseInt(value, base, 64); err == nil {
		return number, nil
	}
	if number, err := strconv.ParseUint(value, base, 64); err == nil {
		return number, nil
	}

	return nil, fmt.Errorf("line %d: unsupported integer %q", node.Line, node.Value)
}

func convertFloat(node *yaml.Node) (any, error) {
	number, err := strconv.ParseFloat(node.Value, 64)
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
		return nil, fmt.Errorf("line %d: unsupported number %q", node.Line, node.Value)
	}

	return number, nil
}

// decodeDefinition strictly decodes the parsed document into the
// definition types, rejecting unknown members and name-case
// mismatches.
func decodeDefinition(value any) (config.PlatformModuleDefinition, error) {
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return config.PlatformModuleDefinition{}, err
	}

	var definition config.PlatformModuleDefinition
	err = jsonv2.Unmarshal(encoded, &definition, jsonv2.RejectUnknownMembers(true))
	if err != nil {
		return config.PlatformModuleDefinition{}, err
	}

	return definition, nil
}

// resolveRendererEntry resolves the selected manifest backend path
// against the module root and validates the backend entry file. The
// authored path string in the definition is never rewritten.
func resolveRendererEntry(definition config.PlatformModuleDefinition, root string) (string, error) {
	manifests := definition.Spec.Manifests

	switch manifests.Format {
	case config.FormatHelm:
		return resolveHelmEntry(manifests.Helm.Path, root)
	case config.FormatKustomize:
		return resolveKustomizeEntry(manifests.Kustomize.Path, root)
	default:
		// Unreachable: validation rejects unknown formats first.
		return "", config.NewDefinitionError(fmt.Errorf("unsupported manifest format %q", manifests.Format))
	}
}

// resolveHelmEntry resolves the chart directory and requires its
// Chart.yaml.
func resolveHelmEntry(authored, root string) (string, error) {
	entry, err := resolveContainedPath(authored, root, "helm path")
	if err != nil {
		return "", err
	}

	if err := requireDirectory(entry, "helm path", authored); err != nil {
		return "", err
	}

	if err := requireRegularFile(filepath.Join(entry, "Chart.yaml"), "Chart.yaml", authored); err != nil {
		return "", err
	}

	return entry, nil
}

// kustomizationFiles are the entry file names Kustomize accepts. The
// loader requires one of them because the engine would otherwise fall
// back to a default/ subdirectory and silently change the selected
// input.
var kustomizationFiles = []string{"kustomization.yaml", "kustomization.yml"}

// resolveKustomizeEntry resolves the kustomization directory and
// requires an explicit kustomization file, with no default/ fallback.
func resolveKustomizeEntry(authored, root string) (string, error) {
	entry, err := resolveContainedPath(authored, root, "kustomize path")
	if err != nil {
		return "", err
	}

	if err := requireDirectory(entry, "kustomize path", authored); err != nil {
		return "", err
	}

	for _, name := range kustomizationFiles {
		if isRegularFile(filepath.Join(entry, name)) {
			return entry, nil
		}
	}

	return "", config.NewDefinitionError(fmt.Errorf("kustomize path %q contains no kustomization.yaml", authored))
}

// resolveContainedPath resolves the authored path against the module
// root and returns its canonical form, rejecting anything that escapes
// the root through relative segments or symlinks.
func resolveContainedPath(authored, root, what string) (string, error) {
	path := authored
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}

	entry, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", config.NewDefinitionError(fmt.Errorf("resolve %s %q: %w", what, authored, err))
	}

	if !isWithinRoot(entry, root) {
		return "", config.NewDefinitionError(fmt.Errorf("%s %q escapes the module root %s", what, authored, root))
	}

	return entry, nil
}

// isWithinRoot reports whether path is root or inside it, comparing
// path components so root-prefix lookalikes fail.
func isWithinRoot(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}

	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// requireDirectory fails unless path is an existing directory.
func requireDirectory(path, what, authored string) error {
	info, err := os.Stat(path)
	if err != nil {
		return config.NewDefinitionError(fmt.Errorf("resolve %s %q: %w", what, authored, err))
	}
	if !info.IsDir() {
		return config.NewDefinitionError(fmt.Errorf("%s %q is not a directory", what, authored))
	}

	return nil
}

// requireRegularFile fails unless path is an existing regular file.
func requireRegularFile(path, name, authored string) error {
	info, err := os.Stat(path)
	if err != nil {
		return config.NewDefinitionError(fmt.Errorf("%s for %q is missing: %w", name, authored, err))
	}
	if !info.Mode().IsRegular() {
		return config.NewDefinitionError(fmt.Errorf("%s for %q is not a regular file", name, authored))
	}

	return nil
}

// isRegularFile reports whether path is an existing regular file.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}
