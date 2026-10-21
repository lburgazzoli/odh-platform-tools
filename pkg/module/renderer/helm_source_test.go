package renderer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	. "github.com/onsi/gomega"
)

// Helm chart inputs of the standard module tree.
const (
	chartYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
`

	chartWithVendoredDependencyYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- name: vendored
  version: 1.2.3
  repository: https://charts.example.com
`

	chartWithMissingDependencyYAML = `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- name: absent
  version: 1.0.0
  repository: https://charts.example.com
`

	valuesYAML = `replicas: 1
`

	templateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: module-controller
`

	vendoredChartYAML = `apiVersion: v2
name: vendored
version: 1.2.3
`

	validValuesSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "replicas": {"$ref": "#/$defs/positive"}
  },
  "$defs": {
    "positive": {"type": "integer", "minimum": 1}
  }
}
`
)

// archiveEntry is one entry of a test chart archive.
type archiveEntry struct {
	name     string
	content  string
	typeflag byte
}

// buildArchive builds a gzipped tar archive holding the entries.
func buildArchive(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()

	ordered := slices.Clone(entries)
	slices.SortFunc(ordered, func(a, b archiveEntry) int {
		return strings.Compare(a.name, b.name)
	})

	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)

	for _, entry := range ordered {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}

		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o644,
			Typeflag: typeflag,
		}

		if typeflag == tar.TypeSymlink {
			header.Linkname = entry.content
		} else {
			header.Size = int64(len(entry.content))
		}

		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}

		if typeflag == tar.TypeSymlink {
			continue
		}

		if _, err := tw.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buffer.Bytes()
}

// writeHelmFixture writes the supporting files of the standard Helm
// module tree with a vendored archive dependency.
func writeHelmFixture(t *testing.T, root string) {
	t.Helper()

	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithVendoredDependencyYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/values.schema.json", validValuesSchemaJSON)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", templateYAML)

	archive := buildArchive(t, []archiveEntry{
		{name: "vendored/Chart.yaml", content: vendoredChartYAML},
		{name: "vendored/values.yaml", content: valuesYAML},
		{name: "vendored/values.schema.json", content: validValuesSchemaJSON},
		{name: "vendored/templates/service.yaml", content: templateYAML},
	})
	writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))

	writeModuleFile(t, root, "manifests/chart/.git/config", "unrelated repository metadata")
}

func TestCaptureHelmChartCapturesTree(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmFixture(t, root)

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	// The whole chart tree is captured, including the vendored
	// dependency archive; repository metadata stays inert because the
	// dependency is satisfied locally.
	g.Expect(capturedPaths(snapshot)).To(ConsistOf(
		"manifests/chart/Chart.yaml",
		"manifests/chart/values.yaml",
		"manifests/chart/values.schema.json",
		"manifests/chart/templates/deployment.yaml",
		"manifests/chart/charts/vendored-1.2.3.tgz",
	))

	g.Expect(snapshot.Entry()).To(Equal(filepath.Join(snapshot.Root(), "manifests/chart")))
	g.Expect(snapshotFileContent(t, snapshot, "manifests/chart/templates/deployment.yaml")).To(Equal(templateYAML))
}

func TestCaptureHelmUnpackedSubchart(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- name: unpacked
  repository: file://./charts/unpacked
`)
	writeModuleFile(t, root, "manifests/chart/charts/unpacked/Chart.yaml", vendoredChartYAML)
	writeModuleFile(t, root, "manifests/chart/charts/unpacked/values.schema.json", validValuesSchemaJSON)

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/chart/charts/unpacked/values.schema.json"))
}

func TestCaptureHelmRejectsUnsafeInputs(t *testing.T) {
	t.Parallel()

	badSchema := `{"$ref": "https://example.com/schema.json"}`
	relativeRefSchema := `{"$ref": "shared.json"}`

	tests := []struct {
		name        string
		extra       func(t *testing.T, root string)
		errContains string
	}{
		{
			name: "missing local dependency",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithMissingDependencyYAML)
			},
			errContains: `dependency "absent" is not vendored`,
		},
		{
			name: "malformed dependency entry",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/Chart.yaml", `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- not-an-object
`)
			},
			errContains: "malformed dependency entry",
		},
		{
			name: "external ref in root schema",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/values.schema.json", badSchema)
			},
			errContains: "non-fragment $ref",
		},
		{
			name: "relative file ref in root schema",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/values.schema.json", relativeRefSchema)
			},
			errContains: "non-fragment $ref",
		},
		{
			name: "external dynamic ref in root schema",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/values.schema.json",
					`{"$dynamicRef": "http://example.com/dynamic"}`)
			},
			errContains: "non-fragment $dynamicRef",
		},
		{
			name: "custom metaschema",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/values.schema.json",
					`{"$schema": "https://example.com/my-metaschema.json"}`)
			},
			errContains: "not a built-in metaschema identifier",
		},
		{
			name: "invalid schema json",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/values.schema.json", `{not json`)
			},
			errContains: "not valid JSON",
		},
		{
			name: "external ref in unpacked subchart schema",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/Chart.yaml", `apiVersion: v2
name: synthetic
version: 1.0.0
dependencies:
- name: unpacked
`)
				writeModuleFile(t, root, "manifests/chart/charts/unpacked/Chart.yaml", vendoredChartYAML)
				writeModuleFile(t, root, "manifests/chart/charts/unpacked/values.schema.json", badSchema)
			},
			errContains: "non-fragment $ref",
		},
		{
			name: "external ref inside chart archive",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithVendoredDependencyYAML)
				archive := buildArchive(t, []archiveEntry{
					{name: "vendored/Chart.yaml", content: vendoredChartYAML},
					{name: "vendored/values.schema.json", content: badSchema},
				})
				writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))
			},
			errContains: "non-fragment $ref",
		},
		{
			name: "traversal entry inside chart archive",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithVendoredDependencyYAML)
				archive := buildArchive(t, []archiveEntry{
					{name: "vendored/Chart.yaml", content: vendoredChartYAML},
					{name: "../evil.txt", content: "escaping content"},
				})
				writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))
			},
			errContains: "traversing entry",
		},
		{
			name: "absolute entry inside chart archive",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithVendoredDependencyYAML)
				archive := buildArchive(t, []archiveEntry{
					{name: "vendored/Chart.yaml", content: vendoredChartYAML},
					{name: "/etc/passwd", content: "escaping content"},
				})
				writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))
			},
			errContains: "absolute entry",
		},
		{
			name: "symlink entry inside chart archive",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithVendoredDependencyYAML)
				archive := buildArchive(t, []archiveEntry{
					{name: "vendored/Chart.yaml", content: vendoredChartYAML},
					{name: "vendored/link", content: "../../../etc/passwd", typeflag: tar.TypeSymlink},
				})
				writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))
			},
			errContains: "is not a regular file",
		},
		{
			name: "archive without chart.yaml",
			extra: func(t *testing.T, root string) {
				writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithVendoredDependencyYAML)
				archive := buildArchive(t, []archiveEntry{
					{name: "vendored/values.yaml", content: valuesYAML},
				})
				writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(archive))
			},
			errContains: "contains no Chart.yaml",
		},
		{
			name: "chart file symlink outside root",
			extra: func(t *testing.T, root string) {
				outside := filepath.Join(filepath.Dir(root), "outside")
				if err := os.MkdirAll(outside, 0o755); err != nil {
					t.Fatal(err)
				}
				outsideFile := filepath.Join(outside, "deployment.yaml")
				if err := os.WriteFile(outsideFile, []byte(templateYAML), 0o644); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(root, "manifests/chart/templates/deployment.yaml")
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outsideFile, target); err != nil {
					t.Fatal(err)
				}
			},
			errContains: "resolves outside module root",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			root := t.TempDir()
			writeHelmFixture(t, root)
			if test.extra != nil {
				test.extra(t, root)
			}

			snapshot, err := capture(t, root)
			g.Expect(err).To(HaveOccurred())
			g.Expect(snapshot).To(BeNil())
			g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeTrue())
			g.Expect(err.Error()).To(ContainSubstring(test.errContains))
		})
	}
}

func TestCaptureHelmMissingChartDirectory(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmFixture(t, root)

	// The loader validated the entry when it existed; the chart tree
	// disappears before capture runs.
	document := loadDefinition(t, root)

	if err := os.RemoveAll(filepath.Join(root, "manifests/chart")); err != nil {
		t.Fatal(err)
	}

	snapshot, err := Capture(t.Context(), document)
	g.Expect(err).To(HaveOccurred())
	g.Expect(snapshot).To(BeNil())
	g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("missing chart directory"))
}

func TestCaptureHelmAcceptsFragmentRefsAndBuiltinMetaschemas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema string
	}{
		{
			name:   "root reference",
			schema: `{"$ref": "#"}`,
		},
		{
			name:   "draft-07 metaschema",
			schema: `{"$schema": "http://json-schema.org/draft-07/schema#", "$ref": "#/$defs/x", "$defs": {"x": {}}}`,
		},
		{
			name:   "2019-09 metaschema",
			schema: `{"$schema": "https://json-schema.org/draft/2019-09/schema", "$recursiveRef": "#"}`,
		},
		{
			name:   "nested non-fragment-free values are data",
			schema: `{"properties": {"url": {"type": "string", "examples": ["https://example.com"]}}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			root := t.TempDir()
			writeHelmFixture(t, root)
			writeModuleFile(t, root, "manifests/chart/values.schema.json", test.schema)

			snapshot, err := capture(t, root)
			g.Expect(err).NotTo(HaveOccurred())
			defer snapshot.Close() //nolint:errcheck // test cleanup
		})
	}
}

func TestCaptureHelmNestedArchiveDependency(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithVendoredDependencyYAML)

	// The vendored archive itself vendors a nested dependency, declared
	// in its Chart.yaml and satisfied by a nested archive.
	nested := buildArchive(t, []archiveEntry{
		{name: "nested/Chart.yaml", content: `apiVersion: v2
name: nested
version: 0.1.0
`},
	})
	vendored := buildArchive(t, []archiveEntry{
		{name: "vendored/Chart.yaml", content: `apiVersion: v2
name: vendored
version: 1.2.3
dependencies:
- name: nested
  version: 0.1.0
`},
		{name: "vendored/charts/nested-0.1.0.tgz", content: string(nested)},
	})
	writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(vendored))

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup
}

func TestCaptureHelmRejectsMissingNestedArchiveDependency(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartWithVendoredDependencyYAML)

	// The vendored archive declares a dependency nothing inside it
	// satisfies.
	vendored := buildArchive(t, []archiveEntry{
		{name: "vendored/Chart.yaml", content: `apiVersion: v2
name: vendored
version: 1.2.3
dependencies:
- name: absent
  version: 0.1.0
`},
	})
	writeModuleFile(t, root, "manifests/chart/charts/vendored-1.2.3.tgz", string(vendored))

	snapshot, err := capture(t, root)
	g.Expect(err).To(HaveOccurred())
	g.Expect(snapshot).To(BeNil())
	g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring(`dependency "absent" not vendored inside the archive`))
}

func TestCaptureHelmChartSymlinkedDirectoryInsideRoot(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmFixture(t, root)

	// A chart directory reached through a contained symlink: the link
	// is preserved and the real directory's files are captured.
	chartDir := filepath.Join(root, "manifests/chart")
	writeModuleFile(t, root, "manifests/real-templates/deployment.yaml", templateYAML)
	if err := os.Symlink("../real-templates", filepath.Join(chartDir, "templates-link")); err != nil {
		t.Fatal(err)
	}

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/chart/templates-link"))
	g.Expect(capturedPaths(snapshot)).To(ContainElement("manifests/real-templates/deployment.yaml"))
	g.Expect(snapshotHasLink(t, snapshot,
		"manifests/chart/templates-link",
		"../real-templates")).To(BeTrue())
}

func TestCaptureHelmChartSymlinkEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		extra       func(t *testing.T, root string)
		errContains string
	}{
		{
			name: "broken symlink inside chart tree",
			extra: func(t *testing.T, root string) {
				chartDir := filepath.Join(root, "manifests/chart")
				if err := os.Symlink("nowhere.yaml", filepath.Join(chartDir, "templates/broken.yaml")); err != nil {
					t.Fatal(err)
				}
			},
			errContains: "broken symlink",
		},
		{
			name: "special file inside chart tree",
			extra: func(t *testing.T, root string) {
				chartDir := filepath.Join(root, "manifests/chart")
				if err := syscall.Mkfifo(filepath.Join(chartDir, "templates/pipe"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			errContains: "is not a regular file",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			root := t.TempDir()
			writeHelmFixture(t, root)
			test.extra(t, root)

			snapshot, err := capture(t, root)
			g.Expect(err).To(HaveOccurred())
			g.Expect(snapshot).To(BeNil())
			g.Expect(errors.Is(err, ErrUnsafeSource)).To(BeTrue())
			g.Expect(err.Error()).To(ContainSubstring(test.errContains))
		})
	}
}

func TestCaptureHelmIgnoresOriginalsChangedAfterCapture(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeHelmFixture(t, root)

	snapshot, err := capture(t, root)
	g.Expect(err).NotTo(HaveOccurred())
	defer snapshot.Close() //nolint:errcheck // test cleanup

	// Rewrite the originals after capture: the snapshot bytes and the
	// snapshot chart archive are unaffected.
	writeModuleFile(t, root, "manifests/chart/values.yaml", "replicas: 99\n")
	writeModuleFile(t, root, "manifests/chart/values.schema.json", `{"$ref": "https://evil.example.com"}`)

	g.Expect(snapshotFileContent(t, snapshot, "manifests/chart/values.yaml")).To(Equal(valuesYAML))
	g.Expect(snapshotFileContent(t, snapshot, "manifests/chart/values.schema.json")).To(Equal(validValuesSchemaJSON))
}
