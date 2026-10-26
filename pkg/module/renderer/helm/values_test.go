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

package helm

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	. "github.com/onsi/gomega"
)

// TestDecodeHelmValues proves authored literal chart values decode to
// the map the Helm renderer consumes: every JSON kind survives,
// including a null entry whose apiextensions JSON representation has
// empty Raw, and placeholder-looking text stays literal.
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

	decoded, err := decodeHelmValues(values)
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

// TestDecodeHelmValuesRejectsMalformedEntry proves a malformed
// authored entry is reported rather than dropped.
func TestDecodeHelmValuesRejectsMalformedEntry(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	values := map[string]apiextensionsv1.JSON{
		"malformed": {Raw: []byte(`{"tag":`)},
	}

	decoded, err := decodeHelmValues(values)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(`helm value "malformed"`))
	g.Expect(decoded).To(BeNil())
}
