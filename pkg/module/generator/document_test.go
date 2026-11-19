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

package generator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
)

// baseSelection is the controller selection of the shared valid
// fixture: a nondefault deployment name, the default container name,
// and no init container.
func baseSelection() controllerSelection {
	return controllerSelection{
		deploymentName: "foo-controller",
		containerName:  "manager",
		containerImage: "quay.io/example/controller:v1",
	}
}

// documentValues builds literal JSON values exercising the number
// preservation and structure of merged Helm overrides. Numbers stay
// within the exact range of the existing loader's YAML decoder.
func documentValues() map[string]apiextensionsv1.JSON {
	return map[string]apiextensionsv1.JSON{
		"replicas": {Raw: []byte("3")},
		"big":      {Raw: []byte("9223372036854775807")},
		"negative": {Raw: []byte("-9223372036854775808")},
		"nested":   {Raw: []byte(`{"list":["a",true,null]}`)},
	}
}

// TestMinimalDocumentRoundTripsThroughLoader proves the minimal
// document, loaded with the existing loader, equals the effective
// definition: defaults fill exactly the omitted fields.
func TestMinimalDocumentRoundTripsThroughLoader(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	values := documentValues()
	selection := baseSelection()
	relatedImages := []string{"a-image", "b-image"}

	document, err := encodeDocument(minimalDocument(
		"foobar", fooBarType(), toolApi.FormatHelm, "chart",
		values, relatedImages, selection))
	g.Expect(err).NotTo(HaveOccurred())

	path := filepath.Join(t.TempDir(), "module.yaml")
	g.Expect(os.WriteFile(path, document, 0o644)).To(Succeed())

	loaded, err := loader.LoadFromPath(context.Background(), path)
	g.Expect(err).NotTo(HaveOccurred())

	effective := effectiveDefinition(
		"foobar", fooBarType(), toolApi.FormatHelm, "chart",
		values, relatedImages, selection)

	g.Expect(loaded.TypeMeta).To(Equal(effective.TypeMeta))
	g.Expect(loaded.Name).To(Equal(effective.Name))
	g.Expect(loaded.Spec.Instance).To(Equal(effective.Spec.Instance))
	g.Expect(loaded.Spec.Manifests).To(Equal(effective.Spec.Manifests))
	g.Expect(loaded.Spec.RelatedImages).To(Equal(effective.Spec.RelatedImages))
	g.Expect(loaded.Spec.Controller).To(Equal(effective.Spec.Controller))
}

// TestMinimalDocumentOmitsDefaultFields proves the wire document
// carries only fields the loader cannot default: instance name,
// container name, and a deployment matching the module name are
// absent, and no lifecycle fields appear.
func TestMinimalDocumentOmitsDefaultFields(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	selection := controllerSelection{
		deploymentName: "foobar",
		containerName:  "manager",
		containerImage: "quay.io/example/controller:v1",
	}

	document, err := encodeDocument(minimalDocument(
		"foobar", fooBarType(), toolApi.FormatKustomize, "",
		nil, nil, selection))
	g.Expect(err).NotTo(HaveOccurred())

	text := string(document)

	g.Expect(text).NotTo(ContainSubstring("name: manager"))
	g.Expect(text).NotTo(ContainSubstring("foo-controller"))
	g.Expect(text).NotTo(ContainSubstring("status"))
	g.Expect(text).NotTo(ContainSubstring("initContainer"))

	path := filepath.Join(t.TempDir(), "module.yaml")
	g.Expect(os.WriteFile(path, document, 0o644)).To(Succeed())

	loaded, err := loader.LoadFromPath(context.Background(), path)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(loaded.Spec.Instance.Name).To(Equal("foobar"))
	g.Expect(loaded.Spec.Controller.Deployment.Container.Name).
		To(Equal(toolApi.DefaultMainContainerName))

	// The deployment name fallback is computed, not defaulted in place.
	g.Expect(loaded.Spec.Controller.Deployment.Name).To(BeEmpty())
	g.Expect(loaded.ControllerDeploymentName()).To(Equal("foobar"))
}

// TestMinimalDocumentKeepsNondefaultSelections proves nondefault
// deployment and container names, a differing init image, and a
// same-image init selection survive into the document.
func TestMinimalDocumentKeepsNondefaultSelections(t *testing.T) {
	t.Parallel()

	sameImage := baseSelection()
	sameImage.initName = "setup"
	sameImage.initImage = sameImage.containerImage

	differingImage := baseSelection()
	differingImage.initName = "setup"
	differingImage.initImage = "quay.io/example/init:v1"
	differingImage.containerName = "controller"

	for _, tc := range []struct {
		name      string
		selection controllerSelection
		contains  []string
		absent    []string
	}{
		{
			name:      "init sharing the main image",
			selection: sameImage,
			contains:  []string{"initContainer", "name: setup"},
			absent:    []string{"quay.io/example/init"},
		},
		{
			name:      "differing init image and container name",
			selection: differingImage,
			contains: []string{
				"initContainer",
				"name: setup",
				"quay.io/example/init:v1",
				"name: controller",
			},
			absent: []string{"name: manager"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			document, err := encodeDocument(minimalDocument(
				"foobar", fooBarType(), toolApi.FormatHelm, "chart",
				nil, nil, tc.selection))
			g.Expect(err).NotTo(HaveOccurred())

			text := string(document)
			for _, fragment := range tc.contains {
				g.Expect(text).To(ContainSubstring(fragment))
			}

			for _, fragment := range tc.absent {
				g.Expect(text).NotTo(ContainSubstring(fragment))
			}
		})
	}
}

// TestEncodeDocumentPreservesLiteralValues proves merged literal Helm
// overrides survive encoding exactly, including 64-bit integers.
func TestEncodeDocumentPreservesLiteralValues(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	document, err := encodeDocument(minimalDocument(
		"foobar", fooBarType(), toolApi.FormatHelm, "chart",
		documentValues(), nil, baseSelection()))
	g.Expect(err).NotTo(HaveOccurred())

	text := string(document)

	g.Expect(text).To(ContainSubstring("replicas: 3"))
	g.Expect(text).To(ContainSubstring("big: 9223372036854775807"))
	g.Expect(text).To(ContainSubstring("negative: -9223372036854775808"))
	g.Expect(text).To(ContainSubstring("nested"))

	// Values above the loader's exact range still serialize as exact
	// literals; only the loader's own YAML decoder would degrade them.
	edge, err := encodeDocument(minimalDocument(
		"foobar", fooBarType(), toolApi.FormatHelm, "chart",
		map[string]apiextensionsv1.JSON{
			"max": {Raw: []byte("18446744073709551615")},
		}, nil, baseSelection()))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(string(edge)).To(ContainSubstring("max: 18446744073709551615"))
}

// TestEncodeDocumentIsDeterministic proves repeated encodes of the
// same inputs produce identical bytes.
func TestEncodeDocumentIsDeterministic(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	values := documentValues()

	first, err := encodeDocument(minimalDocument(
		"foobar", fooBarType(), toolApi.FormatHelm, "chart",
		values, []string{"a-image", "b-image"}, baseSelection()))
	g.Expect(err).NotTo(HaveOccurred())

	second, err := encodeDocument(minimalDocument(
		"foobar", fooBarType(), toolApi.FormatHelm, "chart",
		values, []string{"a-image", "b-image"}, baseSelection()))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(first).To(Equal(second))
}

// TestManifestPathResolvesRelativeSources proves a source equal to
// the definition directory omits the manifests path, and other
// sources resolve relative to it, with absolute fallback.
func TestManifestPathResolvesRelativeSources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	g.Expect(manifestPath("/work/chart", "/work/chart")).To(Equal(""))

	g.Expect(manifestPath("/work/source/chart", "/work")).
		To(Equal(filepath.Join("source", "chart")))

	g.Expect(manifestPath("not/a/path", "")).To(ContainSubstring("not"))
}

// TestManifestPathCanonicalizesSymlinks proves a symlinked definition
// directory does not leak its resolved prefix into the authored
// relative path, and a symlinked alias of the source directory still
// omits the manifests path entirely.
func TestManifestPathCanonicalizesSymlinks(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()

	g.Expect(os.MkdirAll(filepath.Join(root, "real", "chart"), 0o755)).To(Succeed())
	g.Expect(os.MkdirAll(filepath.Join(root, "real", "out"), 0o755)).To(Succeed())
	g.Expect(os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias"))).
		To(Succeed())

	g.Expect(manifestPath(filepath.Join(root, "real", "chart"),
		filepath.Join(root, "alias", "out"))).
		To(Equal(filepath.Join("..", "chart")))

	g.Expect(manifestPath(filepath.Join(root, "alias", "chart"),
		filepath.Join(root, "real", "chart"))).
		To(Equal(""))
}
