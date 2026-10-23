package images

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
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

	var inputErr *ImageSourceError
	g.Expect(errors.As(err, &inputErr)).To(BeTrue())
	g.Expect(inputErr.Error()).To(ContainSubstring(path))
	g.Expect(errors.Is(err, fs.ErrNotExist)).To(BeTrue())
}

func TestLoadMalformedFileHidesContent(t *testing.T) {
	g := NewWithT(t)

	_, err := Load(writeSources(t, "sources.env", malformedDotenv), []string{
		"RELATED_IMAGE_CONTROLLER",
	})

	var inputErr *ImageSourceError
	g.Expect(errors.As(err, &inputErr)).To(BeTrue())

	// The public text never echoes the offending line, while the
	// cause retains it for callers that need the details.
	g.Expect(inputErr.Error()).To(SatisfyAll(
		Not(ContainSubstring("THIS LINE IS NOT AN ASSIGNMENT")),
		Not(ContainSubstring("doesn't match format")),
	))

	cause := errors.Unwrap(inputErr)
	g.Expect(cause).NotTo(BeNil())
	g.Expect(cause.Error()).To(ContainSubstring("THIS LINE IS NOT AN ASSIGNMENT"))
}
