package renderer

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	. "github.com/onsi/gomega"
)

func TestResolveEntry(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	const root = "/modules/synthetic"

	// An authored relative path resolves against the definition
	// file's directory; an empty path — an omitted block or an
	// omitted path — resolves to the directory itself, the
	// co-located layout; an absolute path is honored as is.
	g.Expect(ResolveEntry(root, "./manifests/chart")).To(Equal("/modules/synthetic/manifests/chart"))
	g.Expect(ResolveEntry(root, "manifests/chart")).To(Equal("/modules/synthetic/manifests/chart"))
	g.Expect(ResolveEntry(root, "")).To(Equal("/modules/synthetic"))
	g.Expect(ResolveEntry(root, "/elsewhere/chart")).To(Equal("/elsewhere/chart"))
	g.Expect(ResolveEntry(root, "/elsewhere/chart/")).To(Equal("/elsewhere/chart"))
}

func TestDecodeHelmValues(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	values := map[string]apiextensionsv1.JSON{
		"scalar":      {Raw: []byte("2")},
		"string":      {Raw: []byte(`"literal"`)},
		"nested":      {Raw: []byte(`{"tag":"v1.0.0"}`)},
		"list":        {Raw: []byte(`["a","b"]`)},
		"boolean":     {Raw: []byte("true")},
		"nullValue":   {}, // apiextensions JSON represents null with empty Raw.
		"placeholder": {Raw: []byte(`"{{RELATED_IMAGE_EXAMPLE_CONTROLLER}}"`)},
	}

	decoded, err := DecodeHelmValues(values)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(decoded).To(SatisfyAll(
		HaveKeyWithValue("scalar", float64(2)),
		HaveKeyWithValue("string", "literal"),
		HaveKeyWithValue("nested", map[string]any{"tag": "v1.0.0"}),
		HaveKeyWithValue("list", []any{"a", "b"}),
		HaveKeyWithValue("boolean", true),
		// Empty Raw is JSON null, not malformed content: it decodes
		// as a nil value instead of failing with EOF.
		HaveKeyWithValue("nullValue", BeNil()),
		// Placeholder-looking text stays literal.
		HaveKeyWithValue("placeholder", "{{RELATED_IMAGE_EXAMPLE_CONTROLLER}}"),
	))

	// The source values are not mutated.
	g.Expect(values["nullValue"].Raw).To(BeEmpty())
	g.Expect(values["scalar"].Raw).To(Equal([]byte("2")))
}

func TestDecodeHelmValuesRejectsMalformedEntry(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	values := map[string]apiextensionsv1.JSON{
		"malformed": {Raw: []byte(`{"tag":`)},
	}

	decoded, err := DecodeHelmValues(values)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(`helm value "malformed"`))
	g.Expect(decoded).To(BeNil())
}
