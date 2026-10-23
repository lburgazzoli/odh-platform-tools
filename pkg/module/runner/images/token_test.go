package images

import (
	"testing"

	. "github.com/onsi/gomega"
)

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
