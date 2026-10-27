package adapt

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"
	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/k8s"

	toolk8s "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
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

// wrongGroupConfigMapYAML is a ConfigMap lookalike sharing the
// platform config's namespace and name in a foreign group: only its
// group differs from the target's identity.
const wrongGroupConfigMapYAML = `apiVersion: config.example.com/v1
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: opendatahub
data:
  MODULE_KEY: wrong-group
`

// wrongVersionConfigMapYAML is a ConfigMap lookalike sharing the
// platform config's namespace and name in the wrong core version:
// only its version differs from the target's identity.
const wrongVersionConfigMapYAML = `apiVersion: v2
kind: ConfigMap
metadata:
  name: odh-synthetic-config
  namespace: opendatahub
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

// platformConfigKey is the full identity of the platform config
// ConfigMap the injector targets.
var platformConfigKey = types.NamespacedName{Namespace: "opendatahub", Name: "odh-synthetic-config"}

// wrongGroupConfigMapGVK and wrongVersionConfigMapGVK are the
// group/version/kinds of the lookalike fixtures, for looking them up
// by their own full identities.
var (
	wrongGroupConfigMapGVK   = schema.GroupVersionKind{Group: "config.example.com", Version: "v1", Kind: "ConfigMap"}
	wrongVersionConfigMapGVK = schema.GroupVersionKind{Version: "v2", Kind: "ConfigMap"}
)

// foreignConfigMapKey is the full identity of the foreign-namespace
// lookalike.
var foreignConfigMapKey = types.NamespacedName{Namespace: "rendered-namespace", Name: "odh-synthetic-config"}

// newPlatformConfigInjector returns the platform config injector with
// fully populated runtime inputs.
func newPlatformConfigInjector() *PlatformConfigInjector {
	return &PlatformConfigInjector{
		Definition:            newDefinition(),
		ApplicationsNamespace: "opendatahub",
		PlatformVersion:       "2.19.0",
	}
}

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

	var render enginetypes.PostRenderer = newPlatformConfigInjector().Run

	input, err := toolk8s.DecodeResources([]byte(serviceYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(2))

	configMap := findResource(resources, toolk8s.ConfigMap, platformConfigKey)
	g.Expect(configMap).NotTo(BeNil(), "expected ConfigMap opendatahub/odh-synthetic-config in the rendered output")
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

	input, err := toolk8s.DecodeResources([]byte(moduleConfigMapYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(1))

	configMap := findResource(resources, toolk8s.ConfigMap, platformConfigKey)
	g.Expect(configMap).NotTo(BeNil(), "expected ConfigMap opendatahub/odh-synthetic-config in the rendered output")
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
// not the platform config's identity: wherever it sits in the batch it
// passes through untouched while the real target is merged, and a
// batch holding only the foreign lookalike still gets the platform
// config ConfigMap appended.
func TestPlatformConfigInjectorIgnoresForeignNamespace(t *testing.T) {
	t.Parallel()

	for name, batch := range map[string][]string{
		"lookalike before the target": {foreignConfigMapYAML, firstConfigMapYAML},
		"lookalike after the target":  {firstConfigMapYAML, foreignConfigMapYAML},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			var render enginetypes.PostRenderer = newPlatformConfigInjector().Run

			input, err := toolk8s.DecodeResources([]byte(strings.Join(batch, "\n---\n")))
			g.Expect(err).To(Succeed())
			g.Expect(input).To(HaveLen(2))

			resources, err := render(t.Context(), input)
			g.Expect(err).To(Succeed())

			g.Expect(resources).To(HaveLen(2))

			configMap := findResource(resources, toolk8s.ConfigMap, platformConfigKey)
			g.Expect(configMap).NotTo(BeNil(), "expected ConfigMap opendatahub/odh-synthetic-config in the rendered output")
			g.Expect(configMap).To(WithTransform(k8s.Data(), SatisfyAll(
				HaveKeyWithValue("MODULE_KEY", "first-value"),
				HaveKeyWithValue("platformVersion", "2.19.0"),
			)))

			expected, err := toolk8s.DecodeResources([]byte(foreignConfigMapYAML))
			g.Expect(err).To(Succeed())
			g.Expect(expected).To(HaveLen(1))

			foreign := findResource(resources, toolk8s.ConfigMap, foreignConfigMapKey)
			g.Expect(foreign).NotTo(BeNil(), "expected ConfigMap rendered-namespace/odh-synthetic-config in the rendered output")
			g.Expect(*foreign).To(Equal(expected[0]))
		})
	}

	t.Run("foreign only appends", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		var render enginetypes.PostRenderer = newPlatformConfigInjector().Run

		input, err := toolk8s.DecodeResources([]byte(foreignConfigMapYAML))
		g.Expect(err).To(Succeed())
		g.Expect(input).To(HaveLen(1))

		resources, err := render(t.Context(), input)
		g.Expect(err).To(Succeed())

		g.Expect(resources).To(HaveLen(2))

		configMap := findResource(resources, toolk8s.ConfigMap, platformConfigKey)
		g.Expect(configMap).NotTo(BeNil(), "expected ConfigMap opendatahub/odh-synthetic-config in the rendered output")
		g.Expect(configMap).To(WithTransform(k8s.Data(), SatisfyAll(
			HaveKeyWithValue("platformVersion", "2.19.0"),
			HaveLen(1),
		)))
		g.Expect(configMap.Object).To(jq.Match(`.metadata.namespace == "opendatahub"`))

		expected, err := toolk8s.DecodeResources([]byte(foreignConfigMapYAML))
		g.Expect(err).To(Succeed())
		g.Expect(expected).To(HaveLen(1))

		foreign := findResource(resources, toolk8s.ConfigMap, foreignConfigMapKey)
		g.Expect(foreign).NotTo(BeNil(), "expected ConfigMap rendered-namespace/odh-synthetic-config in the rendered output")
		g.Expect(*foreign).To(Equal(expected[0]))
	})
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

	input, err := toolk8s.DecodeResources([]byte(malformedDataConfigMapYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("converting platform config ConfigMap for synthetic"))
	g.Expect(resources).To(BeNil())
}

// TestPlatformConfigInjectorLastExactMatchWins pins that the last
// exact identity match of the name in the applications namespace wins
// the merge. The two duplicate identities are inspected through
// disjoint subslices because findResource is first-match: a full-slice
// lookup cannot tell them apart, and the last-wins policy belongs to
// the injector, not the lookup.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (indexConfigMapsByName last-wins), commit 5a1d17f57b32.
func TestPlatformConfigInjectorLastExactMatchWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newPlatformConfigInjector().Run

	batch := strings.Join([]string{
		firstConfigMapYAML,
		secondConfigMapYAML,
	}, "\n---\n")

	input, err := toolk8s.DecodeResources([]byte(batch))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(2))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(2))

	first := findResource(resources[:1], toolk8s.ConfigMap, platformConfigKey)
	g.Expect(first).NotTo(BeNil(), "expected ConfigMap opendatahub/odh-synthetic-config in the first half")
	g.Expect(first).To(WithTransform(k8s.Data(), SatisfyAll(
		HaveKeyWithValue("MODULE_KEY", "first-value"),
		Not(HaveKey("platformVersion")),
	)))

	second := findResource(resources[1:], toolk8s.ConfigMap, platformConfigKey)
	g.Expect(second).NotTo(BeNil(), "expected ConfigMap opendatahub/odh-synthetic-config in the second half")
	g.Expect(second).To(WithTransform(k8s.Data(), SatisfyAll(
		HaveKeyWithValue("MODULE_KEY", "second-value"),
		HaveKeyWithValue("platformVersion", "2.19.0"),
	)))
}

// TestPlatformConfigInjectorExactGVKSelection pins that only the
// object's full identity is a candidate: same-name lookalikes sharing
// the target's namespace and kind but carrying a foreign group or a
// wrong version never shadow the real target, wherever they sit in the
// batch, and pass through untouched with the batch order preserved.
func TestPlatformConfigInjectorExactGVKSelection(t *testing.T) {
	t.Parallel()

	for name, batch := range map[string][]string{
		"lookalikes after the target":  {firstConfigMapYAML, wrongGroupConfigMapYAML, wrongVersionConfigMapYAML},
		"lookalikes before the target": {wrongGroupConfigMapYAML, wrongVersionConfigMapYAML, firstConfigMapYAML},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			var render enginetypes.PostRenderer = newPlatformConfigInjector().Run

			input, err := toolk8s.DecodeResources([]byte(strings.Join(batch, "\n---\n")))
			g.Expect(err).To(Succeed())
			g.Expect(input).To(HaveLen(3))

			resources, err := render(t.Context(), input)
			g.Expect(err).To(Succeed())

			g.Expect(resources).To(HaveLen(3))

			configMap := findResource(resources, toolk8s.ConfigMap, platformConfigKey)
			g.Expect(configMap).NotTo(BeNil(), "expected ConfigMap opendatahub/odh-synthetic-config in the rendered output")
			g.Expect(configMap).To(WithTransform(k8s.Data(), SatisfyAll(
				HaveKeyWithValue("MODULE_KEY", "first-value"),
				HaveKeyWithValue("platformVersion", "2.19.0"),
			)))

			expectedWrongGroup, err := toolk8s.DecodeResources([]byte(wrongGroupConfigMapYAML))
			g.Expect(err).To(Succeed())
			g.Expect(expectedWrongGroup).To(HaveLen(1))

			wrongGroup := findResource(resources, wrongGroupConfigMapGVK, platformConfigKey)
			g.Expect(wrongGroup).NotTo(BeNil(), "expected the config.example.com/v1 ConfigMap lookalike in the rendered output")
			g.Expect(*wrongGroup).To(Equal(expectedWrongGroup[0]))

			expectedWrongVersion, err := toolk8s.DecodeResources([]byte(wrongVersionConfigMapYAML))
			g.Expect(err).To(Succeed())
			g.Expect(expectedWrongVersion).To(HaveLen(1))

			wrongVersion := findResource(resources, wrongVersionConfigMapGVK, platformConfigKey)
			g.Expect(wrongVersion).NotTo(BeNil(), "expected the v2 ConfigMap lookalike in the rendered output")
			g.Expect(*wrongVersion).To(Equal(expectedWrongVersion[0]))
		})
	}
}

// TestPlatformConfigInjectorAppendsWithLookalikeOnly pins that a batch
// containing only a wrong-GVK lookalike keeps it unchanged and appends
// a real core/v1 platform config ConfigMap.
func TestPlatformConfigInjectorAppendsWithLookalikeOnly(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var render enginetypes.PostRenderer = newPlatformConfigInjector().Run

	input, err := toolk8s.DecodeResources([]byte(wrongGroupConfigMapYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	g.Expect(resources).To(HaveLen(2))

	expected, err := toolk8s.DecodeResources([]byte(wrongGroupConfigMapYAML))
	g.Expect(err).To(Succeed())
	g.Expect(expected).To(HaveLen(1))

	wrongGroup := findResource(resources, wrongGroupConfigMapGVK, platformConfigKey)
	g.Expect(wrongGroup).NotTo(BeNil(), "expected the config.example.com/v1 ConfigMap lookalike in the rendered output")
	g.Expect(*wrongGroup).To(Equal(expected[0]))

	configMap := findResource(resources, toolk8s.ConfigMap, platformConfigKey)
	g.Expect(configMap).NotTo(BeNil(), "expected ConfigMap opendatahub/odh-synthetic-config in the rendered output")
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

	input, err := toolk8s.DecodeResources([]byte(moduleConfigMapYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
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

	var render enginetypes.PostRenderer = newPlatformConfigInjector().Run

	input, err := toolk8s.DecodeResources([]byte(gateConfigMapYAML))
	g.Expect(err).To(Succeed())
	g.Expect(input).To(HaveLen(1))

	resources, err := render(t.Context(), input)
	g.Expect(err).To(Succeed())

	// The gate ConfigMap passes through untouched; the only addition
	// is the ordinary platform config ConfigMap.
	g.Expect(resources).To(HaveLen(2))

	expected, err := toolk8s.DecodeResources([]byte(gateConfigMapYAML))
	g.Expect(err).To(Succeed())
	g.Expect(expected).To(HaveLen(1))

	gate := findResource(resources, toolk8s.ConfigMap, types.NamespacedName{Namespace: "opendatahub", Name: "odh-synthetic-upgrade-gate"})
	g.Expect(gate).NotTo(BeNil(), "expected ConfigMap opendatahub/odh-synthetic-upgrade-gate in the rendered output")
	g.Expect(*gate).To(Equal(expected[0]))

	configMap := findResource(resources, toolk8s.ConfigMap, platformConfigKey)
	g.Expect(configMap).NotTo(BeNil(), "expected ConfigMap opendatahub/odh-synthetic-config in the rendered output")
	g.Expect(configMap).To(WithTransform(k8s.Data(),
		HaveKeyWithValue("platformVersion", "2.19.0")))
}
