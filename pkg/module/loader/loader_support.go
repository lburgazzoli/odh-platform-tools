package loader

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	jsonv2 "encoding/json/v2"

	yamlv2 "go.yaml.in/yaml/v2"

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

	return config.Document{
		Definition:     normalized,
		DefinitionFile: file,
		RendererEntry:  rendererEntry(normalized.Spec.Manifests, filepath.Dir(file)),
	}, nil
}

// sourceError marks err as a definition failure and names the source
// file. The cause stays reachable through errors.Is and errors.As.
func sourceError(file string, err error) error {
	return config.NewDefinitionError(fmt.Errorf("%s: %w", file, err))
}

// decodeDefinition decodes the definition bytes in a single pass
// through the go.yaml.in/yaml/v2 machinery that the Kubernetes YAML
// conversion itself uses. The stream must hold exactly one nonempty
// document: further documents fail instead of being silently ignored.
// The document value is round-tripped through JSON for the strict
// typed decode, which rejects unknown members and name-case
// mismatches.
func decodeDefinition(data []byte) (config.PlatformModuleDefinition, error) {
	decoder := yamlv2.NewDecoder(bytes.NewReader(data))

	var document any
	for document == nil {
		if err := decoder.Decode(&document); err != nil {
			if errors.Is(err, io.EOF) {
				return config.PlatformModuleDefinition{}, errors.New("definition must contain one object document")
			}
			return config.PlatformModuleDefinition{}, err
		}
	}

	var extra any
	for {
		err := decoder.Decode(&extra)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return config.PlatformModuleDefinition{}, err
		}
		if extra != nil {
			return config.PlatformModuleDefinition{}, errors.New("definition must contain exactly one document")
		}
	}

	encoded, err := jsonv2.Marshal(document)
	if err != nil {
		return config.PlatformModuleDefinition{}, err
	}

	var definition config.PlatformModuleDefinition
	if err := jsonv2.Unmarshal(encoded, &definition, jsonv2.RejectUnknownMembers(true)); err != nil {
		return config.PlatformModuleDefinition{}, err
	}

	return definition, nil
}

// rendererEntry resolves the selected manifest backend path against
// the module root: the canonical directory of the definition file. The
// path is used exactly as configured, and an absolute path is honored
// as is. Existence and shape of the entry are the render engines' to
// validate, so a missing or unsuitable entry fails at render time,
// not at load time.
func rendererEntry(manifests config.Manifests, root string) string {
	var authored string

	switch manifests.Format {
	case config.FormatHelm:
		authored = manifests.Helm.Path
	case config.FormatKustomize:
		authored = manifests.Kustomize.Path
	}

	if filepath.IsAbs(authored) {
		return filepath.Clean(authored)
	}

	return filepath.Join(root, authored)
}
