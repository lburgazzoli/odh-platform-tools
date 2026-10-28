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
	"testing"
	"time"

	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// prepareEstablishedStatusJSON is the status a served CRD carries:
// established with its names accepted.
const prepareEstablishedStatusJSON = `{"conditions":[{"type":"Established","status":"True"},{"type":"NamesAccepted","status":"True"}]}
`

// prepareApplyOption is one captured preparation apply's effective
// request options: the recorded write identity with the field manager
// and force flag observed before delegation.
type prepareApplyOption struct {
	id      string
	manager string
	force   bool
}

// Recorded apply-identity substrings of the two preparation writes.
const (
	prepareCRDWrite        = "Kind=CustomResourceDefinition"
	prepareDefinitionWrite = "Kind=PlatformModuleDefinition synthetic"

	// prepareDefinitionIdentity is the definition apply's full
	// recorded identity, whose deterministic server UID the
	// assertions expect.
	prepareDefinitionIdentity = "config.opendatahub.io/v1alpha1, Kind=PlatformModuleDefinition synthetic"
)

// Recorded apply-identity substrings of the compose payload writes.
const (
	preparePayloadDeploymentWrite = "Kind=Deployment run-namespace/synthetic-controller"
	preparePayloadConfigMapWrite  = "Kind=ConfigMap run-namespace/odh-synthetic-config"
)

// waitFrame is one scripted GET reaction: either an error, or a live
// object map returned as the observation. The last frame of a
// sequence repeats.
type waitFrame struct {
	err  error
	live map[string]any
}

// prepareCluster is the intercepted fake client the deployer tests
// drive every remote operation through: every apply is recorded in
// order with its request body and effective request options, a served
// CRD receives the establishment status a real server maintains,
// newly created objects receive deterministic server UIDs stamped into
// the apply response — which the framework helper converts back into
// the typed object, exactly as a server echoes its assigned identity
// — and scripted GET frames replace lookups of workload objects the
// cluster holds. Frames and the observation log are keyed by full
// identity (GVK, namespace, name) and consulted only after the
// delegated lookup succeeds, so the framework deploy action's
// pre-apply lookups of objects the cluster does not hold keep the
// fake client's real NotFound behavior and never consume a rollout
// frame, and a same-named object of another kind never collides with
// a scripted frame. The residual caveat: a pre-apply lookup of an
// already-applied scripted object — a re-deploy — would consume that
// object's next frame; no current scenario re-deploys with scripted
// frames. Fake-client results exercise the request sequence and
// identity rules, not real SSA admission semantics.
type prepareCluster struct {
	client crclient.Client

	applies  []string
	requests []map[string]any
	gets     []string
	getCtxs  []context.Context

	// applyCtxs records the invocation context of every apply, in
	// order, before delegation.
	applyCtxs []context.Context

	// applyOptions captures each recorded apply's effective field
	// manager and force flag, in order, before delegation.
	applyOptions []prepareApplyOption

	// frames maps one full object identity to its scripted
	// observation sequence for unstructured lookups.
	frames map[string][]waitFrame

	// failApply maps one identity substring to the error its apply
	// returns.
	failApply map[string]error

	// applyHook, when set, runs after one successful apply of the
	// matching identity, with the invocation context.
	applyHook func(ctx context.Context, id string)

	// crdNeverEstablished suppresses the establishment status, so the
	// preparation's real CRD wait polls until the context expires.
	crdNeverEstablished bool
}

// newPrepareCluster builds the intercepted fake client.
func newPrepareCluster(t *testing.T) *prepareCluster {
	t.Helper()

	fixture := &prepareCluster{
		frames:    map[string][]waitFrame{},
		failApply: map[string]error{},
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := toolApi.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	// applyIdentity renders the recorded identity of one apply, its
	// unstructured probe, and its captured request body.
	applyIdentity := func(obj runtime.ApplyConfiguration) (string, unstructured.Unstructured, map[string]any) {
		encoded, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}

		var probe unstructured.Unstructured
		if err := json.Unmarshal(encoded, &probe); err != nil {
			t.Fatal(err)
		}

		var request map[string]any
		if err := json.Unmarshal(encoded, &request); err != nil {
			t.Fatal(err)
		}

		return prepareIdentity(probe.GroupVersionKind(), probe.GetNamespace(), probe.GetName()), probe, request
	}

	// assign simulates the server assigning identity: after the fake
	// merge stores the apply result, the UID is stamped into the
	// apply response — which shares the unstructured the framework
	// helper converts back into the typed object — and into the
	// stored copy, so re-applies and read-backs keep one identity.
	// SSA requests stay UID-free, as real ones must.
	assign := func(
		c crclient.WithWatch,
		obj runtime.ApplyConfiguration,
		id string,
		merged unstructured.Unstructured,
	) {
		if merged.GetUID() != "" {
			return
		}

		metadata, _ := merged.Object["metadata"].(map[string]any)
		if metadata == nil {
			metadata = map[string]any{}
			merged.Object["metadata"] = metadata
		}
		metadata["uid"] = prepareServerUID(id)

		encoded, err := json.Marshal(merged.Object)
		if err != nil {
			t.Fatal(err)
		}

		// The apply response carries the assigned identity.
		if err := json.Unmarshal(encoded, obj); err != nil {
			t.Fatal(err)
		}

		// So does the stored copy — the identity assignment a real
		// server performs inside the apply itself. Kinds the fake
		// client treats with a status subresource, like the
		// definition CRD, need the status written through the status
		// subresource: a plain update would drop it. The status write
		// runs first and refreshes the resource version, so the
		// identity update stays conflict-free.
		stored := &unstructured.Unstructured{}
		if err := json.Unmarshal(encoded, stored); err != nil {
			t.Fatal(err)
		}

		if merged.GetKind() == "CustomResourceDefinition" && merged.Object["status"] != nil {
			if err := c.Status().Update(context.Background(), stored); err != nil {
				t.Fatal(err)
			}
		}

		if err := c.Update(context.Background(), stored); err != nil {
			t.Fatal(err)
		}
	}

	funcs := interceptor.Funcs{
		Apply: func(
			ctx context.Context,
			c crclient.WithWatch,
			obj runtime.ApplyConfiguration,
			opts ...crclient.ApplyOption,
		) error {
			id, probe, request := applyIdentity(obj)
			fixture.applies = append(fixture.applies, id)
			fixture.requests = append(fixture.requests, request)
			fixture.applyCtxs = append(fixture.applyCtxs, ctx)

			// The effective request options are captured before
			// delegation: the field manager and force flag the
			// apply actually sent.
			effective := &crclient.ApplyOptions{}
			for _, opt := range opts {
				opt.ApplyToApply(effective)
			}
			force := effective.Force != nil && *effective.Force
			fixture.applyOptions = append(fixture.applyOptions,
				prepareApplyOption{id: id, manager: effective.FieldManager, force: force})

			for key, injected := range fixture.failApply {
				if strings.Contains(id, key) {
					return injected
				}
			}

			if err := c.Apply(ctx, obj, opts...); err != nil {
				return err
			}

			// The fake's apply response carries the merged stored
			// object; simulate the server's identity assignment over
			// it.
			encoded, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}

			var merged unstructured.Unstructured
			if err := json.Unmarshal(encoded, &merged); err != nil {
				t.Fatal(err)
			}

			if merged.GroupVersionKind().Empty() {
				merged.SetGroupVersionKind(probe.GroupVersionKind())
			}

			// A served CRD is reported established with its names
			// accepted — the status a real server maintains — unless
			// the fixture suppresses it to hold the real wait.
			if merged.GetKind() == "CustomResourceDefinition" && !fixture.crdNeverEstablished {
				var crdStatus map[string]any
				if err := json.Unmarshal([]byte(prepareEstablishedStatusJSON), &crdStatus); err != nil {
					t.Fatal(err)
				}

				merged.Object["status"] = crdStatus
			}

			assign(c, obj, id, merged)

			if fixture.applyHook != nil {
				fixture.applyHook(ctx, id)
			}

			return nil
		},
		Get: func(
			ctx context.Context,
			c crclient.WithWatch,
			key crclient.ObjectKey,
			obj crclient.Object,
			opts ...crclient.GetOption,
		) error {
			// Typed lookups — the CRD establishment poll — delegate
			// untouched: only unstructured workload probes take part
			// in frame scripting.
			live, isUnstructured := obj.(*unstructured.Unstructured)
			if !isUnstructured {
				return c.Get(ctx, key, obj, opts...)
			}

			id := prepareIdentity(live.GroupVersionKind(), key.Namespace, key.Name)

			// The lookup delegates first: the framework deploy
			// action's pre-apply probes of objects the cluster does
			// not hold keep the fake client's real NotFound behavior
			// and never consume a rollout frame.
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}

			frames := fixture.frames[id]
			if len(frames) == 0 {
				return nil
			}

			frame := frames[0]
			if len(frames) > 1 {
				fixture.frames[id] = frames[1:]
			}

			// Only scripted observations reach the log: pre-apply
			// probes and unscripted read-backs do not count as wait
			// observations.
			fixture.gets = append(fixture.gets, id)
			fixture.getCtxs = append(fixture.getCtxs, ctx)

			if frame.err != nil {
				return frame.err
			}

			live.Object = frame.live

			return nil
		},
	}

	fixture.client = fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(funcs).
		Build()

	return fixture
}

// prepareIdentity renders one recorded operation identity.
func prepareIdentity(gvk schema.GroupVersionKind, namespace string, name string) string {
	if namespace == "" {
		return gvk.String() + " " + name
	}

	return gvk.String() + " " + namespace + "/" + name
}

// waitTargetIdentity renders the full recorded identity of one waited
// apps/v1 controller Deployment.
func waitTargetIdentity(namespace, name string) string {
	return prepareIdentity(deploymentGVKForFrames, namespace, name)
}

// deploymentGVKForFrames is the waited workload identity.
var deploymentGVKForFrames = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

// prepareServerUID renders the deterministic server identity of one
// object identity.
func prepareServerUID(id string) string {
	return "server-uid-" + strings.ReplaceAll(
		strings.ReplaceAll(id, "/", "-"),
		" ", "-",
	)
}

// applyIndex returns how many recorded applies carry the given
// substring, and the index of the first one.
func (f *prepareCluster) applyIndex(substr string) (int, int) {
	count, first := 0, -1
	for i, apply := range f.applies {
		if strings.Contains(apply, substr) {
			count++
			if first < 0 {
				first = i
			}
		}
	}

	return count, first
}

// storedDefinition reads the persisted definition back through the
// fixture client.
func (f *prepareCluster) storedDefinition(t *testing.T) unstructured.Unstructured {
	t.Helper()

	probe := &unstructured.Unstructured{}
	probe.SetGroupVersionKind(definitionGVK)

	if err := f.client.Get(t.Context(), crclient.ObjectKey{Name: "synthetic"}, probe); err != nil {
		t.Fatal(err)
	}

	return *probe
}

// stored reads one live object back through the fixture client.
func (f *prepareCluster) stored(
	t *testing.T,
	gvk schema.GroupVersionKind,
	namespace string,
	name string,
) unstructured.Unstructured {
	t.Helper()

	probe := &unstructured.Unstructured{}
	probe.SetGroupVersionKind(gvk)

	if err := f.client.Get(t.Context(), crclient.ObjectKey{Namespace: namespace, Name: name}, probe); err != nil {
		t.Fatal(err)
	}

	return *probe
}

// definitionGVK is the persisted definition's identity.
var definitionGVK = schema.GroupVersionKind{
	Group:   "config.opendatahub.io",
	Version: "v1alpha1",
	Kind:    "PlatformModuleDefinition",
}

// payloadGVKs are the compose payload identities the assertions read
// back.
var deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

// markerError is a distinctive cause for safe-text assertions.
type markerError struct {
	marker string
}

// Error returns the distinctive marker.
func (e *markerError) Error() string { return e.marker }

// newPrepareTarget returns a Deployer holding the authored definition
// and the effective field manager, over the fixture client: the
// minimum receiver state the preparation reads.
func newPrepareTarget(
	fixture *prepareCluster,
	definition *toolApi.PlatformModuleDefinition,
	fieldManager string,
) *Deployer {
	return &Deployer{
		options:    Options{FieldManager: fieldManager},
		client:     fixture.client,
		definition: definition,
	}
}

// TestPrepareOwnerAppliesCRDThenDefinition proves the preparation
// performs exactly two server-side applies in order — the embedded
// definition CRD, then the definition itself — and returns the
// server-backed identity the apply response assigned.
func TestPrepareOwnerAppliesCRDThenDefinition(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	definition := testDefinition()

	target := newPrepareTarget(fixture, &definition, DefaultFieldManager)

	owner, err := target.publishDefinition(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	crdApplies, crdFirst := fixture.applyIndex(prepareCRDWrite)
	definitionApplies, definitionFirst := fixture.applyIndex(prepareDefinitionWrite)
	g.Expect(fixture.applies).To(HaveLen(2))
	g.Expect(crdApplies).To(Equal(1))
	g.Expect(definitionApplies).To(Equal(1))
	g.Expect(definitionFirst).To(BeNumerically(">", crdFirst))

	// The returned owner is the server-backed object: the authored
	// name with the identity the apply response carried.
	g.Expect(owner).NotTo(BeNil())
	g.Expect(owner.Name).To(Equal("synthetic"))
	g.Expect(string(owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))
	g.Expect(owner.Namespace).To(BeEmpty())

	// The stored definition carries the same identity and the
	// authored, unexpanded spec.
	stored := fixture.storedDefinition(t)
	g.Expect(string(stored.GetUID())).To(Equal(prepareServerUID(prepareDefinitionIdentity)))
	g.Expect(stored.Object).To(SatisfyAll(
		jq.Match(`.spec.manifests.format == "helm"`),
		jq.Match(`.spec.controller.deployment.container.image == "{{RELATED_IMAGE_CONTROLLER}}"`),
	))
}

// TestPrepareOwnerUsesGivenFieldManager proves both preparation
// writes — the embedded definition CRD and the definition itself —
// claim their fields under the invocation's manager with force
// ownership, whatever that manager is.
func TestPrepareOwnerUsesGivenFieldManager(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	definition := testDefinition()

	target := newPrepareTarget(fixture, &definition, "team.example.deployer")

	_, err := target.publishDefinition(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(fixture.applyOptions).To(HaveLen(2))
	for _, captured := range fixture.applyOptions {
		g.Expect(captured.manager).To(Equal("team.example.deployer"))
		g.Expect(captured.force).To(BeTrue())
	}
}

// TestPrepareOwnerDefaultManagerIsPlatformIdentity proves the default
// request resolves to the canonical platform manager on both
// preparation writes.
func TestPrepareOwnerDefaultManagerIsPlatformIdentity(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	definition := testDefinition()

	target := newPrepareTarget(fixture, &definition, DefaultFieldManager)

	_, err := target.publishDefinition(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(fixture.applyOptions).To(HaveLen(2))
	for _, captured := range fixture.applyOptions {
		g.Expect(captured.manager).To(Equal("platform.opendatahub.io"))
		g.Expect(captured.force).To(BeTrue())
	}
}

// TestPrepareOwnerSanitizesDefinitionApply proves forged authority
// fields never reach the definition apply request: only the authored
// name, labels, annotations, and spec are submitted — no UID,
// resourceVersion, namespace, owner references, finalizers, or
// status. The receiver's authored definition keeps its forged values:
// sanitization operates on a copy.
func TestPrepareOwnerSanitizesDefinitionApply(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)

	definition := testDefinition()
	definition.UID = "forged-uid"
	definition.ResourceVersion = "forged-version"
	definition.Namespace = "forged-namespace"
	definition.OwnerReferences = []metav1.OwnerReference{{UID: "forged-owner"}}
	definition.Finalizers = []string{"forged.example.com/finalizer"}
	definition.Labels = map[string]string{"authored": "label"}
	definition.Annotations = map[string]string{"authored": "annotation"}
	definition.Status = toolApi.PlatformModuleDefinitionStatus{
		Status: api.Status{Phase: "forged", ObservedGeneration: 7},
	}

	target := newPrepareTarget(fixture, &definition, DefaultFieldManager)

	owner, err := target.publishDefinition(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	_, definitionFirst := fixture.applyIndex(prepareDefinitionWrite)
	request := fixture.requests[definitionFirst]

	g.Expect(request).To(SatisfyAll(
		jq.Match(`.metadata.uid == null`),
		jq.Match(`.metadata.resourceVersion == null`),
		jq.Match(`.metadata.namespace == null`),
		jq.Match(`.metadata.ownerReferences == null`),
		jq.Match(`.metadata.finalizers == null`),
		jq.Match(`.metadata.name == "synthetic"`),
		jq.Match(`.metadata.labels.authored == "label"`),
		jq.Match(`.metadata.annotations.authored == "annotation"`),
		jq.Match(`.status == null`),
		jq.Match(`.spec.controller.deployment.container.image == "{{RELATED_IMAGE_CONTROLLER}}"`),
	))

	// The authored definition the receiver holds is untouched: the
	// sanitized object is the applied copy, and the confirmed owner
	// carries the server identity instead.
	g.Expect(target.definition.UID).To(Equal(types.UID("forged-uid")))
	g.Expect(target.definition.Namespace).To(Equal("forged-namespace"))
	g.Expect(owner.UID).To(Equal(types.UID(prepareServerUID(prepareDefinitionIdentity))))
}

// TestPrepareOwnerWaitsForEstablishment proves the definition apply
// happens only after the CRD serves: a CRD that never establishes
// holds the preparation until the context expires, with exactly one
// CRD apply and no definition apply.
func TestPrepareOwnerWaitsForEstablishment(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.crdNeverEstablished = true

	definition := testDefinition()
	target := newPrepareTarget(fixture, &definition, DefaultFieldManager)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	owner, err := target.publishDefinition(ctx)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(owner).To(BeNil())

	crdApplies, _ := fixture.applyIndex(prepareCRDWrite)
	definitionApplies, _ := fixture.applyIndex(prepareDefinitionWrite)
	g.Expect(crdApplies).To(Equal(1))
	g.Expect(definitionApplies).To(Equal(0))
}

// TestPrepareOwnerFailuresAreSafeAndBounded proves a failed
// preparation returns no owner, stops before the later writes, keeps
// the raw cause reachable through Unwrap, and never echoes the
// server's own error text into the public message.
func TestPrepareOwnerFailuresAreSafeAndBounded(t *testing.T) {
	t.Parallel()

	t.Run("crd apply failure stops before the definition", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		cause := apierrors.NewForbidden(
			schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
			toolApi.CRDName, &markerError{marker: "PREPARE-MARKER-crd"})

		fixture := newPrepareCluster(t)
		fixture.failApply[prepareCRDWrite] = cause

		definition := testDefinition()
		target := newPrepareTarget(fixture, &definition, DefaultFieldManager)

		owner, err := target.publishDefinition(t.Context())
		g.Expect(err).To(HaveOccurred())
		g.Expect(owner).To(BeNil())

		g.Expect(err.Error()).To(SatisfyAll(
			ContainSubstring("applying the definition custom resource definition"),
			Not(ContainSubstring("PREPARE-MARKER-crd")),
		))

		var status *apierrors.StatusError
		g.Expect(errors.As(err, &status)).To(BeTrue())
		g.Expect(apierrors.IsForbidden(err)).To(BeTrue())
		g.Expect(errors.Is(err, moduleerrors.ErrOwnerPreparation)).To(BeTrue())
		g.Expect(errors.Is(err, moduleerrors.ErrDeployPrepare)).To(BeFalse())

		definitionApplies, _ := fixture.applyIndex(prepareDefinitionWrite)
		g.Expect(definitionApplies).To(Equal(0))
	})

	t.Run("definition apply failure returns no owner", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		cause := &markerError{marker: "PREPARE-MARKER-definition"}

		fixture := newPrepareCluster(t)
		fixture.failApply[prepareDefinitionWrite] = cause

		definition := testDefinition()
		target := newPrepareTarget(fixture, &definition, DefaultFieldManager)

		owner, err := target.publishDefinition(t.Context())
		g.Expect(err).To(HaveOccurred())
		g.Expect(owner).To(BeNil())

		g.Expect(err.Error()).To(SatisfyAll(
			ContainSubstring("applying the module definition"),
			Not(ContainSubstring("PREPARE-MARKER-definition")),
		))

		var marker *markerError
		g.Expect(errors.As(err, &marker)).To(BeTrue())
		g.Expect(errors.Is(err, moduleerrors.ErrOwnerPreparation)).To(BeTrue())
		g.Expect(errors.Is(err, moduleerrors.ErrRolloutWait)).To(BeFalse())
	})
}
