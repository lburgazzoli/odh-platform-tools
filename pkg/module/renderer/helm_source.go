package renderer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"path"
	"path/filepath"
	"strings"

	jsonv2 "encoding/json/v2"
)

// captureHelmChart captures the whole chart tree at chartDir and
// validates it offline: vendored dependencies must be present, archives
// must be traversal-free, and values schemas must only use built-in
// metaschemas with fragment-only references.
func captureHelmChart(state *captureState, chartDir string) error {
	canonical, err := filepath.EvalSymlinks(chartDir)
	if err != nil {
		return unsafeError("missing chart directory %s", chartDir)
	}

	if _, err := relativeContainedPath(canonical, state.moduleRoot); err != nil {
		return err
	}

	if err := walkTree(state, canonical); err != nil {
		return err
	}

	rel, err := relativeContainedPath(canonical, state.moduleRoot)
	if err != nil {
		return err
	}

	return validateHelmChart(state, filepath.ToSlash(rel))
}

// validateHelmChart validates a captured chart tree: every values
// schema in the tree, every dependency vendored, every archive
// inspected without extraction, and every unpacked subchart recursed.
func validateHelmChart(state *captureState, chartRel string) error {
	chartFile, ok := state.fileAt(chartRel + "/Chart.yaml")
	if !ok {
		return unsafeError("chart at %s has no Chart.yaml", chartRel)
	}

	chart, err := decodeYAMLDocument(chartFile.Bytes)
	if err != nil {
		return unsafeError("invalid Chart.yaml in %s: %v", chartRel, err)
	}

	for _, file := range state.filesUnder(chartRel) {
		if path.Base(file.Path) == "values.schema.json" {
			if err := validateValuesSchema(file.Path, file.Bytes); err != nil {
				return err
			}
		}
	}

	if deps, ok := chart["dependencies"].([]any); ok {
		for _, dep := range deps {
			name, ok := dependencyName(dep)
			if !ok {
				return unsafeError("chart %s has a malformed dependency entry", chartRel)
			}

			if !dependencyVendored(state, chartRel, name) {
				return unsafeError(
					"chart %s dependency %q is not vendored under %s/charts; remote repositories are never fetched",
					chartRel, name, chartRel)
			}
		}
	}

	chartsRel := chartRel + "/charts"

	for _, file := range state.filesUnder(chartsRel) {
		if strings.HasSuffix(file.Path, ".tgz") {
			if err := inspectChartArchive(file.Path, file.Bytes); err != nil {
				return err
			}
		}
	}

	for _, file := range state.filesUnder(chartsRel) {
		if strings.HasSuffix(file.Path, "/Chart.yaml") {
			if err := validateHelmChart(state, path.Dir(file.Path)); err != nil {
				return err
			}
		}
	}

	return nil
}

// dependencyName extracts the name of a chart dependency entry.
func dependencyName(dep any) (string, bool) {
	entry, ok := dep.(map[string]any)
	if !ok {
		return "", false
	}

	name, ok := entry["name"].(string)
	if !ok || name == "" {
		return "", false
	}

	return name, true
}

// dependencyVendored reports whether a dependency is satisfied by a
// local vendored chart: charts/<name>.tgz, charts/<name>-<version>.tgz,
// or an unpacked charts/<name>/ directory.
func dependencyVendored(state *captureState, chartRel, name string) bool {
	prefix := chartRel + "/charts/"

	for _, file := range state.filesUnder(chartRel + "/charts") {
		rel := strings.TrimPrefix(file.Path, prefix)

		switch {
		case rel == name+".tgz":
			return true
		case strings.HasPrefix(rel, name+"-") && strings.HasSuffix(rel, ".tgz"):
			return true
		case strings.HasPrefix(rel, name+"/"):
			return true
		}
	}

	return false
}

// inspectChartArchive inspects a gzipped tar chart archive entirely in
// memory: entry names must not traverse, entries must be regular files
// or directories, and the chart inside is validated recursively, including
// nested archives, without ever extracting to disk.
func inspectChartArchive(location string, data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return unsafeError("chart archive %s is not readable: %v", location, err)
	}
	defer gz.Close() //nolint:errcheck // read-only reader

	entries := map[string][]byte{}
	tr := tar.NewReader(gz)

	for {
		header, err := tr.Next()
		if err != nil {
			if err == io.EOF { //nolint:errorlint // tar.Reader returns io.EOF directly
				break
			}

			return unsafeError("chart archive %s is not readable: %v", location, err)
		}

		name := path.Clean(header.Name)
		if name == "." {
			continue
		}

		if err := checkArchiveEntryName(location, name); err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
			content, err := io.ReadAll(tr)
			if err != nil {
				return unsafeError("chart archive %s is not readable: %v", location, err)
			}

			entries[name] = content
		default:
			return unsafeError("chart archive %s entry %q is not a regular file", location, name)
		}
	}

	var roots []string
	for name := range entries {
		if path.Base(name) == "Chart.yaml" && strings.Count(name, "/") == 1 {
			roots = append(roots, path.Dir(name))
		}
	}

	if len(roots) == 0 {
		return unsafeError("chart archive %s contains no Chart.yaml", location)
	}

	for _, root := range roots {
		if err := validateArchiveChart(location, entries, root); err != nil {
			return err
		}
	}

	return nil
}

// checkArchiveEntryName rejects absolute and traversing archive entries.
func checkArchiveEntryName(location, name string) error {
	if strings.HasPrefix(name, "/") {
		return unsafeError("chart archive %s has absolute entry %q", location, name)
	}

	if name == ".." || strings.HasPrefix(name, "../") {
		return unsafeError("chart archive %s has traversing entry %q", location, name)
	}

	return nil
}

// validateArchiveChart validates the chart rooted at prefix inside an
// archive's in-memory entries: dependencies must be vendored inside the
// same archive, values schemas are policy-checked, and nested archives
// and unpacked subcharts recurse.
func validateArchiveChart(location string, entries map[string][]byte, prefix string) error {
	chartData, ok := entries[prefix+"/Chart.yaml"]
	if !ok {
		return unsafeError("chart archive %s has no Chart.yaml at %s", location, prefix)
	}

	chart, err := decodeYAMLDocument(chartData)
	if err != nil {
		return unsafeError("invalid Chart.yaml in archive %s at %s: %v", location, prefix, err)
	}

	for name, content := range entries {
		if strings.HasPrefix(name, prefix+"/") && path.Base(name) == "values.schema.json" {
			if err := validateValuesSchema(name, content); err != nil {
				return err
			}
		}
	}

	if deps, ok := chart["dependencies"].([]any); ok {
		for _, dep := range deps {
			name, ok := dependencyName(dep)
			if !ok {
				return unsafeError("chart in archive %s at %s has a malformed dependency entry", location, prefix)
			}

			if !archiveDependencyVendored(entries, prefix, name) {
				return unsafeError(
					"chart in archive %s at %s has dependency %q not vendored inside the archive",
					location, prefix, name)
			}
		}
	}

	chartsPrefix := prefix + "/charts/"

	for name, content := range entries {
		if !strings.HasPrefix(name, chartsPrefix) {
			continue
		}

		rel := strings.TrimPrefix(name, chartsPrefix)

		switch {
		case strings.HasSuffix(rel, ".tgz") && strings.Count(rel, "/") == 0:
			if err := inspectChartArchive(name, content); err != nil {
				return err
			}
		case strings.HasSuffix(rel, "/Chart.yaml") && strings.Count(rel, "/") == 1:
			if err := validateArchiveChart(location, entries, path.Dir(name)); err != nil {
				return err
			}
		}
	}

	return nil
}

// archiveDependencyVendored reports whether a dependency inside an
// archive is satisfied by entries of the same archive.
func archiveDependencyVendored(entries map[string][]byte, prefix, name string) bool {
	chartsPrefix := prefix + "/charts/"

	for entry := range entries {
		if !strings.HasPrefix(entry, chartsPrefix) {
			continue
		}

		rel := strings.TrimPrefix(entry, chartsPrefix)

		switch {
		case rel == name+".tgz":
			return true
		case strings.HasPrefix(rel, name+"-") && strings.HasSuffix(rel, ".tgz") && strings.Count(rel, "/") == 0:
			return true
		case strings.HasPrefix(rel, name+"/"):
			return true
		}
	}

	return false
}

// allowedMetaschemas lists the metaschema identifiers built into the
// schema compiler used by the Helm chart machinery, in both schemes and
// with or without the fragment-only trailing hash.
var allowedMetaschemas = buildAllowedMetaschemas()

func buildAllowedMetaschemas() map[string]struct{} {
	specs := []string{
		"draft-04",
		"draft-06",
		"draft-07",
	}

	allowed := make(map[string]struct{})
	for _, spec := range specs {
		for _, scheme := range []string{"http", "https"} {
			allowed[scheme+"://json-schema.org/"+spec+"/schema"] = struct{}{}
			allowed[scheme+"://json-schema.org/"+spec+"/schema#"] = struct{}{}
		}
	}

	modern := []string{
		"2019-09",
		"2020-12",
	}

	for _, version := range modern {
		for _, scheme := range []string{"https", "http"} {
			allowed[scheme+"://json-schema.org/draft/"+version+"/schema"] = struct{}{}
			allowed[scheme+"://json-schema.org/draft/"+version+"/schema#"] = struct{}{}
		}
	}

	return allowed
}

// validateValuesSchema policy-checks a captured values.schema.json:
// reference keywords must be fragment-only, and the metaschema must be
// one the compiler has built in, so no validation can fetch.
func validateValuesSchema(location string, data []byte) error {
	var document any
	if err := jsonv2.Unmarshal(data, &document); err != nil {
		return unsafeError("values schema %s is not valid JSON: %v", location, err)
	}

	return walkSchemaValue(location, document)
}

// walkSchemaValue recursively checks reference keywords in a decoded
// values schema.
func walkSchemaValue(location string, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for keyword, raw := range typed {
			switch keyword {
			case "$schema":
				identifier, ok := raw.(string)
				if !ok {
					return unsafeError("values schema %s has a malformed $schema", location)
				}

				if _, allowed := allowedMetaschemas[identifier]; !allowed {
					return unsafeError(
						"values schema %s uses %q, which is not a built-in metaschema identifier",
						location, identifier)
				}
			case "$ref", "$dynamicRef", "$recursiveRef":
				reference, ok := raw.(string)
				if !ok {
					return unsafeError("values schema %s has a malformed %s", location, keyword)
				}

				if reference != "#" && !strings.HasPrefix(reference, "#/") {
					return unsafeError(
						"values schema %s uses non-fragment %s %q; external references are never fetched",
						location, keyword, reference)
				}
			}

			if err := walkSchemaValue(location, raw); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := walkSchemaValue(location, item); err != nil {
				return err
			}
		}
	case string, bool, int64, uint64, float64, nil:
		return nil
	default:
		return unsafeError("values schema %s has an unsupported value type %T", location, value)
	}

	return nil
}
