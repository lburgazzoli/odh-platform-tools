package images

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// sourcesDotenv is a dotenv file mixing quoting styles and comments.
const sourcesDotenv = `# related images for the synthetic module
RELATED_IMAGE_CONTROLLER="quay.io/example/controller:v1"
RELATED_IMAGE_OPERAND='quay.io/example/operand:v2'
RELATED_IMAGE_UNQUOTED=quay.io/example/operand-bare:v3
`

// collidingDotenv is a dotenv file whose key collides with the
// case-colliding test keys.
const collidingDotenv = `RELATED_IMAGE_UPPER=file-upper
`

// ambientExpandingDotenv is a dotenv file whose double-quoted value
// expands a variable only the process environment provides, while
// the single-quoted value stays literal.
const ambientExpandingDotenv = `RELATED_IMAGE_EXPANDED="${SYNTHETIC_AMBIENT_BASE}/expanded:v1"
RELATED_IMAGE_LITERAL='${SYNTHETIC_AMBIENT_BASE}/literal:v1'
`

// malformedDotenv is a dotenv file whose second line is not an
// assignment.
const malformedDotenv = `RELATED_IMAGE_CONTROLLER=quay.io/example/controller:v1
THIS LINE IS NOT AN ASSIGNMENT
`

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

	// An arbitrary file-name extension works because the dotenv
	// format is selected explicitly; quoting styles and comments keep
	// their native semantics.
	values, err := Load(writeSources(t, "sources.conf", sourcesDotenv), []string{
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

func TestLoadEnvOverridesFile(t *testing.T) {
	unsetEnv(t, "RELATED_IMAGE_OPERAND")
	t.Setenv("RELATED_IMAGE_CONTROLLER", "quay.io/example/controller:env")

	g := NewWithT(t)

	values, err := Load(writeSources(t, "sources.env", sourcesDotenv), []string{
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
	values, err := Load(writeSources(t, "sources.env", sourcesDotenv), []string{
		"RELATED_IMAGE_CONTROLLER",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(BeEmpty())
}

func TestLoadCaseCollidingKeysDoNotAlias(t *testing.T) {
	t.Setenv("RELATED_IMAGE_UPPER", "env-upper")
	t.Setenv("related_image_upper", "env-lower")

	// The two spellings are distinct environment variables bound
	// exactly and case-sensitively; the result must not depend on the
	// enumeration order.
	orders := [][]string{
		{"RELATED_IMAGE_UPPER", "related_image_upper"},
		{"related_image_upper", "RELATED_IMAGE_UPPER"},
	}

	for _, keys := range orders {
		t.Run("", func(t *testing.T) {
			g := NewWithT(t)

			values, err := Load(writeSources(t, "sources.env", collidingDotenv), keys)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(values).To(Equal(map[string]string{
				"RELATED_IMAGE_UPPER": "env-upper",
				"related_image_upper": "env-lower",
			}))
		})
	}
}

func TestLoadFileKeysAreCaseInsensitive(t *testing.T) {
	unsetEnv(t, "RELATED_IMAGE_UPPER", "related_image_upper")

	g := NewWithT(t)

	// File keys follow Viper's case-insensitive rules; the result
	// still carries the authored spelling of the request.
	values, err := Load(writeSources(t, "sources.env", collidingDotenv), []string{
		"related_image_upper",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(Equal(map[string]string{
		"related_image_upper": "file-upper",
	}))
}

func TestLoadScopesToRequestedKeys(t *testing.T) {
	unsetEnv(t, "RELATED_IMAGE_CONTROLLER")
	t.Setenv("SYNTHETIC_UNREQUESTED_ENV", "ambient")

	g := NewWithT(t)

	// Only the declared keys are looked up, by direct binding rather
	// than settings enumeration: unused file and ambient entries are
	// neither returned nor injected.
	values, err := Load(writeSources(t, "sources.env", sourcesDotenv), []string{
		"RELATED_IMAGE_CONTROLLER",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(Equal(map[string]string{
		"RELATED_IMAGE_CONTROLLER": "quay.io/example/controller:v1",
	}))
}

func TestLoadNativeDotenvExpansionReadsAmbientVariables(t *testing.T) {
	t.Setenv("SYNTHETIC_AMBIENT_BASE", "quay.io/example")

	g := NewWithT(t)

	// Native dotenv expansion resolves double-quoted references from
	// the process environment, while single-quoted values stay
	// literal. This documents upstream behavior, not isolation.
	values, err := Load(writeSources(t, "sources.env", ambientExpandingDotenv), []string{
		"RELATED_IMAGE_EXPANDED",
		"RELATED_IMAGE_LITERAL",
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values).To(Equal(map[string]string{
		"RELATED_IMAGE_EXPANDED": "quay.io/example/expanded:v1",
		"RELATED_IMAGE_LITERAL":  "${SYNTHETIC_AMBIENT_BASE}/literal:v1",
	}))
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

	path := filepath.Join(t.TempDir(), "absent.env")

	_, err := Load(path, []string{"RELATED_IMAGE_CONTROLLER"})

	var inputErr *moduleerrors.ModuleError
	g.Expect(errors.As(err, &inputErr)).To(BeTrue())
	g.Expect(inputErr.Error()).To(Equal("invalid related images file " + path))
	g.Expect(errors.Is(err, moduleerrors.ErrImageSource)).To(BeTrue())
	g.Expect(errors.Is(err, fs.ErrNotExist)).To(BeTrue())
}

func TestLoadMalformedFileHidesContent(t *testing.T) {
	g := NewWithT(t)

	path := writeSources(t, "sources.env", malformedDotenv)

	_, err := Load(path, []string{
		"RELATED_IMAGE_CONTROLLER",
	})

	var inputErr *moduleerrors.ModuleError
	g.Expect(errors.As(err, &inputErr)).To(BeTrue())

	// The public text names the file and never echoes the offending
	// line, while the cause retains it for callers that need the
	// details.
	g.Expect(inputErr.Error()).To(Equal("invalid related images file " + path))
	g.Expect(inputErr.Error()).To(SatisfyAll(
		Not(ContainSubstring("THIS LINE IS NOT AN ASSIGNMENT")),
		Not(ContainSubstring("doesn't match format")),
	))

	cause := errors.Unwrap(inputErr)
	g.Expect(cause).NotTo(BeNil())
	g.Expect(cause.Error()).To(ContainSubstring("THIS LINE IS NOT AN ASSIGNMENT"))
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
	path := writeSources(t, "images.env", "INIT_IMAGE=registry.example/init:file\n")
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

	path := writeSources(t, "sources.env", sourcesDotenv+"\n"+
		"RELATED_IMAGE_UNREQUESTED=quay.io/example/unrequested:v9\n")

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

			values, err := Snapshot(writeSources(t, "sources.env", sourcesDotenv), test.definition)

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(values).To(Equal(map[string]string{
				"RELATED_IMAGE_OPERAND": "quay.io/example/operand:v2",
			}))
		})
	}
}
