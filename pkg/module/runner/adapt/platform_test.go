package adapt

import (
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"
	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/k8s"
)

// moduleConfigMapYAML is a module-rendered ConfigMap co-located in
// the applications namespace that happens to carry the platform
// config name, with its own data key, binaryData, a stale platform
// version, and a stale cert-manager key.
const moduleConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: opendatahub
data:
  MODULE_KEY: module-value
  platformVersion: "1.0.0"
  CERT_MANAGER_ISSUER_REF_NAME: stale-issuer
binaryData:
  extra.bin: dGVzdA==
`

// foreignConfigMapYAML is a same-name ConfigMap explicitly rendered
// into a foreign namespace: not the platform config's identity, so it
// is left untouched.
const foreignConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: rendered-namespace
data:
  MODULE_KEY: module-value
`

// malformedDataConfigMapYAML is a module-rendered ConfigMap of the
// platform config name whose data entries are not all strings.
const malformedDataConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: opendatahub
data:
  MODULE_KEY:
    nested: value
`

// firstConfigMapYAML and secondConfigMapYAML are two ConfigMaps of
// the same name in the applications namespace, pinning the
// last-wins semantics of the exact-identity selection.
const firstConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: opendatahub
data:
  MODULE_KEY: first-value
`

// secondConfigMapYAML is the later same-name ConfigMap.
const secondConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: opendatahub
data:
  MODULE_KEY: second-value
`

// wrongGroupConfigMapYAML is a ConfigMap lookalike of the platform
// config name in a foreign group.
const wrongGroupConfigMapYAML = `apiVersion: config.example.com/v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
data:
  MODULE_KEY: wrong-group
`

// wrongVersionConfigMapYAML is a ConfigMap lookalike of the platform
// config name in the wrong core version.
const wrongVersionConfigMapYAML = `apiVersion: v2
kind: ConfigMap
metadata:
  name: odh-synthetic-config
data:
  MODULE_KEY: wrong-version
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
		Definition:            newDefinition(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, serviceYAML)})
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(2))

	configMap := findResource(t, resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap).To(WithTransform(k8s.Data(), SatisfyAll(
		HaveKeyWithValue("platformVersion", "2.19.0"),
		HaveLen(1),
	)))
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.apiVersion == "v1"`),
		jq.Match(`.kind == "ConfigMap"`),
		jq.Match(`.metadata.name == "odh-synthetic-config"`),
		jq.Match(`.metadata.namespace == "opendatahub"`),
		jq.Match(`.metadata | has("annotations") | not`),
	))
}

// TestPlatformConfigInjectorMergesExisting pins that a module-rendered
// ConfigMap of the platform config name co-located in the applications
// namespace keeps its module-owned keys,
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
		Definition:            newDefinition(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.20.1",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, moduleConfigMapYAML)})
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(1))

	configMap := findResource(t, resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap).To(WithTransform(k8s.Data(), SatisfyAll(
		HaveKeyWithValue("MODULE_KEY", "module-value"),
		HaveKeyWithValue("platformVersion", "2.20.1"),
		Not(HaveKey("CERT_MANAGER_ISSUER_REF_NAME")),
	)))
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "opendatahub"`),
		jq.Match(`.metadata | has("annotations") | not`),
		jq.Match(`.binaryData["extra.bin"] == "dGVzdA=="`),
	))
}

// TestPlatformConfigInjectorIgnoresForeignNamespace pins that a
// same-name ConfigMap explicitly rendered into a foreign namespace is
// not the platform config's identity: it passes through untouched and
// the platform config ConfigMap is appended in the applications
// namespace instead.
func TestPlatformConfigInjectorIgnoresForeignNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinition(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, foreignConfigMapYAML)})
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(2))

	g.Expect(resources[0]).To(Equal(resource(t, foreignConfigMapYAML)))

	configMap := findResource(t, resources[1:], "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap).To(WithTransform(k8s.Data(), SatisfyAll(
		HaveKeyWithValue("platformVersion", "2.19.0"),
		HaveLen(1),
	)))
	g.Expect(configMap.Object).To(jq.Match(`.metadata.namespace == "opendatahub"`))
}

// TestPlatformConfigInjectorMalformedDataErrors pins that a malformed
// (non-string) data block fails the render instead of being repaired:
// the typed round-trip rejects it.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (mergePlatformKeys, ignored NestedStringMap error), commit 5a1d17f57b32.
func TestPlatformConfigInjectorMalformedDataErrors(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinition(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.20.1",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, malformedDataConfigMapYAML)})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("converting platform config ConfigMap for synthetic"))
	g.Expect(resources).To(BeNil())
}

// TestPlatformConfigInjectorLastExactMatchWins pins that the last
// exact identity match of the name in the applications namespace wins
// the merge.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (indexConfigMapsByName last-wins), commit 5a1d17f57b32.
func TestPlatformConfigInjectorLastExactMatchWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinition(),
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
	g.Expect(first).To(WithTransform(k8s.Data(), SatisfyAll(
		HaveKeyWithValue("MODULE_KEY", "first-value"),
		Not(HaveKey("platformVersion")),
	)))

	second := findResource(t, resources[1:], "ConfigMap", "odh-synthetic-config")
	g.Expect(second).To(WithTransform(k8s.Data(), SatisfyAll(
		HaveKeyWithValue("MODULE_KEY", "second-value"),
		HaveKeyWithValue("platformVersion", "2.19.0"),
	)))
}

// TestPlatformConfigInjectorExactGVKSelection pins that only the
// object's full identity is a candidate: same-name lookalikes in a
// foreign group or wrong version never shadow an earlier valid match,
// wherever they sit in the batch, and pass through untouched with the
// batch order preserved.
func TestPlatformConfigInjectorExactGVKSelection(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinition(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{
		resource(t, firstConfigMapYAML),
		resource(t, wrongGroupConfigMapYAML),
		resource(t, wrongVersionConfigMapYAML),
	})
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(3))

	first := findResource(t, resources[:1], "ConfigMap", "odh-synthetic-config")
	g.Expect(first).To(WithTransform(k8s.Data(), SatisfyAll(
		HaveKeyWithValue("MODULE_KEY", "first-value"),
		HaveKeyWithValue("platformVersion", "2.19.0"),
	)))

	g.Expect(resources[1]).To(Equal(resource(t, wrongGroupConfigMapYAML)))
	g.Expect(resources[2]).To(Equal(resource(t, wrongVersionConfigMapYAML)))
}

// TestPlatformConfigInjectorAppendsWithLookalikeOnly pins that a batch
// containing only a wrong-GVK lookalike keeps it unchanged and appends
// a real core/v1 platform config ConfigMap.
func TestPlatformConfigInjectorAppendsWithLookalikeOnly(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinition(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{
		resource(t, wrongGroupConfigMapYAML),
	})
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(2))
	g.Expect(resources[0]).To(Equal(resource(t, wrongGroupConfigMapYAML)))

	configMap := findResource(t, resources[1:], "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap).To(WithTransform(k8s.Data(),
		HaveKeyWithValue("platformVersion", "2.19.0")))
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.apiVersion == "v1"`),
		jq.Match(`.kind == "ConfigMap"`),
		jq.Match(`.metadata.namespace == "opendatahub"`),
	))
}

// TestPlatformConfigInjectorDoesNotMutateDefinition pins that the
// injector only reads the definition the runner owns: after a full
// pass over a rendered batch, it is exactly as supplied.
func TestPlatformConfigInjectorDoesNotMutateDefinition(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := newDefinition()

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            definition,
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}).Run

	resources, err := render(t.Context(), []unstructured.Unstructured{resource(t, moduleConfigMapYAML)})
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(1))
	g.Expect(*definition).To(Equal(*newDefinition()))
}

// TestPlatformConfigInjectorPassesGateConfigMapsThrough pins that a
// ConfigMap carrying the operator's upgrade-gate label is an ordinary
// rendered resource: it is neither extracted, reported, nor otherwise
// touched — no gates subsystem exists here.
func TestPlatformConfigInjectorPassesGateConfigMapsThrough(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = (&PlatformConfigInjector{
		Definition:            newDefinition(),
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
	g.Expect(configMap).To(WithTransform(k8s.Data(),
		HaveKeyWithValue("platformVersion", "2.19.0")))
}
