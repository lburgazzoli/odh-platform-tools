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

// Package values loads Helm value overrides for module definition
// generation: one optional YAML mapping file plus ordered --set-style
// expressions, merged into literal JSON values.
package values

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	jsonv2 "encoding/json/v2"

	"helm.sh/helm/v4/pkg/strvals"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

var errNoFilesystem = errors.New("no filesystem supplied")

// Load reads the optional values file at path from fsys and applies each
// override expression in order. An empty path loads no file; omitting both
// the file and the overrides returns nil values.
func Load(
	fsys fs.FS,
	path string,
	overrides []string,
) (map[string]apiextensionsv1.JSON, error) {
	if path == "" && len(overrides) == 0 {
		return nil, nil
	}

	var data []byte
	if path != "" {
		if fsys == nil {
			return nil, readFailure(path, errNoFilesystem)
		}

		read, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, readFailure(path, err)
		}

		data = read
	}

	return build(data, path != "", path, overrides)
}

// LoadFromPath reads the optional values file at the native filesystem
// path and applies each override expression in order, with the same
// result as Load.
func LoadFromPath(
	path string,
	overrides []string,
) (map[string]apiextensionsv1.JSON, error) {
	if path == "" && len(overrides) == 0 {
		return nil, nil
	}

	var data []byte
	if path != "" {
		read, err := os.ReadFile(path)
		if err != nil {
			return nil, readFailure(path, err)
		}

		data = read
	}

	return build(data, path != "", path, overrides)
}

// build merges the parsed file mapping with the override expressions and
// encodes the result as literal JSON values. Overrides apply in order
// through the native Helm parser; type conflicts fail instead of being
// coerced, and only overrides are embedded, never chart defaults.
func build(
	data []byte,
	hasFile bool,
	path string,
	overrides []string,
) (map[string]apiextensionsv1.JSON, error) {
	merged := make(map[string]any)

	if hasFile {
		parsed, err := parseDocument(data)
		if err != nil {
			return nil, moduleerrors.New(
				moduleerrors.ErrInvalidDefinition,
				"invalid Helm values file "+path,
				moduleerrors.WithCause(err),
			)
		}

		merged = parsed
	}

	for index, expression := range overrides {
		if err := strvals.ParseInto(expression, merged); err != nil {
			return nil, moduleerrors.New(
				moduleerrors.ErrInvalidDefinition,
				fmt.Sprintf("invalid Helm value override at position %d", index+1),
				moduleerrors.WithCause(err),
			)
		}
	}

	encoded := make(map[string]apiextensionsv1.JSON, len(merged))
	for key, value := range merged {
		raw, err := jsonv2.Marshal(value)
		if err != nil {
			return nil, moduleerrors.New(
				moduleerrors.ErrInvalidDefinition,
				"encode Helm value overrides",
				moduleerrors.WithCause(err),
			)
		}

		encoded[key] = apiextensionsv1.JSON{Raw: raw}
	}

	return encoded, nil
}

// readFailure classifies a values-file read failure. The public message
// names the file, never its contents.
func readFailure(path string, cause error) error {
	return moduleerrors.New(
		moduleerrors.ErrInvalidDefinition,
		"read Helm values file "+path,
		moduleerrors.WithCause(cause),
	)
}
