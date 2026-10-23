package adapt

import (
	"testing"

	. "github.com/onsi/gomega"

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

// TestInjectPlatformConfigCreates pins that the odh-<module>-config
// ConfigMap is appended in the applications namespace with the
// platform version read from the request's release.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (injectPlatformConfig, buildPlatformConfigMap), commit 5a1d17f57b32.
func TestInjectPlatformConfigCreates(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(resource(t, serviceYAML))
	setRelease(t, request, "2.19.0")

	action := InjectPlatformConfig(newDefinition(), "opendatahub")
	g.Expect(action(t.Context(), request)).To(Succeed())

	g.Expect(request.Resources).To(HaveLen(2))

	configMap := findResource(t, request.Resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.apiVersion == "v1"`),
		jq.Match(`.kind == "ConfigMap"`),
		jq.Match(`.metadata.name == "odh-synthetic-config"`),
		jq.Match(`.metadata.namespace == "opendatahub"`),
		jq.Match(`.data.platformVersion == "2.19.0"`),
		jq.Match(`.data | length == 1`),
	))
}

// TestInjectPlatformConfigMergesExisting pins that a module-rendered
// ConfigMap of the platform config name keeps its module-owned keys,
// has its platform version enforced, and has stale platform-managed
// cert-manager keys removed — without a new resource being appended.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (mergePlatformKeys), commit 5a1d17f57b32.
func TestInjectPlatformConfigMergesExisting(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(resource(t, moduleConfigMapYAML))
	setRelease(t, request, "2.20.1")

	action := InjectPlatformConfig(newDefinition(), "opendatahub")
	g.Expect(action(t.Context(), request)).To(Succeed())

	g.Expect(request.Resources).To(HaveLen(1))

	configMap := findResource(t, request.Resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.data.MODULE_KEY == "module-value"`),
		jq.Match(`.data.platformVersion == "2.20.1"`),
		jq.Match(`.data | has("CERT_MANAGER_ISSUER_REF_NAME") | not`),
		jq.Match(`.metadata.namespace == "rendered-namespace"`),
	))
}

// TestInjectPlatformConfigReplacesMalformedData pins that a malformed
// (non-string) data block is replaced by the platform keys alone.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (mergePlatformKeys, ignored NestedStringMap error), commit 5a1d17f57b32.
func TestInjectPlatformConfigReplacesMalformedData(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(resource(t, malformedDataConfigMapYAML))
	setRelease(t, request, "2.20.1")

	action := InjectPlatformConfig(newDefinition(), "opendatahub")
	g.Expect(action(t.Context(), request)).To(Succeed())

	configMap := findResource(t, request.Resources, "ConfigMap", "odh-synthetic-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.data | length == 1`),
		jq.Match(`.data.platformVersion == "2.20.1"`),
		jq.Match(`.data | has("MODULE_KEY") | not`),
	))
}

// TestInjectPlatformConfigIndexKindNameOnly pins that ConfigMaps are
// matched by kind and name only — namespace-insensitive — and that a
// later resource with the same name wins the merge.
//
// Operator source: internal/controller/modules/modules_controller_actions_platform_config.go
// (indexConfigMapsByName), commit 5a1d17f57b32.
func TestInjectPlatformConfigIndexKindNameOnly(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	request := newRequest(
		resource(t, firstConfigMapYAML),
		resource(t, secondConfigMapYAML),
	)
	setRelease(t, request, "2.19.0")

	action := InjectPlatformConfig(newDefinition(), "opendatahub")
	g.Expect(action(t.Context(), request)).To(Succeed())

	g.Expect(request.Resources).To(HaveLen(2))

	first := findResource(t, request.Resources[:1], "ConfigMap", "odh-synthetic-config")
	g.Expect(first.Object).To(SatisfyAll(
		jq.Match(`.data.MODULE_KEY == "first-value"`),
		jq.Match(`.data | has("platformVersion") | not`),
	))

	second := findResource(t, request.Resources[1:], "ConfigMap", "odh-synthetic-config")
	g.Expect(second.Object).To(SatisfyAll(
		jq.Match(`.data.MODULE_KEY == "second-value"`),
		jq.Match(`.data.platformVersion == "2.19.0"`),
	))
}
