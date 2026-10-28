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

package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/blang/semver/v4"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/operator-framework/api/pkg/lib/version"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// definitionUID is the server-assigned identity of the synthetic
// persisted owner.
const definitionUID = "definition-uid"

// defaultFieldManager is the canonical application field manager the
// production caller passes; the service adds no defaulting of its
// own.
const defaultFieldManager = "platform.opendatahub.io"

// payloadCRDYAML is a bundled CRD establishing the synthetic module
// type.
const payloadCRDYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: syntheticmodules.components.platform.opendatahub.io
spec:
  group: components.platform.opendatahub.io
  names:
    kind: SyntheticModule
    plural: syntheticmodules
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
`

// namespaceYAML is the installation namespace every payload shares.
const namespaceYAML = `apiVersion: v1
kind: Namespace
metadata:
  name: modules
`

// configMapYAML is an ordinary namespaced payload object.
const configMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: module-config
  namespace: modules
data:
  key: value
`

// unmanagedConfigMapYAML is the desired form of a live object marked
// unmanaged through the framework's managed-by annotation.
const unmanagedConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: unmanaged-config
  namespace: modules
  annotations:
    opendatahub.io/managed: "false"
data:
  key: value
`

// foreignOwnedConfigMapYAML is the desired form of a live object
// carrying an owner reference of another type.
const foreignOwnedConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: foreign-owned-config
  namespace: modules
data:
  key: value
`

// deploymentYAML is an ordinary Deployment payload object.
const deploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  namespace: modules
spec:
  replicas: 1
`

// gvkConfigMap and gvkCRD are the payload identities the write log
// records and the stored-object probes read.
var (
	gvkConfigMap = schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	gvkCRD       = apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")
)

// newDefinition returns the server-backed persisted owner: real
// identity, real GVK, and the synthetic module type its controller
// owns.
func newDefinition() *toolApi.PlatformModuleDefinition {
	return &toolApi.PlatformModuleDefinition{
		TypeMeta: metav1.TypeMeta{APIVersion: toolApi.APIVersion, Kind: toolApi.Kind},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "synthetic",
			UID:        types.UID(definitionUID),
			Generation: 1,
		},
		Spec: toolApi.PlatformModuleDefinitionSpec{
			Instance: toolApi.Instance{
				Type: toolApi.ModuleType{
					Group:   "components.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "SyntheticModule",
				},
			},
		},
	}
}

// newRelease returns the canonical platform release entry.
func newRelease() api.Release {
	return api.Release{
		Name:    api.ReleasePlatform,
		Version: version.OperatorVersion{Version: semver.MustParse("2.19.0")},
	}
}

// payload converts YAML documents to rendered unstructured objects.
func payload(t *testing.T, g *WithT, documents ...string) []unstructured.Unstructured {
	t.Helper()

	resources := make([]unstructured.Unstructured, 0, len(documents))
	for _, document := range documents {
		object := unstructured.Unstructured{}
		g.Expect(yaml.Unmarshal([]byte(document), &object)).To(Succeed())
		resources = append(resources, object)
	}

	return resources
}

// applyError extracts Apply's shared module error.
func applyError(g *WithT, err error) *moduleerrors.ModuleError {
	g.Expect(err).To(HaveOccurred())

	typed := &moduleerrors.ModuleError{}
	g.Expect(errors.As(err, &typed)).To(BeTrue())

	return typed
}

// testScheme is the connection's scheme: client-go, apiextensions,
// and the module definition API, matching cluster.Connect.
func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	g := NewWithT(t)

	scheme := runtime.NewScheme()
	g.Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	g.Expect(apiextensionsv1.AddToScheme(scheme)).To(Succeed())
	g.Expect(toolApi.AddToScheme(scheme)).To(Succeed())

	return scheme
}

// liveNamespace returns the installation namespace with
// server-assigned identity.
func liveNamespace() *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: "modules", UID: types.UID("modules-uid")},
	}
}

// liveConfigMap returns a live core/v1 ConfigMap in the module
// namespace with server-assigned identity; mutate adjusts the
// framework-relevant live state.
func liveConfigMap(name string, mutate func(*corev1.ConfigMap)) *corev1.ConfigMap {
	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       "modules",
			UID:             types.UID(name + "-uid"),
			ResourceVersion: "2",
		},
		Data: map[string]string{"key": "value"},
	}

	if mutate != nil {
		mutate(configMap)
	}

	return configMap
}

// foreignOwnerReference is an owner reference of another type on a
// live object.
func foreignOwnerReference() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       "foreign-owner",
		UID:        "foreign-owner-uid",
	}
}

// liveTunedDeployment returns a live Deployment the platform operator
// did not scale: its replica count differs from the rendered desired
// state.
func liveTunedDeployment() *appsv1.Deployment {
	tuned := int32(5)

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            "synthetic-controller",
			Namespace:       "modules",
			UID:             types.UID("deployment-uid"),
			ResourceVersion: "7",
		},
		Spec: appsv1.DeploymentSpec{Replicas: &tuned},
	}
}

// writeLog records every mutating client call, in order, as
// "<operation> <gvk> <namespace>/<name>".
type writeLog struct {
	mu      sync.Mutex
	entries []string
	applies []applyOption
}

// record appends one recorded operation.
func (l *writeLog) record(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, entry)
}

// snapshot returns the recorded operations in order.
func (l *writeLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.entries...)
}

// applyOption is one captured mutating write's effective request
// options: the recorded operation identity with the field manager and
// force flag the interceptor observed before delegation.
type applyOption struct {
	id      string
	manager string
	force   bool
}

// applyOptions returns the captured apply request options in order.
func (l *writeLog) applyOptions() []applyOption {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]applyOption(nil), l.applies...)
}

// recordApply appends one captured apply with its effective options.
func (l *writeLog) recordApply(option applyOption) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.applies = append(l.applies, option)
}

// count returns how many recorded operations mention the value.
func (l *writeLog) count(value string) int {
	count := 0
	for _, entry := range l.snapshot() {
		if strings.Contains(entry, value) {
			count++
		}
	}

	return count
}

// identity renders a recorded operation identity.
func identity(gvk schema.GroupVersionKind, namespace string, name string) string {
	if namespace == "" {
		return gvk.String() + " " + name
	}

	return gvk.String() + " " + namespace + "/" + name
}

// fakeCluster builds an intercepted client around the
// controller-runtime fake client: it seeds live objects, records
// every mutating call in order, and injects failures by object
// identity. Fake-client results exercise the action's orchestration,
// not real server-side-apply or admission semantics — the Kind suite
// covers those.
type fakeCluster struct {
	scheme  *runtime.Scheme
	objects []crclient.Object

	// applyErrors maps a recorded identity to an error Apply returns.
	applyErrors map[string]error

	// transientApplyFailures maps a recorded identity to the number
	// of its leading Apply calls that fail; later calls delegate. The
	// countdown is derived from the mutex-guarded write log, so it is
	// safe under the action's own concurrency.
	transientApplyFailures map[string]int

	// getErrors maps a recorded identity to an error Get returns.
	getErrors map[string]error
}

// connect builds the intercepted client and its write log.
func (f *fakeCluster) connect(t *testing.T) (crclient.Client, *writeLog) {
	t.Helper()

	log := &writeLog{}

	objectIdentity := func(obj crclient.Object) string {
		gvk, err := apiutil.GVKForObject(obj, f.scheme)
		if err != nil {
			t.Fatal(err)
		}

		return identity(gvk, obj.GetNamespace(), obj.GetName())
	}

	applyIdentity := func(obj runtime.ApplyConfiguration) string {
		encoded, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}

		var probe unstructured.Unstructured
		if err := json.Unmarshal(encoded, &probe); err != nil {
			t.Fatal(err)
		}

		return identity(probe.GroupVersionKind(), probe.GetNamespace(), probe.GetName())
	}

	funcs := interceptor.Funcs{
		Apply: func(
			ctx context.Context,
			c crclient.WithWatch,
			obj runtime.ApplyConfiguration,
			opts ...crclient.ApplyOption,
		) error {
			id := applyIdentity(obj)

			// The effective request options are captured before
			// delegation: the field manager and force flag the
			// framework action actually sent.
			effective := &crclient.ApplyOptions{}
			for _, opt := range opts {
				opt.ApplyToApply(effective)
			}
			force := effective.Force != nil && *effective.Force
			log.record("apply " + id)
			log.recordApply(applyOption{id: id, manager: effective.FieldManager, force: force})

			if fails, injected := f.transientApplyFailures[id]; injected && log.count("apply "+id) <= fails {
				return apierrors.NewInternalError(errors.New("transient apply failure"))
			}

			if err, injected := f.applyErrors[id]; injected {
				return err
			}

			return c.Apply(ctx, obj, opts...)
		},
		Create: func(ctx context.Context, c crclient.WithWatch, obj crclient.Object, opts ...crclient.CreateOption) error {
			log.record("create " + objectIdentity(obj))
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c crclient.WithWatch, obj crclient.Object, opts ...crclient.UpdateOption) error {
			log.record("update " + objectIdentity(obj))
			return c.Update(ctx, obj, opts...)
		},
		Patch: func(
			ctx context.Context,
			c crclient.WithWatch,
			obj crclient.Object,
			patch crclient.Patch,
			opts ...crclient.PatchOption,
		) error {
			log.record("patch " + objectIdentity(obj))
			return c.Patch(ctx, obj, patch, opts...)
		},
		Get: func(ctx context.Context, c crclient.WithWatch, key crclient.ObjectKey, obj crclient.Object, opts ...crclient.GetOption) error {
			// The lookup probe arrives unpopulated; its identity is
			// the requested key plus the probe's type.
			gvk, err := apiutil.GVKForObject(obj, f.scheme)
			if err != nil {
				t.Fatal(err)
			}

			if injected, ok := f.getErrors[identity(gvk, key.Namespace, key.Name)]; ok {
				return injected
			}

			return c.Get(ctx, key, obj, opts...)
		},
	}

	builder := fake.NewClientBuilder().
		WithScheme(f.scheme).
		WithInterceptorFuncs(funcs)
	if len(f.objects) > 0 {
		builder = builder.WithObjects(f.objects...)
	}

	return builder.Build(), log
}

// payloadTarget returns a Deployer holding the minimum receiver state
// the private payload method reads: the client, the persisted owner,
// the release, the rendered payload, and the field manager.
func payloadTarget(
	client crclient.Client,
	owner *toolApi.PlatformModuleDefinition,
	release api.Release,
	resources []unstructured.Unstructured,
	fieldManager string,
) *Deployer {
	return &Deployer{
		client:       client,
		owner:        owner,
		release:      release,
		resources:    resources,
		fieldManager: fieldManager,
	}
}

// stored reads one live object back through the intercepted client.
func stored(
	t *testing.T,
	client crclient.Client,
	gvk schema.GroupVersionKind,
	namespace string,
	name string,
) unstructured.Unstructured {
	t.Helper()

	g := NewWithT(t)

	probe := &unstructured.Unstructured{}
	probe.SetGroupVersionKind(gvk)

	err := client.Get(t.Context(), crclient.ObjectKey{Namespace: namespace, Name: name}, probe)
	g.Expect(err).NotTo(HaveOccurred())

	return *probe
}

// moduleConfigMapIdentity is the ordinary payload object's recorded
// identity.
func moduleConfigMapIdentity() string {
	return identity(gvkConfigMap, "modules", "module-config")
}

// payloadCRDIdentity is the bundled CRD's recorded identity.
func payloadCRDIdentity() string {
	return identity(gvkCRD, "", "syntheticmodules.components.platform.opendatahub.io")
}

// TestApplyAppliesWholePayloadThroughFrameworkDefaults proves the
// minimal flow end to end with the actual framework action over the
// fake client: the persisted definition is the live instance — its
// identity and the canonical release land on every ordinary object as
// instance annotations and a controller reference — and the action's
// own default apply-order sort places the bundled CRD ahead of the
// ordinary payload regardless of payload order. CRDs never receive
// owner references.
func TestApplyAppliesWholePayloadThroughFrameworkDefaults(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
	}
	connection, writes := harness.connect(t)

	// The ConfigMap precedes the CRD in the payload: the action's
	// own sort, not input order, drives the writes.
	resources := payload(t, g, configMapYAML, payloadCRDYAML)

	g.Expect(payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager).deployPayload(t.Context())).To(Succeed())

	g.Expect(writes.snapshot()).To(Equal([]string{
		"apply " + payloadCRDIdentity(),
		"apply " + moduleConfigMapIdentity(),
	}))

	configMap := stored(t, connection, gvkConfigMap, "modules", "module-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.metadata.annotations["platform.opendatahub.io/instance.name"] == "synthetic"`),
		jq.Match(`.metadata.annotations["platform.opendatahub.io/instance.uid"] == "definition-uid"`),
		jq.Match(`.metadata.annotations["platform.opendatahub.io/type"] == "platform"`),
		jq.Match(`.metadata.annotations["platform.opendatahub.io/version"] == "2.19.0"`),
		jq.Match(`.metadata.labels["platform.opendatahub.io/part-of"] == "platform.opendatahub.io"`),
		jq.Match(`.metadata.ownerReferences[0].kind == "PlatformModuleDefinition"`),
		jq.Match(`.metadata.ownerReferences[0].uid == "definition-uid"`),
		jq.Match(`.metadata.ownerReferences[0].controller == true`),
		jq.Match(`.data.key == "value"`),
	))

	crd := stored(t, connection, gvkCRD, "", "syntheticmodules.components.platform.opendatahub.io")
	g.Expect(crd.Object).To(SatisfyAll(
		jq.Match(`.metadata.labels["platform.opendatahub.io/part-of"] == "platform"`),
		jq.Match(`.metadata | has("ownerReferences") | not`),
	))
}

// TestApplyUsesGivenFieldManagerOnOrdinaryPayloadOnly proves the
// captured request options carry the invocation's manager with force
// ownership on the ordinary payload apply, while the bundled payload
// CRD keeps the framework's fixed platform manager — and that the
// manager-derived fallback part-of label lands only on the ordinary
// object, the CRD keeping its own fallback.
func TestApplyUsesGivenFieldManagerOnOrdinaryPayloadOnly(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
	}
	connection, writes := harness.connect(t)

	resources := payload(t, g, configMapYAML, payloadCRDYAML)

	g.Expect(payloadTarget(connection, newDefinition(), newRelease(), resources, "team.example.deployer").deployPayload(t.Context())).
		To(Succeed())

	g.Expect(writes.applyOptions()).To(Equal([]applyOption{
		{id: payloadCRDIdentity(), manager: "platform.opendatahub.io", force: true},
		{id: moduleConfigMapIdentity(), manager: "team.example.deployer", force: true},
	}))

	configMap := stored(t, connection, gvkConfigMap, "modules", "module-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.metadata.labels["platform.opendatahub.io/part-of"] == "team.example.deployer"`),
		jq.Match(`.metadata.ownerReferences[0].controller == true`),
	))

	crd := stored(t, connection, gvkCRD, "", "syntheticmodules.components.platform.opendatahub.io")
	g.Expect(crd.Object).To(SatisfyAll(
		jq.Match(`.metadata.labels["platform.opendatahub.io/part-of"] == "platform"`),
		jq.Match(`.metadata | has("ownerReferences") | not`),
	))
}

// TestApplyPreservesAuthoredPartOfLabel proves a nonempty authored
// part-of label survives the manager-derived fallback: the fallback
// applies only when the label is absent or empty.
func TestApplyPreservesAuthoredPartOfLabel(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
	}
	connection, _ := harness.connect(t)

	authored := `apiVersion: v1
kind: ConfigMap
metadata:
  name: module-config
  namespace: modules
  labels:
    platform.opendatahub.io/part-of: authored-value
data:
  key: value
`

	empty := `apiVersion: v1
kind: ConfigMap
metadata:
  name: empty-label
  namespace: modules
  labels:
    platform.opendatahub.io/part-of: ""
data:
  key: value
`

	resources := payload(t, g, authored, empty)

	g.Expect(payloadTarget(connection, newDefinition(), newRelease(), resources, "team.example.deployer").deployPayload(t.Context())).
		To(Succeed())

	authoredMap := stored(t, connection, gvkConfigMap, "modules", "module-config")
	g.Expect(authoredMap.Object).To(
		jq.Match(`.metadata.labels["platform.opendatahub.io/part-of"] == "authored-value"`))

	emptyMap := stored(t, connection, gvkConfigMap, "modules", "empty-label")
	g.Expect(emptyMap.Object).To(
		jq.Match(`.metadata.labels["platform.opendatahub.io/part-of"] == "team.example.deployer"`))
}

// TestApplyRequiresFieldManager proves an empty service argument is a
// rejected preflight — the safe preparation sentinel with the cause
// reachable — before any client access, never a silent fallback to
// the framework's owner-kind default.
func TestApplyRequiresFieldManager(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{scheme: testScheme(t)}
	connection, writes := harness.connect(t)

	err := payloadTarget(connection, newDefinition(), newRelease(), payload(t, g, configMapYAML), "").deployPayload(t.Context())

	typed := applyError(g, err)
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployPrepare)).To(BeTrue())
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployApply)).To(BeFalse())
	g.Expect(typed.Error()).To(Equal(prepareFailureMessage))
	g.Expect(errors.Is(typed.Unwrap(), errNilArgument)).To(BeTrue())
	g.Expect(writes.snapshot()).To(BeEmpty())
}

// TestApplyFieldManagerIsPerInvocation proves two invocations over
// separate connections with different managers each send their own
// manager: no action state leaks between invocations.
func TestApplyFieldManagerIsPerInvocation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	first := &fakeCluster{scheme: testScheme(t), objects: []crclient.Object{liveNamespace()}}
	firstConnection, firstWrites := first.connect(t)
	g.Expect(payloadTarget(firstConnection, newDefinition(), newRelease(),
		payload(t, g, configMapYAML), "first.example").deployPayload(t.Context())).To(Succeed())

	second := &fakeCluster{scheme: testScheme(t), objects: []crclient.Object{liveNamespace()}}
	secondConnection, secondWrites := second.connect(t)
	g.Expect(payloadTarget(secondConnection, newDefinition(), newRelease(),
		payload(t, g, configMapYAML), "second.example").deployPayload(t.Context())).To(Succeed())

	g.Expect(firstWrites.applyOptions()).To(ContainElement(
		applyOption{id: moduleConfigMapIdentity(), manager: "first.example", force: true}))
	g.Expect(secondWrites.applyOptions()).To(ContainElement(
		applyOption{id: moduleConfigMapIdentity(), manager: "second.example", force: true}))
}

// TestApplyNormalizesOwnerRuntimeType proves an owner whose TypeMeta
// the caller left empty is normalized from the connection scheme
// before the action runs: the controller reference still names the
// persisted definition kind and identity.
func TestApplyNormalizesOwnerRuntimeType(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
	}
	connection, _ := harness.connect(t)

	owner := newDefinition()
	owner.TypeMeta = metav1.TypeMeta{}

	g.Expect(payloadTarget(connection, owner, newRelease(), payload(t, g, configMapYAML), defaultFieldManager).deployPayload(t.Context())).To(Succeed())

	configMap := stored(t, connection, gvkConfigMap, "modules", "module-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.metadata.ownerReferences[0].kind == "PlatformModuleDefinition"`),
		jq.Match(`.metadata.ownerReferences[0].uid == "definition-uid"`),
		jq.Match(`.metadata.annotations["platform.opendatahub.io/instance.uid"] == "definition-uid"`),
	))
}

// TestApplyNeverOwnsNamespaces proves rendered Namespaces pass through
// the action like the operator passes them: enriched with the
// platform metadata but never carrying controller references, and
// sorted ahead of the bundled CRD.
func TestApplyNeverOwnsNamespaces(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
	}
	connection, writes := harness.connect(t)

	resources := payload(t, g, payloadCRDYAML, namespaceYAML)

	g.Expect(payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager).deployPayload(t.Context())).To(Succeed())

	// The action's sort places the Namespace first.
	g.Expect(writes.snapshot()).To(Equal([]string{
		"apply " + identity(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, "", "modules"),
		"apply " + payloadCRDIdentity(),
	}))

	namespace := stored(t, connection, schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, "", "modules")
	g.Expect(namespace.Object).To(SatisfyAll(
		jq.Match(`.metadata.annotations["platform.opendatahub.io/instance.name"] == "synthetic"`),
		jq.Match(`.metadata | has("ownerReferences") | not`),
	))
}

// TestApplyCreatesAbsentRenderedNamespace proves a rendered Namespace
// the cluster does not have reaches the framework apply like any other
// payload object: there is no namespace-existence probe, the action's
// own default sort still places it first, the apply creates it, and it
// never receives a controller reference.
func TestApplyCreatesAbsentRenderedNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{scheme: testScheme(t)}
	connection, writes := harness.connect(t)

	// The ConfigMap and the CRD precede the Namespace in the payload:
	// the action's own sort, not input order, drives the writes.
	resources := payload(t, g, configMapYAML, payloadCRDYAML, namespaceYAML)

	g.Expect(payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager).deployPayload(t.Context())).To(Succeed())

	g.Expect(writes.snapshot()).To(Equal([]string{
		"apply " + identity(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, "", "modules"),
		"apply " + payloadCRDIdentity(),
		"apply " + moduleConfigMapIdentity(),
	}))

	namespace := stored(t, connection, schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, "", "modules")
	g.Expect(namespace.Object).To(SatisfyAll(
		jq.Match(`.metadata.annotations["platform.opendatahub.io/instance.name"] == "synthetic"`),
		jq.Match(`.metadata | has("ownerReferences") | not`),
	))
}

// TestApplyApplyErrorFailsStageWithFirstReport proves a payload write
// the server refuses fails the apply stage with the framework's own
// first report reachable through Unwrap, while the public message
// stays the safe stage text; writes that preceded the failure stand,
// so the failure is reported as partial state. The schedule is
// shortened to a single attempt so the test observes exactly the
// first report; the retry behavior has its own tests.
func TestApplyApplyErrorFailsStageWithFirstReport(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
		applyErrors: map[string]error{
			moduleConfigMapIdentity(): apierrors.NewForbidden(
				schema.GroupResource{Group: "", Resource: "configmaps"},
				"module-config",
				errors.New("secret-detail"),
			),
		},
	}
	connection, writes := harness.connect(t)

	resources := payload(t, g, payloadCRDYAML, configMapYAML)

	target := payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager)
	target.payloadRetryBackoff = []time.Duration{}

	err := target.deployPayload(t.Context())

	typed := applyError(g, err)
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployApply)).To(BeTrue())
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployPrepare)).To(BeFalse())
	g.Expect(typed.Error()).To(Equal(applyFailureMessage))
	g.Expect(typed.Error()).NotTo(ContainSubstring("Forbidden"))
	g.Expect(typed.Error()).NotTo(ContainSubstring("secret-detail"))
	g.Expect(apierrors.IsForbidden(typed.Unwrap())).To(BeTrue())

	// The CRD write succeeded before the ordinary payload failed.
	g.Expect(writes.count("apply " + payloadCRDIdentity())).To(Equal(1))
	crd := stored(t, connection, gvkCRD, "", "syntheticmodules.components.platform.opendatahub.io")
	g.Expect(crd.Object).To(jq.Match(`.metadata.labels["platform.opendatahub.io/part-of"] == "platform"`))
}

// TestApplyLookupErrorFailsStageWithFirstReport proves a read the
// server refuses during the action's own lookup fails the apply stage
// with the framework's continue-on-error first report; no classifier
// of our own sits in front of it. The schedule is shortened to a
// single attempt so the test observes exactly the first report.
func TestApplyLookupErrorFailsStageWithFirstReport(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
		getErrors: map[string]error{
			moduleConfigMapIdentity(): apierrors.NewServiceUnavailable("unavailable-detail"),
		},
	}
	connection, _ := harness.connect(t)

	resources := payload(t, g, payloadCRDYAML, configMapYAML)

	target := payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager)
	target.payloadRetryBackoff = []time.Duration{}

	err := target.deployPayload(t.Context())

	typed := applyError(g, err)
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployApply)).To(BeTrue())
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployPrepare)).To(BeFalse())
	g.Expect(typed.Error()).To(Equal(applyFailureMessage))
	g.Expect(typed.Error()).NotTo(ContainSubstring("unavailable-detail"))
	g.Expect(apierrors.IsServiceUnavailable(typed.Unwrap())).To(BeTrue())
}

// TestApplyRetriesTransientActionFailure proves a payload write that
// fails once and then succeeds does not fail the stage: the whole
// action runs again, reapplying the already-successful resources, and
// the payload ends fully applied.
func TestApplyRetriesTransientActionFailure(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
		transientApplyFailures: map[string]int{
			moduleConfigMapIdentity(): 1,
		},
	}
	connection, writes := harness.connect(t)

	resources := payload(t, g, payloadCRDYAML, configMapYAML)

	target := payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager)
	target.payloadRetryBackoff = []time.Duration{time.Millisecond}

	g.Expect(target.deployPayload(t.Context())).To(Succeed())

	// The failed write was attempted again and succeeded, and the
	// retry reapplied the already-successful CRD too.
	g.Expect(writes.count("apply " + moduleConfigMapIdentity())).To(Equal(2))
	g.Expect(writes.count("apply " + payloadCRDIdentity())).To(Equal(2))

	configMap := stored(t, connection, gvkConfigMap, "modules", "module-config")
	g.Expect(configMap.Object).To(jq.Match(`.data["key"] == "value"`))
}

// TestApplyRetriesPersistentFailureUntilExhausted proves a payload
// write the server keeps refusing consumes exactly the settled five
// attempts and then fails the stage with the last action error as the
// cause, with the public message staying the safe stage text.
func TestApplyRetriesPersistentFailureUntilExhausted(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
		applyErrors: map[string]error{
			moduleConfigMapIdentity(): apierrors.NewForbidden(
				schema.GroupResource{Group: "", Resource: "configmaps"},
				"module-config",
				errors.New("secret-detail"),
			),
		},
	}
	connection, writes := harness.connect(t)

	resources := payload(t, g, payloadCRDYAML, configMapYAML)

	target := payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager)
	target.payloadRetryBackoff = []time.Duration{
		time.Millisecond,
		time.Millisecond,
		time.Millisecond,
		time.Millisecond,
	}

	err := target.deployPayload(t.Context())

	typed := applyError(g, err)
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployApply)).To(BeTrue())
	g.Expect(typed.Error()).To(Equal(applyFailureMessage))
	g.Expect(typed.Error()).NotTo(ContainSubstring("secret-detail"))
	g.Expect(apierrors.IsForbidden(typed.Unwrap())).To(BeTrue())

	// Five attempts in total: the first plus one after each of the
	// four pauses.
	g.Expect(writes.count("apply " + moduleConfigMapIdentity())).To(Equal(5))
}

// TestApplyStopsRetryingWhenBackoffIsCancelled proves the invocation's
// deadline observed during a backoff wait stops the retry loop
// without a further attempt, reporting the cancellation identity
// through the cause chain under the safe stage text.
func TestApplyStopsRetryingWhenBackoffIsCancelled(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
		applyErrors: map[string]error{
			moduleConfigMapIdentity(): apierrors.NewInternalError(errors.New("broken")),
		},
	}
	connection, writes := harness.connect(t)

	resources := payload(t, g, payloadCRDYAML, configMapYAML)

	target := payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager)
	// One long pause: the invocation deadline fires inside it.
	target.payloadRetryBackoff = []time.Duration{20 * time.Second}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	err := target.deployPayload(ctx)

	typed := applyError(g, err)
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployApply)).To(BeTrue())
	g.Expect(typed.Error()).To(Equal(applyFailureMessage))
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())

	// Exactly one attempt ran; the backoff wait consumed the rest.
	g.Expect(writes.count("apply " + moduleConfigMapIdentity())).To(Equal(1))
}

// TestApplyPreservesLiveDeploymentTuning proves the framework's
// default Deployment customizer keeps live tuning: the controller
// reference and instance metadata still land, but the live replica
// count survives the apply.
func TestApplyPreservesLiveDeploymentTuning(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace(), liveTunedDeployment()},
	}
	connection, _ := harness.connect(t)

	resources := payload(t, g, payloadCRDYAML, deploymentYAML)

	g.Expect(payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager).deployPayload(t.Context())).To(Succeed())

	deployment := stored(
		t,
		connection,
		appsv1.SchemeGroupVersion.WithKind("Deployment"),
		"modules",
		"synthetic-controller",
	)
	g.Expect(deployment.Object).To(SatisfyAll(
		jq.Match(`.spec.replicas == 5`),
		jq.Match(`.metadata.ownerReferences[0].kind == "PlatformModuleDefinition"`),
		jq.Match(`.metadata.ownerReferences[0].uid == "definition-uid"`),
	))
}

// TestApplySkipsUnmanagedResources proves an object the framework
// marks unmanaged is read and left alone: no write touches it, and
// the deploy still succeeds.
func TestApplySkipsUnmanagedResources(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme: testScheme(t),
		objects: []crclient.Object{
			liveNamespace(),
			liveConfigMap("unmanaged-config", func(configMap *corev1.ConfigMap) {
				configMap.Annotations = map[string]string{"opendatahub.io/managed": "false"}
			}),
		},
	}
	connection, writes := harness.connect(t)

	resources := payload(t, g, payloadCRDYAML, unmanagedConfigMapYAML)

	g.Expect(payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager).deployPayload(t.Context())).To(Succeed())

	// No write touched the unmanaged object; only the CRD applied.
	g.Expect(writes.count("unmanaged-config")).To(Equal(0))
	g.Expect(writes.snapshot()).To(Equal([]string{"apply " + payloadCRDIdentity()}))
}

// TestApplyStripsForeignOwnerReferences proves owner-reference
// stripping is the action's own operator behavior: a live object
// carrying a foreign reference is stripped of it, then adopted by the
// persisted owner through a controller reference.
func TestApplyStripsForeignOwnerReferences(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme: testScheme(t),
		objects: []crclient.Object{
			liveNamespace(),
			liveConfigMap("foreign-owned-config", func(configMap *corev1.ConfigMap) {
				configMap.OwnerReferences = []metav1.OwnerReference{foreignOwnerReference()}
			}),
		},
	}
	connection, writes := harness.connect(t)

	resources := payload(t, g, payloadCRDYAML, foreignOwnedConfigMapYAML)

	g.Expect(payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager).deployPayload(t.Context())).To(Succeed())

	// The foreign reference was stripped through an update, then the
	// object was adopted through the apply.
	g.Expect(writes.count("update " + identity(gvkConfigMap, "modules", "foreign-owned-config"))).To(Equal(1))
	g.Expect(writes.count("apply " + identity(gvkConfigMap, "modules", "foreign-owned-config"))).To(Equal(1))

	configMap := stored(t, connection, gvkConfigMap, "modules", "foreign-owned-config")
	g.Expect(configMap.Object).To(SatisfyAll(
		jq.Match(`.metadata.ownerReferences | length == 1`),
		jq.Match(`.metadata.ownerReferences[0].kind == "PlatformModuleDefinition"`),
		jq.Match(`.metadata.ownerReferences[0].uid == "definition-uid"`),
		jq.Match(`.metadata.ownerReferences[0] | has("name")`),
	))
}

// TestApplySecondInvocationRewrites proves no cache survives between
// invocations: a second Apply builds its own action and cache, so the
// same payload is applied again rather than skipped.
func TestApplySecondInvocationRewrites(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{
		scheme:  testScheme(t),
		objects: []crclient.Object{liveNamespace()},
	}
	connection, writes := harness.connect(t)

	resources := payload(t, g, payloadCRDYAML, configMapYAML)

	g.Expect(payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager).deployPayload(t.Context())).To(Succeed())
	firstWrites := writes.count("apply " + moduleConfigMapIdentity())

	g.Expect(payloadTarget(connection, newDefinition(), newRelease(), resources, defaultFieldManager).deployPayload(t.Context())).To(Succeed())

	g.Expect(writes.count("apply " + moduleConfigMapIdentity())).To(Equal(2 * firstWrites))
}

// TestApplyRequiresLiveOwner proves nil arguments fail the deploy as a
// rejected preflight with the sentinel reachable through Unwrap.
func TestApplyRequiresLiveOwner(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := payloadTarget(nil, nil, newRelease(), nil, defaultFieldManager).deployPayload(t.Context())

	typed := applyError(g, err)
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployPrepare)).To(BeTrue())
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployApply)).To(BeFalse())
	g.Expect(typed.Error()).To(Equal(prepareFailureMessage))
	g.Expect(errors.Is(typed.Unwrap(), errNilArgument)).To(BeTrue())
}

// TestApplyCancelledInvocationIsNotAttempted proves a cancelled
// invocation fails before any request with the cancellation identity
// preserved through the typed error's cause chain.
func TestApplyCancelledInvocationIsNotAttempted(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	harness := &fakeCluster{scheme: testScheme(t)}
	connection, writes := harness.connect(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := payloadTarget(connection, newDefinition(), newRelease(), payload(t, g, configMapYAML), defaultFieldManager).deployPayload(ctx)

	typed := applyError(g, err)
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployPrepare)).To(BeTrue())
	g.Expect(errors.Is(typed, moduleerrors.ErrDeployApply)).To(BeFalse())
	g.Expect(errors.Is(typed.Unwrap(), context.Canceled)).To(BeTrue())
	g.Expect(writes.snapshot()).To(BeEmpty())
}
