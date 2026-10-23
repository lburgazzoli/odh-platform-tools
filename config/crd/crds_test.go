package crd

import (
	"testing"

	"sigs.k8s.io/yaml"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"
)

// crdJSON converts the embedded YAML manifest to a JSON document the
// jq matchers accept.
func crdJSON(t *testing.T) []byte {
	t.Helper()

	json, err := yaml.YAMLToJSON(PlatformModuleDefinitionYAML)
	if err != nil {
		t.Fatalf("convert embedded CRD to JSON: %v", err)
	}

	return json
}

// TestEmbeddedPlatformModuleDefinitionCRD proves the generated CRD is
// accessible from the built package without a source checkout or
// working-directory dependency, with the expected identity, the
// optional+nullable env value schema, and the authored-values schema:
// each value accepts every JSON kind, and the backend paths are
// optional — co-located entries default at render time.
func TestEmbeddedPlatformModuleDefinitionCRD(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	g.Expect(PlatformModuleDefinitionYAML).NotTo(BeEmpty())
	g.Expect(crdJSON(t)).To(SatisfyAll(
		jq.Match(`.apiVersion == "apiextensions.k8s.io/v1"`),
		jq.Match(`.kind == "CustomResourceDefinition"`),
		jq.Match(`.metadata.name == "platformmoduledefinitions.config.opendatahub.io"`),
		jq.Match(`.spec.group == "config.opendatahub.io"`),
		jq.Match(`.spec.names.kind == "PlatformModuleDefinition"`),
		jq.Match(`.spec.scope == "Cluster"`),
		jq.Match(`.spec.versions[0].name == "v1alpha1"`),
		jq.Match(`.spec.versions[0].served == true`),
		jq.Match(`.spec.versions[0].storage == true`),
		jq.Match(`.spec.versions[0].subresources.status == {}`),
		jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.controller.properties.deployment.properties.container.properties.env.items.properties.value.nullable == true`),
		jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.manifests.properties.helm.properties.values.type == "object"`),
		jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.manifests.properties.helm.properties.values.additionalProperties."x-kubernetes-preserve-unknown-fields" == true`),
		jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.manifests.properties.helm | has("required") | not`),
		jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.manifests.properties.kustomize | has("required") | not`),
	))
}
