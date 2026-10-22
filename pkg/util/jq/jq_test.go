package jq

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"
)

// The CRD-like fixture Matchesf runs against: nested objects, JSON
// numbers as decoded by the YAML machinery, and arrays.
func crdLikeObject() map[string]any {
	return map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "synthetics.components.platform.opendatahub.io"},
		"spec": map[string]any{
			"group": "components.platform.opendatahub.io",
			"names": map[string]any{
				"kind":   "SyntheticModule",
				"plural": "synthetics",
			},
			"versions": []any{
				map[string]any{"name": "v1alpha1", "served": true},
			},
		},
	}
}

func TestMatchesf(t *testing.T) {
	t.Parallel()

	t.Run("nested equality with quoted interpolation", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		matches, err := Matchesf(crdLikeObject(),
			`.spec.group == %q and .spec.names.kind == %q`,
			"components.platform.opendatahub.io", "SyntheticModule")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(matches).To(BeTrue())
	})

	t.Run("mismatch is a clean false", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		matches, err := Matchesf(crdLikeObject(), `.spec.group == %q`, "wrong.group.io")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(matches).To(BeFalse())
	})

	t.Run("missing field is null, not an error", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		matches, err := Matchesf(crdLikeObject(), `.spec.scope == %q`, "Cluster")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(matches).To(BeFalse())
	})

	t.Run("array and numeric expressions", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		g.Expect(Matchesf(crdLikeObject(), `.spec.versions | length == 1`)).To(BeTrue())
		g.Expect(Matchesf(crdLikeObject(), `.spec.versions[0].served`)).To(BeTrue())
	})
}

func TestCountf(t *testing.T) {
	t.Parallel()

	t.Run("counts selected entries", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		count, err := Countf(crdLikeObject(),
			`[.spec.versions[] | select(.name == %q)] | length`, "v1alpha1")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(count).To(Equal(1))
	})

	t.Run("missing list counts zero with an explicit default", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		// gojq errors on iterating null, unlike jq: a possibly
		// missing list must default to empty, while a malformed one
		// still fails.
		count, err := Countf(crdLikeObject(),
			`[.spec.template.spec.containers // [] | .[]] | length`)

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(count).To(Equal(0))
	})

	t.Run("iterating null without a default is an error", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		_, err := Countf(crdLikeObject(), `[.spec.template.spec.containers[]] | length`)

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("evaluate jq expression"))
	})

	t.Run("non-integer result is an error", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		_, err := Countf(crdLikeObject(), `.spec.group`)

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("want a non-negative integer"))
	})
}

func TestMatchesfUnstructuredInput(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	object := unstructured.Unstructured{Object: crdLikeObject()}

	// Both the value and the pointer form evaluate against the
	// object's content, not the Go struct.
	g.Expect(Matchesf(object, `.spec.names.kind == %q`, "SyntheticModule")).To(BeTrue())
	g.Expect(Matchesf(&object, `.spec.names.kind == %q`, "SyntheticModule")).To(BeTrue())
}

func TestMatchesfErrors(t *testing.T) {
	t.Parallel()

	t.Run("invalid expression", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		_, err := Matchesf(crdLikeObject(), `.spec.group ==`)

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("parse jq expression"))
	})

	t.Run("evaluation failure", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		// Indexing a scalar is a jq runtime error, not a false.
		_, err := Matchesf(map[string]any{"spec": 5}, `.spec.group == %q`, "x")

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("evaluate jq expression"))
	})

	t.Run("no value", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		_, err := Matchesf(crdLikeObject(), `empty`)

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("yielded no value"))
	})

	t.Run("more than one value", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		_, err := Matchesf(crdLikeObject(), `.spec.group, .spec.names.kind`)

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("yielded more than one value"))
	})

	t.Run("non-boolean result", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		_, err := Matchesf(crdLikeObject(), `.spec.group`)

		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("want a boolean"))
	})
}
