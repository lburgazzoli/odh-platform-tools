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

package images

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

const sourcesYAML = `# Related images for the synthetic module.
relatedImages:
- name: RELATED_IMAGE_CONTROLLER
  value: "quay.io/example/controller:v1"
- name: RELATED_IMAGE_OPERAND
  value: 'quay.io/example/operand:v2'
- name: RELATED_IMAGE_UNQUOTED
  value: quay.io/example/operand-bare:v3
- name: RELATED_IMAGE_UNREQUESTED
  value: quay.io/example/unrequested:v9
`

const caseDistinctSourcesYAML = `relatedImages:
- name: RELATED_IMAGE_UPPER
  value: file-upper
- name: related_image_upper
  value: file-lower
`

const literalSourcesYAML = `relatedImages:
- name: RELATED_IMAGE_DOUBLE_QUOTED
  value: "${SYNTHETIC_AMBIENT_BASE}/double:v1"
- name: RELATED_IMAGE_SINGLE_QUOTED
  value: '${SYNTHETIC_AMBIENT_BASE}/single:v1'
`

const unsetValuesYAML = `relatedImages:
- name: SOURCE_WITHOUT_VALUE
- name: SOURCE_WITH_NULL_VALUE
  value: null
- name: SOURCE_WITH_EMPTY_VALUE
  value: ""
`

const malformedSourcesYAML = `relatedImages:
- name: RELATED_IMAGE_CONTROLLER
  value: [private-image-fixture
`

const undecodableSourcesYAML = `relatedImages:
- name: RELATED_IMAGE_CONTROLLER
  value: {private-image-fixture: opaque}
`

const oldDotenv = `RELATED_IMAGE_CONTROLLER=quay.io/example/controller:v1
`

const initSourcesYAML = `relatedImages:
- name: INIT_IMAGE
  value: registry.example/init:file
`

func TestRelatedImagesConfigMarshalsToTheDocument(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	file := api.RelatedImagesConfig{
		RelatedImages: []api.RelatedImage{{Name: "INIT_IMAGE", Value: "registry.example/init:file"}},
	}

	g.Expect(yaml.Marshal(file)).To(MatchYAML(initSourcesYAML))
}

// writeSources writes content to a file with the given name inside a
// fresh directory and returns the absolute path.
func writeSources(t *testing.T, name string, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

// unsetEnv removes the named variables from the process environment
// for the duration of the test, restoring the previous state
// afterwards, so file-only resolution is deterministic regardless of
// the ambient environment.
func unsetEnv(t *testing.T, names ...string) {
	t.Helper()

	for _, name := range names {
		previous, present := os.LookupEnv(name)
		if present {
			t.Cleanup(func() {
				if err := os.Setenv(name, previous); err != nil {
					t.Errorf("restore %s: %v", name, err)
				}
			})
		} else {
			t.Cleanup(func() {
				if err := os.Unsetenv(name); err != nil {
					t.Errorf("unset %s: %v", name, err)
				}
			})
		}

		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadEnvOnly(t *testing.T) {
	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/example/controller:v1")
	t.Setenv("Related_Image_Mixed_Case", "quay.io/example/mixed:v1")

	g := NewWithT(t)

	// An empty path means environment-only resolution; the authored
	// key spelling is preserved and missing keys are omitted.
	values, err := Load("", []string{
		"RELATED_IMAGE_CONTROLLER",
		"RELATED_IMAGE_MISSING",
		"Related_Image_Mixed_Case",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(Equal(map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
		"Related_Image_Mixed_Case": "quay.io/example/mixed:v1",
	}))
}

func TestLoadFileOnly(t *testing.T) {
	unsetEnv(t,
		"RELATED_IMAGE_CONTROLLER",
		"RELATED_IMAGE_OPERAND",
		"RELATED_IMAGE_UNQUOTED",
		"RELATED_IMAGE_ABSENT")

	g := NewWithT(t)

	// YAML is selected explicitly, regardless of the file-name extension.
	values, err := Load(writeSources(t, "sources.conf", sourcesYAML), []string{
		"RELATED_IMAGE_CONTROLLER",
		"RELATED_IMAGE_OPERAND",
		"RELATED_IMAGE_UNQUOTED",
		"RELATED_IMAGE_ABSENT",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(Equal(map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
		"RELATED_IMAGE_OPERAND":    "quay.io/example/operand:v2",
		"RELATED_IMAGE_UNQUOTED":   "quay.io/example/operand-bare:v3",
	}))
}

func TestLoadAcceptsNonUTF8LinuxFilename(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux filesystem semantics")
	}

	unsetEnv(t, "INIT_IMAGE")

	g := NewWithT(t)
	path := writeSources(t, "images-\xff.yaml", initSourcesYAML)

	g.Expect(Load(path, []string{"INIT_IMAGE"})).To(Equal(map[string]string{
		"INIT_IMAGE": "registry.example/init:file",
	}))
}

func TestLoadEnvOverridesFile(t *testing.T) {
	unsetEnv(t, "RELATED_IMAGE_OPERAND")
	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/example/controller:env")

	g := NewWithT(t)

	values, err := Load(writeSources(t, "sources.yaml", sourcesYAML), []string{
		"RELATED_IMAGE_CONTROLLER",
		"RELATED_IMAGE_OPERAND",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(Equal(map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:env",
		"RELATED_IMAGE_OPERAND":    "quay.io/example/operand:v2",
	}))
}

func TestLoadExplicitEmptyEnvMasksFileValue(t *testing.T) {
	t.Setenv("RELATED_IMAGE_CONTROLLER", "")

	g := NewWithT(t)

	// An explicitly empty environment variable masks the file value
	// and counts as missing, so the key is omitted entirely.
	values, err := Load(writeSources(t, "sources.yaml", sourcesYAML), []string{
		"RELATED_IMAGE_CONTROLLER",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(BeEmpty())
}

func TestLoadCaseCollidingKeysDoNotAlias(t *testing.T) {
	t.Setenv("RELATED_IMAGE_UPPER", "env-upper")
	t.Setenv("related_image_upper", "env-lower")

	// Each spelling has its own value regardless of request order.
	orders := [][]string{
		{"RELATED_IMAGE_UPPER", "related_image_upper"},
		{"related_image_upper", "RELATED_IMAGE_UPPER"},
	}

	for _, keys := range orders {
		t.Run("", func(t *testing.T) {
			g := NewWithT(t)

			values, err := Load(writeSources(t, "sources.yaml", caseDistinctSourcesYAML), keys)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(values).To(Equal(map[string]string{
				"RELATED_IMAGE_UPPER": "env-upper",
				"related_image_upper": "env-lower",
			}))
		})
	}
}

func TestLoadFileKeysUseExactNames(t *testing.T) {
	unsetEnv(t, "RELATED_IMAGE_UPPER", "related_image_upper", "Related_Image_Upper")

	g := NewWithT(t)

	values, err := Load(writeSources(t, "sources.yaml", caseDistinctSourcesYAML), []string{
		"RELATED_IMAGE_UPPER",
		"related_image_upper",
		"Related_Image_Upper",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(Equal(map[string]string{
		"RELATED_IMAGE_UPPER": "file-upper",
		"related_image_upper": "file-lower",
	}))
}

func TestLoadScopesToRequestedKeys(t *testing.T) {
	unsetEnv(t, "RELATED_IMAGE_CONTROLLER")
	t.Setenv("SYNTHETIC_UNREQUESTED_ENV", "ambient")

	g := NewWithT(t)

	// The result includes only requested keys.
	values, err := Load(writeSources(t, "sources.yaml", sourcesYAML), []string{
		"RELATED_IMAGE_CONTROLLER",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(Equal(map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
	}))
}

func TestLoadKeepsYAMLValuesLiteral(t *testing.T) {
	unsetEnv(t, "RELATED_IMAGE_DOUBLE_QUOTED", "RELATED_IMAGE_SINGLE_QUOTED")
	t.Setenv("SYNTHETIC_AMBIENT_BASE", "quay.io/example")

	g := NewWithT(t)

	values, err := Load(writeSources(t, "sources.yaml", literalSourcesYAML), []string{
		"RELATED_IMAGE_DOUBLE_QUOTED",
		"RELATED_IMAGE_SINGLE_QUOTED",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(Equal(map[string]string{
		"RELATED_IMAGE_DOUBLE_QUOTED": "${SYNTHETIC_AMBIENT_BASE}/double:v1",
		"RELATED_IMAGE_SINGLE_QUOTED": "${SYNTHETIC_AMBIENT_BASE}/single:v1",
	}))
}

func TestLoadEmptyEntryValuesCanBeSuppliedByEnv(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "missing value", key: "SOURCE_WITHOUT_VALUE"},
		{name: "null value", key: "SOURCE_WITH_NULL_VALUE"},
		{name: "empty string", key: "SOURCE_WITH_EMPTY_VALUE"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)
			unsetEnv(t, test.key)
			path := writeSources(t, "sources.yaml", unsetValuesYAML)
			keys := []string{test.key}

			g.Expect(Load(path, keys)).To(BeEmpty())

			t.Setenv(test.key, "quay.io/example/controller:env")
			g.Expect(Load(path, keys)).To(Equal(map[string]string{
				test.key: "quay.io/example/controller:env",
			}))
		})
	}
}

func TestLoadFilesWithoutEntriesPreserveEnvResolution(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty file"},
		{name: "comments only", content: "# No sources.\n"},
		{name: "missing list", content: "{}\n"},
		{name: "null list", content: "relatedImages: null\n"},
		{name: "empty list", content: "relatedImages: []\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)
			unsetEnv(t, "SOURCE_WITHOUT_VALUE")
			path := writeSources(t, "sources.yaml", test.content)
			keys := []string{"SOURCE_WITHOUT_VALUE"}

			g.Expect(Load(path, keys)).To(BeEmpty())

			t.Setenv("SOURCE_WITHOUT_VALUE", "quay.io/example/controller:env")
			g.Expect(Load(path, keys)).To(Equal(map[string]string{
				"SOURCE_WITHOUT_VALUE": "quay.io/example/controller:env",
			}))
		})
	}
}

func TestLoadReturnsFreshStateForEachCall(t *testing.T) {
	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/example/controller:v1")

	g := NewWithT(t)

	first, err := Load("", []string{"RELATED_IMAGE_CONTROLLER"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(first).To(Equal(map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
	}))

	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/example/controller:v2")

	second, err := Load("", []string{"RELATED_IMAGE_CONTROLLER"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(second).To(Equal(map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v2",
	}))
}

func TestLoadMissingFileIsAnInputError(t *testing.T) {
	g := NewWithT(t)

	path := filepath.Join(t.TempDir(), "absent.yaml")

	_, err := Load(path, []string{"RELATED_IMAGE_CONTROLLER"})

	g.Expect(err).To(MatchError(moduleerrors.ErrImageSource))
	g.Expect(err).To(MatchError(fs.ErrNotExist))
	g.Expect(err).To(MatchError(Equal("invalid related images file " + path)))
}

func TestLoadRejectsInvalidFilesWithSanitizedErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		keys    []string
	}{
		{name: "dotenv assignments", content: oldDotenv, keys: []string{"RELATED_IMAGE_CONTROLLER"}},
		{name: "invalid syntax", content: malformedSourcesYAML},
		{name: "invalid value type", content: undecodableSourcesYAML},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)
			path := writeSources(t, "sources.yaml", test.content)

			values, err := Load(path, test.keys)

			g.Expect(values).To(BeNil())
			g.Expect(err).To(MatchError(moduleerrors.ErrImageSource))
			g.Expect(err).To(MatchError(Equal("invalid related images file " + path)))
			g.Expect(errors.Unwrap(err)).NotTo(BeNil())
		})
	}
}

func TestImageSourceKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		image string
		want  string
		ok    bool
	}{
		{name: "complete token", image: "{{RELATED_IMAGE_CONTROLLER}}", want: "RELATED_IMAGE_CONTROLLER", ok: true},
		{name: "single letter name", image: "{{X}}", want: "X", ok: true},
		{name: "lowercase name", image: "{{related_image_controller}}", want: "related_image_controller", ok: true},
		{name: "leading underscore name", image: "{{_PRIVATE}}", want: "_PRIVATE", ok: true},
		{name: "empty image", image: ""},
		{name: "literal image", image: "quay.io/example/controller:v1"},
		{name: "trailing text", image: "{{X}}-suffix"},
		{name: "leading text", image: "prefix-{{X}}"},
		{name: "inner whitespace", image: "{{ X }}"},
		{name: "non-portable name", image: "{{X-Y}}"},
		{name: "unclosed token", image: "{{X"},
		{name: "unopened token", image: "X}}"},
		{name: "shell reference", image: "${X}"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			key, ok := ImageSourceKey(test.image)
			g.Expect(ok).To(Equal(test.ok))
			g.Expect(key).To(Equal(test.want))
		})
	}
}

func TestResolveImage(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
		"RELATED_IMAGE_EMPTY":      "",
		"RELATED_IMAGE_NESTED":     "{{RELATED_IMAGE_CONTROLLER}}",
	}

	tests := []struct {
		name  string
		image string
		want  string
	}{
		{name: "token resolves", image: "{{RELATED_IMAGE_CONTROLLER}}", want: "quay.io/example/controller:v1"},
		{name: "missing token resolves empty", image: "{{RELATED_IMAGE_MISSING}}", want: ""},
		{name: "empty value resolves empty", image: "{{RELATED_IMAGE_EMPTY}}", want: ""},
		{name: "literal image passes through", image: "quay.io/example/controller:v1", want: "quay.io/example/controller:v1"},
		{name: "empty image stays empty", image: "", want: ""},
		{name: "resolution is not recursive", image: "{{RELATED_IMAGE_NESTED}}", want: "{{RELATED_IMAGE_CONTROLLER}}"},
		{name: "partial token is literal", image: "{{RELATED_IMAGE_CONTROLLER}}-suffix", want: "{{RELATED_IMAGE_CONTROLLER}}-suffix"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			g.Expect(ResolveImage(test.image, values)).To(Equal(test.want))
		})
	}
}

// snapshotDefinition builds one definition with the given related
// images and controller container image, matching the deployer's
// authored-definition shape. An empty image means no container block.
func snapshotDefinition(relatedImages []string, image string) toolApi.PlatformModuleDefinition {
	definition := toolApi.PlatformModuleDefinition{
		Spec: toolApi.PlatformModuleDefinitionSpec{
			RelatedImages: relatedImages,
		},
	}
	if image != "" {
		definition.Spec.Controller.Deployment.Container = &toolApi.ContainerDefinition{Image: image}
	}

	return definition
}

func TestSnapshotInitImageSources(t *testing.T) {
	unsetEnv(t, "INIT_IMAGE")
	g := NewWithT(t)
	definition := snapshotDefinition(nil, "controller:literal")
	definition.Spec.Controller.Deployment.InitContainer = &toolApi.ContainerTarget{Image: "{{INIT_IMAGE}}"}
	path := writeSources(t, "sources.yaml", initSourcesYAML)
	g.Expect(Snapshot(path, definition)).To(Equal(map[string]string{"INIT_IMAGE": "registry.example/init:file"}))
	t.Setenv("INIT_IMAGE", "registry.example/init:env")
	g.Expect(Snapshot(path, definition)).To(Equal(map[string]string{"INIT_IMAGE": "registry.example/init:env"}))
	t.Setenv("INIT_IMAGE", "")
	g.Expect(Snapshot(path, definition)).To(BeEmpty())
	definition.Spec.Controller.Deployment.InitContainer.Image = ""
	g.Expect(Snapshot(path, definition)).To(BeEmpty())
	g.Expect(definition.Spec.RelatedImages).To(BeEmpty())
}

// TestSnapshotSelectsDeclaredAndContainerKeys proves the snapshot
// requests exactly the declared related images plus the main
// container's whole-token source key: every requested source resolves
// and no other file or ambient entry leaks in.
func TestSnapshotSelectsDeclaredAndContainerKeys(t *testing.T) {
	unsetEnv(t,
		"RELATED_IMAGE_OPERAND",
		"RELATED_IMAGE_CONTROLLER",
		"RELATED_IMAGE_UNREQUESTED")

	g := NewWithT(t)

	path := writeSources(t, "sources.yaml", sourcesYAML)

	definition := snapshotDefinition(
		[]string{"RELATED_IMAGE_OPERAND"},
		"{{RELATED_IMAGE_CONTROLLER}}")

	values, err := Snapshot(path, definition)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(Equal(map[string]string{
		"RELATED_IMAGE_OPERAND":    "quay.io/example/operand:v2",
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
	}))
}

// TestSnapshotContainerCases proves the container contributes a key
// only when it exists and its image is a whole-token reference: an
// absent container block and a literal image both snapshot only the
// declared related images.
func TestSnapshotContainerCases(t *testing.T) {
	unsetEnv(t, "RELATED_IMAGE_OPERAND")

	tests := []struct {
		name       string
		definition toolApi.PlatformModuleDefinition
	}{
		{
			name: "literal container image adds no key",
			definition: snapshotDefinition(
				[]string{"RELATED_IMAGE_OPERAND"},
				"quay.io/example/controller:v1"),
		},
		{
			name: "absent container adds no key",
			definition: toolApi.PlatformModuleDefinition{Spec: toolApi.PlatformModuleDefinitionSpec{
				RelatedImages: []string{"RELATED_IMAGE_OPERAND"},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)

			values, err := Snapshot(writeSources(t, "sources.yaml", sourcesYAML), test.definition)

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(values).To(Equal(map[string]string{
				"RELATED_IMAGE_OPERAND": "quay.io/example/operand:v2",
			}))
		})
	}
}
