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

package values

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	jsonv2 "encoding/json/v2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	. "github.com/onsi/gomega"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// typedValuesYAML exercises every supported JSON kind plus text that
// must survive literally.
const typedValuesYAML = `replicas: 2
enabled: true
ratio: 1.5
image:
  tag: v1.0.0
  pullPolicy: IfNotPresent
servers:
  - name: first
    port: 8080
  - name: second
    port: 8443
extras:
  emptyMap: {}
  emptyList: []
  nullValue: null
placeholder: "{{RELATED_IMAGE_EXAMPLE_CONTROLLER}}"
template: "${RELATED_IMAGE_EXAMPLE_CONTROLLER}"
caseSensitive: KeepCase
`

// exactIntegerValuesYAML keeps integers that lose exactness through
// float64 rounding.
const exactIntegerValuesYAML = `aboveDoublePrecision: 9007199254740993
maxInt64: 9223372036854775807
minInt64: -9223372036854775808
maxUint64: 18446744073709551615
`

// aliasedValuesYAML proves non-cyclic anchors expand to equal values.
const aliasedValuesYAML = `common: &common
  level: info
first: *common
second: *common
`

// mergedValuesYAML is the file-side input for merge-order checks.
const mergedValuesYAML = `replicas: 2
image:
  tag: v1.0.0
retained: keep
`

// secretValuesYAML carries a secret-looking value whose text must
// never reach a public error message.
const secretValuesYAML = `password: super-secret-value
password: repeated
`

// valuesFS adapts a document to the filesystem the loader reads from.
func valuesFS(document string) fstest.MapFS {
	return fstest.MapFS{
		"values.yaml": &fstest.MapFile{Data: []byte(document)},
	}
}

// decodeEntry unmarshals one stored JSON value for assertions.
func decodeEntry(
	t *testing.T,
	stored map[string]apiextensionsv1.JSON,
	key string,
) any {
	t.Helper()

	raw := stored[key].Raw
	if len(raw) == 0 {
		return nil
	}

	var decoded any
	if err := jsonv2.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode stored value %q: %v", key, err)
	}

	return decoded
}

// TestLoadOmittedInputs proves omitting both the values file and the
// overrides yields nil values, and overrides alone need no filesystem.
func TestLoadOmittedInputs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stored, err := Load(fstest.MapFS{}, "", nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stored).To(BeNil())

	stored, err = LoadFromPath("", nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stored).To(BeNil())

	stored, err = Load(nil, "", []string{"count=1"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(stored["count"].Raw)).To(Equal("1"))
}

// TestLoadEmptyMapping proves an explicit empty mapping document is
// accepted as an empty values set.
func TestLoadEmptyMapping(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stored, err := Load(valuesFS("{}\n"), "values.yaml", nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stored).NotTo(BeNil())
	g.Expect(stored).To(BeEmpty())
}

// TestLoadTypedValuesFile proves a values file preserves every
// supported JSON kind, nested nulls, empty collections, and literal
// placeholder text.
func TestLoadTypedValuesFile(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stored, err := Load(valuesFS(typedValuesYAML), "values.yaml", nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stored).To(HaveLen(9))

	g.Expect(decodeEntry(t, stored, "replicas")).To(Equal(float64(2)))
	g.Expect(decodeEntry(t, stored, "enabled")).To(BeTrue())
	g.Expect(decodeEntry(t, stored, "ratio")).To(Equal(float64(1.5)))
	g.Expect(decodeEntry(t, stored, "image")).To(Equal(map[string]any{
		"tag":        "v1.0.0",
		"pullPolicy": "IfNotPresent",
	}))
	g.Expect(decodeEntry(t, stored, "servers")).To(Equal([]any{
		map[string]any{"name": "first", "port": float64(8080)},
		map[string]any{"name": "second", "port": float64(8443)},
	}))
	g.Expect(decodeEntry(t, stored, "extras")).To(Equal(map[string]any{
		"emptyMap":  map[string]any{},
		"emptyList": []any{},
		"nullValue": nil,
	}))
	g.Expect(decodeEntry(t, stored, "placeholder")).To(
		Equal("{{RELATED_IMAGE_EXAMPLE_CONTROLLER}}"))
	g.Expect(decodeEntry(t, stored, "template")).To(
		Equal("${RELATED_IMAGE_EXAMPLE_CONTROLLER}"))
	g.Expect(decodeEntry(t, stored, "caseSensitive")).To(Equal("KeepCase"))
}

// TestLoadValuesIgnoreEnvironment proves stored text is literal: an
// environment variable named like a placeholder changes nothing.
func TestLoadValuesIgnoreEnvironment(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("RELATED_IMAGE_EXAMPLE_CONTROLLER", "injected-from-env")

	stored, err := Load(valuesFS(typedValuesYAML), "values.yaml", nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(decodeEntry(t, stored, "template")).To(
		Equal("${RELATED_IMAGE_EXAMPLE_CONTROLLER}"))
	g.Expect(decodeEntry(t, stored, "placeholder")).To(
		Equal("{{RELATED_IMAGE_EXAMPLE_CONTROLLER}}"))
}

// TestLoadExactIntegers proves integers above the float64 mantissa
// precision and at both 64-bit limits persist exactly in stored JSON.
func TestLoadExactIntegers(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stored, err := Load(valuesFS(exactIntegerValuesYAML), "values.yaml", nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(stored["aboveDoublePrecision"].Raw)).To(Equal("9007199254740993"))
	g.Expect(string(stored["maxInt64"].Raw)).To(Equal("9223372036854775807"))
	g.Expect(string(stored["minInt64"].Raw)).To(Equal("-9223372036854775808"))
	g.Expect(string(stored["maxUint64"].Raw)).To(Equal("18446744073709551615"))
}

// TestLoadAliasedValues proves non-cyclic anchors expand to their
// anchored content.
func TestLoadAliasedValues(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stored, err := Load(valuesFS(aliasedValuesYAML), "values.yaml", nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(decodeEntry(t, stored, "common")).To(Equal(map[string]any{"level": "info"}))
	g.Expect(decodeEntry(t, stored, "first")).To(Equal(map[string]any{"level": "info"}))
	g.Expect(decodeEntry(t, stored, "second")).To(Equal(map[string]any{"level": "info"}))
}

// TestLoadOverridesOnly proves native Helm expressions alone build
// values: dotted keys, list indexes, list literals, commas, escapes,
// and scalar kinds.
func TestLoadOverridesOnly(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	overrides := []string{
		"image.tag=v2.0.0",
		"servers[0].port=9090",
		"names={alpha,beta}",
		"flags.enabled=false",
		"count=3",
		"label=null",
		`escaped=a\,b`,
	}

	stored, err := Load(nil, "", overrides)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stored).To(HaveLen(7))

	g.Expect(decodeEntry(t, stored, "image")).To(Equal(map[string]any{"tag": "v2.0.0"}))
	g.Expect(decodeEntry(t, stored, "servers")).To(
		Equal([]any{map[string]any{"port": float64(9090)}}))
	g.Expect(decodeEntry(t, stored, "names")).To(Equal([]any{"alpha", "beta"}))
	g.Expect(decodeEntry(t, stored, "flags")).To(
		Equal(map[string]any{"enabled": false}))
	g.Expect(decodeEntry(t, stored, "count")).To(Equal(float64(3)))
	g.Expect(decodeEntry(t, stored, "label")).To(BeNil())
	g.Expect(decodeEntry(t, stored, "escaped")).To(Equal("a,b"))
}

// TestLoadMergesFileThenOverrides proves the values file loads first,
// overrides apply in order on top of it, and unrelated file keys are
// retained.
func TestLoadMergesFileThenOverrides(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	overrides := []string{
		"replicas=3",
		"image.tag=v2.0.0",
		"added=new",
	}

	stored, err := Load(valuesFS(mergedValuesYAML), "values.yaml", overrides)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(stored["replicas"].Raw)).To(Equal("3"))
	g.Expect(decodeEntry(t, stored, "image")).To(Equal(map[string]any{"tag": "v2.0.0"}))
	g.Expect(decodeEntry(t, stored, "retained")).To(Equal("keep"))
	g.Expect(decodeEntry(t, stored, "added")).To(Equal("new"))
}

// TestLoadLastOverrideWins proves repeated assignments follow native
// parser semantics: the last one wins without dropping other keys.
func TestLoadLastOverrideWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stored, err := Load(nil, "", []string{"first=1", "second=2", "first=3"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(stored["first"].Raw)).To(Equal("3"))
	g.Expect(string(stored["second"].Raw)).To(Equal("2"))
}

// TestLoadFromPathMatchesLoad proves the native-path loader returns
// the same stored values as the filesystem loader.
func TestLoadFromPathMatchesLoad(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	path := filepath.Join(t.TempDir(), "values.yaml")
	g.Expect(os.WriteFile(path, []byte(mergedValuesYAML), 0o600)).To(Succeed())

	overrides := []string{"replicas=4", "image.tag=v3.0.0"}

	fromPath, err := LoadFromPath(path, overrides)
	g.Expect(err).NotTo(HaveOccurred())

	fromFS, err := Load(valuesFS(mergedValuesYAML), "values.yaml", overrides)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(fromPath).To(Equal(fromFS))
}

// TestLoadRejectsDocumentViolations proves the JSON-compatible YAML
// domain is enforced before generic decoding loses the distinction.
func TestLoadRejectsDocumentViolations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		document string
	}{
		{"empty document", ""},
		{"comment only", "# nothing here\n"},
		{"scalar root", "just-a-string\n"},
		{"sequence root", "- one\n- two\n"},
		{"null root", "~\n"},
		{"multiple documents", "a: 1\n---\nb: 2\n"},
		{"duplicate keys", "a: 1\na: 2\n"},
		{"integer key", "1: value\n"},
		{"boolean key", "true: value\n"},
		{"complex key", "? [a, b]\n: value\n"},
		{"timestamp value", "when: 2026-01-02\n"},
		{"binary value", "data: !!binary aGk=\n"},
		{"custom collection tag", "custom: !Set\n  a: 1\n"},
		{"custom scalar tag", "custom: !Secret value\n"},
		{"merge key", "base: &base {a: 1}\nmerged:\n  <<: *base\n"},
		{"nonfinite float", "nan: .nan\n"},
		{"infinite float", "inf: .inf\n"},
		{"integer above range", "huge: 18446744073709551616\n"},
		{"integer below range", "tiny: -9223372036854775809\n"},
		{"cyclic alias", "a: &anchor\n  b: *anchor\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			stored, err := Load(valuesFS(test.document), "values.yaml", nil)
			g.Expect(err).To(HaveOccurred())
			g.Expect(err.Error()).To(ContainSubstring("values.yaml"))
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
			g.Expect(stored).To(BeNil())
		})
	}
}

// TestLoadRejectsConflictingAssignments proves native scalar/map/list
// assignment conflicts return classified errors instead of coercion.
func TestLoadRejectsConflictingAssignments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		document  string
		overrides []string
	}{
		{"map onto scalar", "a: 1\n", []string{"a.b=2"}},
		{"list onto scalar", "a: 1\n", []string{"a[0]=2"}},
		{"map onto list", "a:\n  - 1\n", []string{"a.b=2"}},
		{"expression without value", "", []string{"novalue"}},
		{"expression with bad index", "", []string{"a[)=2"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			stored, err := Load(valuesFS(test.document), "values.yaml", test.overrides)
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
			g.Expect(stored).To(BeNil())
		})
	}
}

// TestLoadRejectsUnreadableFile proves missing and unreadable values
// files fail with the file named in the public message.
func TestLoadRejectsUnreadableFile(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stored, err := Load(fstest.MapFS{}, "absent.yaml", nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("absent.yaml"))
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	g.Expect(stored).To(BeNil())

	stored, err = LoadFromPath(filepath.Join(t.TempDir(), "absent.yaml"), nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	g.Expect(stored).To(BeNil())
}

// TestLoadErrorExposesNoValues proves public error messages never
// echo values-file contents.
func TestLoadErrorExposesNoValues(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	_, err := Load(valuesFS(secretValuesYAML), "values.yaml", nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).NotTo(ContainSubstring("super-secret-value"))
}

// TestLoadRejectsExponentialAliasExpansion proves an acyclic alias
// graph that would expand exponentially fails fast with the
// classified definition error and no document content in the public
// message.
func TestLoadRejectsExponentialAliasExpansion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	document := strings.Builder{}
	document.WriteString("level0: &level0 [payload]\n")

	for level := 1; level <= 24; level++ {
		fmt.Fprintf(&document, "level%d: &level%d [*level%d, *level%d]\n",
			level, level, level-1, level-1)
	}

	stored, err := Load(valuesFS(document.String()), "values.yaml", nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("values.yaml"))
	g.Expect(err.Error()).NotTo(ContainSubstring("payload"))
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	g.Expect(stored).To(BeNil())
}

// TestLoadAllowsModerateAliasReuse proves reasonable anchor reuse
// stays well inside the conversion budget.
func TestLoadAllowsModerateAliasReuse(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	document := strings.Builder{}
	document.WriteString("common: &common\n  level: info\n")

	for consumer := range 12 {
		fmt.Fprintf(&document, "consumer%d: *common\n", consumer)
	}

	stored, err := Load(valuesFS(document.String()), "values.yaml", nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(decodeEntry(t, stored, "consumer11")).
		To(Equal(map[string]any{"level": "info"}))
}
