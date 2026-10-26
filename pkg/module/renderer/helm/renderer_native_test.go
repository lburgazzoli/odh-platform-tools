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

	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// TestNewBuildsNativeHelmRenderer proves the backend constructor
// returns the configured native renderer: construction alone neither
// renders nor guards, and the release identity — name from
// metadata.name, namespace from the resolved argument — reaches chart
// templates through Process exactly as the render entrypoints
// configure it.
func TestNewBuildsNativeHelmRenderer(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", definitionYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", namespacedTemplateYAML)

	definition := loadDefinition(t, root)

	native, err := New(definition, root+"/definition.yaml", "modules")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(native).NotTo(BeNil())
	g.Expect(native.Name()).NotTo(BeEmpty())

	resources, err := native.Process(t.Context(), nil)
	g.Expect(err).NotTo(HaveOccurred())

	// The release name is metadata.name and the resolved namespace is
	// .Release.Namespace; the renderer never fills the namespace for
	// a template that does not template it.
	deployment := findResource(t, resources, "Deployment", "synthetic-controller")
	g.Expect(deployment.GetNamespace()).To(Equal("modules"))
}

// TestNewHelmValuesDecodeLazilyThroughProcess proves authored literal
// values stay lazy in the native source: construction succeeds with a
// malformed entry, and only Process — the renderer's own error
// channel — fails, carrying the value-decoding cause rather than a
// construction error.
func TestNewHelmValuesDecodeLazilyThroughProcess(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeChartFixture(t, root)

	definition := loadDefinition(t, root)
	definition.Spec.Manifests.Helm.Values = map[string]apiextensionsv1.JSON{
		"malformed": {Raw: []byte(`{"tag":`)},
	}

	native, err := New(definition, root+"/definition.yaml", "")
	g.Expect(err).NotTo(HaveOccurred())

	_, err = native.Process(t.Context(), nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(`helm value "malformed"`))
}

// TestNewHelmKeepsDefinitionUntouched pins that construction reads
// the definition without mutating it: the descriptor strings the
// renderer must ignore stay authored.
func TestNewHelmKeepsDefinitionUntouched(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeModuleFile(t, root, "definition.yaml", definitionWithEnvYAML)
	writeModuleFile(t, root, "manifests/chart/Chart.yaml", chartYAML)
	writeModuleFile(t, root, "manifests/chart/values.yaml", valuesYAML)
	writeModuleFile(t, root, "manifests/chart/templates/deployment.yaml", templateYAML)

	definition := loadDefinition(t, root)
	before := definition.DeepCopy()

	_, err := New(definition, root+"/definition.yaml", "modules")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(definition).To(Equal(*before))
}
