package loader

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	jsonv2 "encoding/json/v2"

	yamlv2 "go.yaml.in/yaml/v2"
	"sigs.k8s.io/yaml"

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
	definition, err := decodeDefinition(data)
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

// decodeDefinition strictly decodes the definition bytes through the
// Kubernetes YAML machinery. The stream must hold exactly one nonempty
// document; the strict YAML-to-JSON conversion rejects duplicate keys
// and malformed input, and the typed JSON decode rejects unknown
// members and name-case mismatches.
func decodeDefinition(data []byte) (config.PlatformModuleDefinition, error) {
	if err := requireSingleDocument(data); err != nil {
		return config.PlatformModuleDefinition{}, err
	}

	encoded, err := yaml.YAMLToJSONStrict(data)
	if err != nil {
		return config.PlatformModuleDefinition{}, err
	}

	var definition config.PlatformModuleDefinition
	if err := jsonv2.Unmarshal(encoded, &definition, jsonv2.RejectUnknownMembers(true)); err != nil {
		return config.PlatformModuleDefinition{}, err
	}

	return definition, nil
}

// requireSingleDocument rejects empty and multi-document streams: the
// Kubernetes YAML conversion reads only the first stream document, so
// a second document would otherwise be silently ignored.
func requireSingleDocument(data []byte) error {
	decoder := yamlv2.NewDecoder(bytes.NewReader(data))

	documents := 0
	for {
		var document any
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if document != nil {
			documents++
		}
	}

	switch {
	case documents == 0:
		return errors.New("definition must contain one object document")
	case documents > 1:
		return errors.New("definition must contain exactly one document")
	default:
		return nil
	}
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
