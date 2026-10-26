package adapt

import (
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"
)

// moduleConfigMapYAML is a module-rendered ConfigMap that happens to
// carry the platform config name, with its own data key, a stale
// platform version, and a stale cert-manager key.
const moduleConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: rendered-namespace
data:
  MODULE_KEY: module-value
  platformVersion: "1.0.0"
  CERT_MANAGER_ISSUER_REF_NAME: stale-issuer
`

// malformedDataConfigMapYAML is a module-rendered ConfigMap of the
// platform config name whose data entries are not all strings.
const malformedDataConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
data:
  MODULE_KEY:
    nested: value
`

// firstConfigMapYAML and secondConfigMapYAML are two ConfigMaps of
// the same name in different namespaces, pinning the kind/name-only
// index and its last-wins semantics.
const firstConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: first-namespace
data:
  MODULE_KEY: first-value
`

// secondConfigMapYAML is the later same-name ConfigMap.
const secondConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: second-namespace
data:
  MODULE_KEY: second-value
`

// gateConfigMapYAML is a ConfigMap carrying the operator's upgrade-gate
// label; gates are out of scope, so it is an ordinary resource.
const gateConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-synthetic-upgrade-gate
  namespace: opendatahub
  labels:
    platform.opendatahub.io/upgrade-gate: "true"
data:
  release: "2.19.0"
`

// TestPlatformConfigInjectorCreates pins that the odh-<module>-config
// ConfigMap is appended in the applications namespace with the
// canonical runner-supplied platform version, and no rollout hash or
// other annotation is added.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (injectPlatformConfig, buildPlatformConfigMap), commit 5a1d17f57b32.
func TestPlatformConfigInjectorCreates(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinitionPointer(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, serviceYAML)})
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(2))

	configMap := findResource(t, resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.apiVersion == "v1"`),
		jq.Match(`.kind == "ConfigMap"`),
		jq.Match(`.metadata.name == "odh-synthetic-config"`),
		jq.Match(`.metadata.namespace == "opendatahub"`),
		jq.Match(`.metadata | has("annotations") | not`),
		jq.Match(`.data.platformVersion == "2.19.0"`),
		jq.Match(`.data | length == 1`),
	))
}

// TestPlatformConfigInjectorMergesExisting pins that a module-rendered
// ConfigMap of the platform config name keeps its module-owned keys,
// has its platform version enforced, and has stale platform-managed
// cert-manager keys removed — without a new resource being appended or
// a rollout hash added.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (mergePlatformKeys), commit 5a1d17f57b32.
func TestPlatformConfigInjectorMergesExisting(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinitionPointer(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.20.1",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, moduleConfigMapYAML)})
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(1))

	configMap := findResource(t, resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.data.MODULE_KEY == "module-value"`),
		jq.Match(`.data.platformVersion == "2.20.1"`),
		jq.Match(`.data | has("CERT_MANAGER_ISSUER_REF_NAME") | not`),
		jq.Match(`.metadata.namespace == "rendered-namespace"`),
		jq.Match(`.metadata | has("annotations") | not`),
	))
}

// TestPlatformConfigInjectorReplacesMalformedData pins that a
// malformed (non-string) data block is replaced by the platform keys
// alone.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (mergePlatformKeys, ignored NestedStringMap error), commit 5a1d17f57b32.
func TestPlatformConfigInjectorReplacesMalformedData(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinitionPointer(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.20.1",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, malformedDataConfigMapYAML)})
	g.Expect(err).To(Succeed())

	configMap := findResource(t, resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.data | length == 1`),
		jq.Match(`.data.platformVersion == "2.20.1"`),
		jq.Match(`.data | has("MODULE_KEY") | not`),
	))
}

// TestPlatformConfigInjectorIndexKindNameOnly pins that ConfigMaps
// are matched by kind and name only — namespace-insensitive — and
// that a later resource with the same name wins the merge.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (indexConfigMapsByName), commit 5a1d17f57b32.
func TestPlatformConfigInjectorIndexKindNameOnly(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinitionPointer(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{
		resource(t, firstConfigMapYAML),
		resource(t, secondConfigMapYAML),
	})
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(2))

	first := findResource(t, resources[:1], "ConfigMap", "odh-synthetic-config")
	g.Expect(first.Object).To(SatisfyAll(
		jq.Match(`.data.MODULE_KEY == "first-value"`),
		jq.Match(`.data | has("platformVersion") | not`),
	))

	second := findResource(t, resources[1:], "ConfigMap", "odh-synthetic-config")
	g.Expect(second.Object).To(SatisfyAll(
		jq.Match(`.data.MODULE_KEY == "second-value"`),
		jq.Match(`.data.platformVersion == "2.19.0"`),
	))
}

// TestPlatformConfigInjectorDoesNotMutateDefinition pins that the
// injector only reads the definition the runner owns: after a full
// pass over a rendered batch, it is exactly as supplied.
func TestPlatformConfigInjectorDoesNotMutateDefinition(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinitionPointer()

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            definition,
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, moduleConfigMapYAML)})
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(1))
	g.Expect(*definition).To(Equal(newDefinition()))
}

// TestPlatformConfigInjectorPassesGateConfigMapsThrough pins that a
// ConfigMap carrying the operator's upgrade-gate label is an ordinary
// rendered resource: it is neither extracted, reported, nor otherwise
// touched — no gates subsystem exists here.
func TestPlatformConfigInjectorPassesGateConfigMapsThrough(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinitionPointer(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, gateConfigMapYAML)})
	g.Expect(err).To(Succeed())

	// The gate ConfigMap passes through untouched; the only addition
	// is the ordinary platform config ConfigMap.
	g.Expect(resources).To(HaveLen(2))
	g.Expect(resources[0]).To(Equal(resource(t, gateConfigMapYAML)))

	configMap := findResource(t, resources[1:], "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap.Object).To(jq.Match(`.data.platformVersion == "2.19.0"`))
}
