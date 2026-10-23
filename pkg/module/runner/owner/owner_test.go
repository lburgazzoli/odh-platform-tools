package owner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
)

// requestDecoder decodes protobuf request bodies — the apiextensions
// typed client prefers protobuf on the wire — through the real
// apimachinery serializer, so the fake can store them as JSON.
var requestDecoder = newRequestDecoder()

// newRequestDecoder builds the protobuf request decoder over a scheme
// holding every type the run writes.
func newRequestDecoder() runtime.Decoder {
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}

	return protobuf.NewSerializer(scheme, scheme)
}

// ownerKubeconfigYAML is a standalone kubeconfig pointing at an
// unreachable server; the connection's ConfigFlags override carries
// the fake server address.
const ownerKubeconfigYAML = `apiVersion: v1
kind: Config
clusters:
- name: test-cluster
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
current-context: test-context
users:
- name: test-user
  user: {}
`

// ownerAPIVersionsJSON is the core /api discovery document.
const ownerAPIVersionsJSON = `{"kind":"APIVersions","versions":["v1"],"serverAddressByClientCIDRs":[]}
`

// ownerCoreV1ResourcesJSON is the core /api/v1 discovery document.
const ownerCoreV1ResourcesJSON = `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"v1","resources":[{"name":"namespaces","singularName":"namespace","namespaced":false,"kind":"Namespace","verbs":["get","list"]}]}
`

// ownerGroupsJSON is the /apis discovery document serving the
// apiextensions and module definition groups.
const ownerGroupsJSON = `{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"apiextensions.k8s.io","versions":[{"groupVersion":"apiextensions.k8s.io/v1","version":"v1"}],"preferredVersion":{"groupVersion":"apiextensions.k8s.io/v1","version":"v1"}},{"name":"config.opendatahub.io","versions":[{"groupVersion":"config.opendatahub.io/v1alpha1","version":"v1alpha1"}],"preferredVersion":{"groupVersion":"config.opendatahub.io/v1alpha1","version":"v1alpha1"}}]}
`

// ownerAPIExtensionsResourcesJSON is the apiextensions group's
// resource discovery document.
const ownerAPIExtensionsResourcesJSON = `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"apiextensions.k8s.io/v1","resources":[{"name":"customresourcedefinitions","singularName":"customresourcedefinition","namespaced":false,"kind":"CustomResourceDefinition","verbs":["get","list","watch","create","update","patch","delete"]}]}
`

// ownerDefinitionsResourcesJSON is the module definition group's
// resource discovery document.
const ownerDefinitionsResourcesJSON = `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"config.opendatahub.io/v1alpha1","resources":[{"name":"platformmoduledefinitions","singularName":"platformmoduledefinition","namespaced":false,"kind":"PlatformModuleDefinition","verbs":["get","list","watch","create","update","patch","delete"]}]}
`

// ownerCRDEstablishedStatusJSON is the status a served CRD carries:
// established with its names accepted.
const ownerCRDEstablishedStatusJSON = `{"conditions":[{"type":"Established","status":"True"},{"type":"NamesAccepted","status":"True"}]}
`

// fakeOwnerServer is a minimal API server double for the owner: core,
// apiextensions, and module definition discovery; CRD and definition
// storage with UID/resourceVersion assignment, conflict injection,
// raced-deletion simulation, and lost create/update responses that
// abort the connection after the write is stored. It records every
// submitted definition write body.
type fakeOwnerServer struct {
	*httptest.Server

	loseCreate  bool
	loseUpdate  bool
	vanishOnGet bool

	forbidDefinitionCreate bool
	conflicts              int

	// raceCRDCreation simulates another writer creating the CRD
	// between the missing read and our creation attempt: reads report
	// it missing until the raced creation is refused.
	raceCRDCreation string

	mu               sync.Mutex
	crds             map[string]string
	crdWrites        int
	definitions      map[string]string
	definitionWrites map[string]int
	updateAttempts   map[string]int
	createBodies     map[string][]string
	updateBodies     map[string][]string
	requests         int
}

// newFakeOwnerServer starts the double; the server closes with the
// test.
func newFakeOwnerServer(t *testing.T) *fakeOwnerServer {
	t.Helper()

	fake := &fakeOwnerServer{
		crds:             map[string]string{},
		definitions:      map[string]string{},
		definitionWrites: map[string]int{},
		updateAttempts:   map[string]int{},
		createBodies:     map[string][]string{},
		updateBodies:     map[string][]string{},
	}
	fake.Server = httptest.NewServer(fake)

	t.Cleanup(fake.Close)

	return fake
}

// seedCRD stores a live CRD body under its name.
func (s *fakeOwnerServer) seedCRD(t *testing.T, body string) {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	var object map[string]any
	if err := json.Unmarshal([]byte(body), &object); err != nil {
		t.Fatal(err)
	}

	s.crds[object["metadata"].(map[string]any)["name"].(string)] = body
}

// seedDefinition stores a live definition body under its name.
func (s *fakeOwnerServer) seedDefinition(t *testing.T, body string) {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	var object map[string]any
	if err := json.Unmarshal([]byte(body), &object); err != nil {
		t.Fatal(err)
	}

	s.definitions[object["metadata"].(map[string]any)["name"].(string)] = body
}

// storedDefinition returns the stored definition body for one name.
func (s *fakeOwnerServer) storedDefinition(t *testing.T, name string) map[string]any {
	t.Helper()

	s.mu.Lock()
	body := s.definitions[name]
	s.mu.Unlock()

	var object map[string]any
	if err := json.Unmarshal([]byte(body), &object); err != nil {
		t.Fatal(err)
	}

	return object
}

// definitionWriteCount returns how many writes (creates plus updates)
// the server applied across all definitions.
func (s *fakeOwnerServer) definitionWriteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	writes := 0
	for _, count := range s.definitionWrites {
		writes += count
	}

	return writes
}

// definitionCreateBodies returns the submitted create bodies for one
// definition name.
func (s *fakeOwnerServer) definitionCreateBodies(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.createBodies[name]...)
}

// definitionUpdateBodies returns the submitted update bodies for one
// definition name.
func (s *fakeOwnerServer) definitionUpdateBodies(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.updateBodies[name]...)
}

// definitionUpdateAttemptCount returns how many update requests —
// applied or conflict-refused — one definition name received.
func (s *fakeOwnerServer) definitionUpdateAttemptCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.updateAttempts[name]
}

// crdWriteCount returns how many CRD writes the server received.
func (s *fakeOwnerServer) crdWriteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.crdWrites
}

// storedCRD returns the stored CRD body for one name.
func (s *fakeOwnerServer) storedCRD(t *testing.T, name string) string {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.crds[name]
}

// requestCount returns how many requests the server received.
func (s *fakeOwnerServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.requests
}

// resetCounters drops the recorded write bodies and counters, keeping
// the stored objects.
func (s *fakeOwnerServer) resetCounters() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.createBodies = map[string][]string{}
	s.updateBodies = map[string][]string{}
	s.definitionWrites = map[string]int{}
	s.updateAttempts = map[string]int{}
}

// ServeHTTP implements http.Handler.
func (s *fakeOwnerServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	s.mu.Unlock()

	writeJSON := func(code int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}

	writeStatus := func(code int, reason string) {
		writeJSON(code, fmt.Sprintf(
			`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"%s","reason":"%s","code":%d}`,
			reason, reason, code,
		))
	}

	readObject := func() (string, map[string]any) {
		raw, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			panic(http.ErrAbortHandler)
		}

		if r.Header.Get("Content-Type") == "application/vnd.kubernetes.protobuf" {
			decoded, _, err := requestDecoder.Decode(raw, nil, nil)
			if err != nil {
				panic(http.ErrAbortHandler)
			}

			encoded, err := json.Marshal(decoded)
			if err != nil {
				panic(http.ErrAbortHandler)
			}

			raw = encoded
		}

		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			panic(http.ErrAbortHandler)
		}

		encoded, err := json.Marshal(object)
		if err != nil {
			panic(http.ErrAbortHandler)
		}

		return string(encoded), object
	}

	metadata := func(object map[string]any) map[string]any {
		meta, _ := object["metadata"].(map[string]any)

		return meta
	}

	assignIdentity := func(object map[string]any, uid string, resourceVersion string) {
		meta := metadata(object)
		if meta == nil {
			meta = map[string]any{}
			object["metadata"] = meta
		}
		meta["uid"] = uid
		meta["resourceVersion"] = resourceVersion
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api":
		writeJSON(http.StatusOK, ownerAPIVersionsJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1":
		writeJSON(http.StatusOK, ownerCoreV1ResourcesJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/apis":
		writeJSON(http.StatusOK, ownerGroupsJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/apis/apiextensions.k8s.io/v1":
		writeJSON(http.StatusOK, ownerAPIExtensionsResourcesJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/apis/config.opendatahub.io/v1alpha1":
		writeJSON(http.StatusOK, ownerDefinitionsResourcesJSON)

	case r.Method == http.MethodPost && r.URL.Path == "/apis/apiextensions.k8s.io/v1/customresourcedefinitions":
		_, object := readObject()

		s.mu.Lock()
		raced := s.raceCRDCreation
		s.raceCRDCreation = ""
		s.mu.Unlock()

		if raced != "" {
			// Another writer created it first: our creation is
			// refused and theirs is what the server stores.
			s.mu.Lock()
			s.crds[metadata(object)["name"].(string)] = raced
			s.mu.Unlock()

			writeStatus(http.StatusConflict, "AlreadyExists")

			return
		}

		var status map[string]any
		if err := json.Unmarshal([]byte(ownerCRDEstablishedStatusJSON), &status); err != nil {
			panic(http.ErrAbortHandler)
		}
		object["status"] = status
		assignIdentity(object, "crd-uid", "1")

		encoded, err := json.Marshal(object)
		if err != nil {
			panic(http.ErrAbortHandler)
		}

		s.mu.Lock()
		s.crds[metadata(object)["name"].(string)] = string(encoded)
		s.crdWrites++
		s.mu.Unlock()

		writeJSON(http.StatusCreated, string(encoded))

	case r.Method == http.MethodGet &&
		strings.HasPrefix(r.URL.Path, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/"):
		name := strings.TrimPrefix(r.URL.Path, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/")

		s.mu.Lock()
		body := s.crds[name]
		s.mu.Unlock()

		if body == "" {
			writeStatus(http.StatusNotFound, "NotFound")

			return
		}

		writeJSON(http.StatusOK, body)

	case r.Method == http.MethodPost && r.URL.Path == "/apis/config.opendatahub.io/v1alpha1/platformmoduledefinitions":
		body, object := readObject()
		name := metadata(object)["name"].(string)

		s.mu.Lock()
		_, exists := s.definitions[name]
		s.mu.Unlock()

		if exists {
			writeStatus(http.StatusConflict, "AlreadyExists")

			return
		}

		if s.forbidDefinitionCreate {
			writeStatus(http.StatusForbidden, "Forbidden")

			return
		}

		assignIdentity(object, definitionUID, "1")
		encoded, err := json.Marshal(object)
		if err != nil {
			panic(http.ErrAbortHandler)
		}

		s.mu.Lock()
		s.definitions[name] = string(encoded)
		s.createBodies[name] = append(s.createBodies[name], body)
		s.definitionWrites[name]++
		lose := s.loseCreate
		s.mu.Unlock()

		if lose {
			// The write is stored but the response never arrives.
			panic(http.ErrAbortHandler)
		}

		writeJSON(http.StatusCreated, string(encoded))

	case r.Method == http.MethodGet &&
		strings.HasPrefix(r.URL.Path, "/apis/config.opendatahub.io/v1alpha1/platformmoduledefinitions/"):
		name := strings.TrimPrefix(r.URL.Path, "/apis/config.opendatahub.io/v1alpha1/platformmoduledefinitions/")

		s.mu.Lock()
		vanish := s.vanishOnGet
		if vanish {
			s.vanishOnGet = false
			delete(s.definitions, name)
		}
		body := s.definitions[name]
		s.mu.Unlock()

		if body == "" {
			writeStatus(http.StatusNotFound, "NotFound")

			return
		}

		writeJSON(http.StatusOK, body)

	case r.Method == http.MethodPut &&
		strings.HasPrefix(r.URL.Path, "/apis/config.opendatahub.io/v1alpha1/platformmoduledefinitions/"):
		name := strings.TrimPrefix(r.URL.Path, "/apis/config.opendatahub.io/v1alpha1/platformmoduledefinitions/")
		body, object := readObject()

		s.mu.Lock()
		stored := s.definitions[name]
		conflicts := s.conflicts
		s.updateAttempts[name]++
		s.mu.Unlock()

		if stored == "" {
			writeStatus(http.StatusNotFound, "NotFound")

			return
		}

		var storedObject map[string]any
		if err := json.Unmarshal([]byte(stored), &storedObject); err != nil {
			panic(http.ErrAbortHandler)
		}

		if conflicts > 0 {
			s.mu.Lock()
			s.conflicts--
			s.mu.Unlock()

			writeStatus(http.StatusConflict, "Conflict")

			return
		}

		if metadata(object)["resourceVersion"] != metadata(storedObject)["resourceVersion"] {
			writeStatus(http.StatusConflict, "Conflict")

			return
		}

		version := 1
		if current, ok := metadata(storedObject)["resourceVersion"].(string); ok {
			if parsed, err := strconv.Atoi(current); err == nil {
				version = parsed
			}
		}
		assignIdentity(object, metadata(storedObject)["uid"].(string), fmt.Sprintf("%d", version+1))

		encoded, err := json.Marshal(object)
		if err != nil {
			panic(http.ErrAbortHandler)
		}

		s.mu.Lock()
		s.definitions[name] = string(encoded)
		s.updateBodies[name] = append(s.updateBodies[name], body)
		s.definitionWrites[name]++
		lose := s.loseUpdate
		s.mu.Unlock()

		if lose {
			// The write is stored but the response never arrives.
			panic(http.ErrAbortHandler)
		}

		writeJSON(http.StatusOK, string(encoded))

	default:
		writeStatus(http.StatusNotFound, "NotFound")
	}
}

// definitionUID is the server-assigned identity of the synthetic
// definition.
const definitionUID = "definition-uid"

// connected returns a connection to the fake server through standard
// ConfigFlags: a real kubeconfig on disk with the server address
// applied as a standard override.
func connected(
	invocation context.Context,
	t *testing.T,
	g *WithT,
	server *fakeOwnerServer,
) *cluster.Connection {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	g.Expect(os.WriteFile(path, []byte(ownerKubeconfigYAML), 0o600)).To(Succeed())

	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path

	serverURL := server.URL
	flags.APIServer = &serverURL

	connection, err := cluster.Connect(invocation, flags)
	g.Expect(err).To(Succeed())

	return connection
}

// ensured runs Ensure against the fake server.
func ensured(
	ctx context.Context,
	t *testing.T,
	g *WithT,
	server *fakeOwnerServer,
	definition v1alpha1.PlatformModuleDefinition,
) (*v1alpha1.PlatformModuleDefinition, error) {
	t.Helper()

	connection := connected(ctx, t, g, server)

	return Ensure(ctx, connection, definition)
}

// newAuthoredDefinition returns the definition exactly as the loader
// hands it to the runner: validated envelope, placement defaults
// applied, literal placeholders and arbitrary Helm values preserved.
func newAuthoredDefinition() v1alpha1.PlatformModuleDefinition {
	return v1alpha1.PlatformModuleDefinition{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind},
		ObjectMeta: metav1.ObjectMeta{
			Name:        "synthetic",
			Labels:      map[string]string{"module": "synthetic"},
			Annotations: map[string]string{"note": "authored"},
		},
		Spec: v1alpha1.PlatformModuleDefinitionSpec{
			Instance: v1alpha1.Instance{
				Name: "synthetic",
				Type: v1alpha1.ModuleType{
					Group:   "components.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "SyntheticModule",
				},
			},
			Manifests: v1alpha1.Manifests{
				Format: v1alpha1.FormatHelm,
				Helm: &v1alpha1.HelmManifests{
					Path: "./manifests/chart",
					Values: map[string]apiextensionsv1.JSON{
						"replicas": {Raw: []byte(`2`)},
						"nested":   {Raw: []byte(`{"enabled":true,"items":["one",null,3]}`)},
						"unset":    {Raw: []byte(`null`)},
					},
				},
			},
			RelatedImages: []string{"RELATED_IMAGE_OPERAND"},
			Controller: v1alpha1.Controller{
				Deployment: v1alpha1.ControllerDeployment{
					Name: "synthetic-controller",
					Container: &v1alpha1.ContainerDefinition{
						Name:  "manager",
						Image: "{{RELATED_IMAGE_CONTROLLER}}",
						Env: []v1alpha1.EnvVar{
							{Name: "LOG_LEVEL", Value: new("debug")},
							{Name: "UNSET_FLAG", Value: nil},
						},
					},
					InitContainer: &v1alpha1.ContainerTarget{
						Name: "copy-manifests",
					},
				},
			},
		},
	}
}

// newLiveDefinition returns the server-backed definition a previous
// run published: the authored object carrying the server identity,
// controller-written status, and unrelated live metadata.
func newLiveDefinition() *v1alpha1.PlatformModuleDefinition {
	live := newAuthoredDefinition()
	live.UID = definitionUID
	live.ResourceVersion = "1"
	live.Status = v1alpha1.PlatformModuleDefinitionStatus{
		Status: api.Status{Phase: "Ready", ObservedGeneration: 7},
	}
	live.Labels["unrelated.opendatahub.io/live"] = "preserved"
	live.Annotations["unrelated.opendatahub.io/live"] = "preserved"

	return &live
}

// marshalDefinition encodes a definition as the server-side JSON
// body.
func marshalDefinition(t *testing.T, g *WithT, definition *v1alpha1.PlatformModuleDefinition) string {
	t.Helper()

	encoded, err := json.Marshal(definition)
	g.Expect(err).To(Succeed())

	return string(encoded)
}

// ownerError asserts the error is a typed owner error and returns it.
func ownerError(g *WithT, err error) *Error {
	var typed *Error
	g.Expect(errors.As(err, &typed)).To(BeTrue())
	g.Expect(typed).NotTo(BeNil())

	return typed
}

// TestEnsureRejectsEnvelopeBeforeAnyWrite proves an unsupported or
// missing envelope is an operational failure that happens before any
// bootstrap write: no request leaves the connection.
func TestEnsureRejectsEnvelopeBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	cases := map[string]v1alpha1.PlatformModuleDefinition{
		"wrong api version": func() v1alpha1.PlatformModuleDefinition {
			definition := newAuthoredDefinition()
			definition.APIVersion = "config.opendatahub.io/v1beta1"

			return definition
		}(),
		"wrong kind": func() v1alpha1.PlatformModuleDefinition {
			definition := newAuthoredDefinition()
			definition.Kind = "Module"

			return definition
		}(),
		"missing envelope": func() v1alpha1.PlatformModuleDefinition {
			definition := newAuthoredDefinition()
			definition.TypeMeta = metav1.TypeMeta{}

			return definition
		}(),
	}

	for name, definition := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)
			server := newFakeOwnerServer(t)

			owner, err := ensured(t.Context(), t, g, server, definition)

			g.Expect(owner).To(BeNil())

			typed := ownerError(g, err)
			g.Expect(typed.Outcome).To(Equal(OutcomeNotAttempted))
			g.Expect(typed.Owner).To(BeNil())
			g.Expect(typed.Error()).To(ContainSubstring("definition envelope"))
			g.Expect(typed.Unwrap()).To(MatchError(errUnsupportedEnvelope))
			g.Expect(server.requestCount()).To(Equal(0))
		})
	}
}

// TestEnsureFirstPublicationReturnsServerIdentity proves the first
// run creates the definition behind a real server identity: the
// returned owner carries the assigned UID and runtime GVK, and the
// submitted create body publishes exactly the authored name, labels,
// annotations, and normalized unexpanded spec — never forged
// authority fields.
func TestEnsureFirstPublicationReturnsServerIdentity(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)

	definition := newAuthoredDefinition()
	definition.UID = "forged-uid"
	definition.ResourceVersion = "999"
	definition.Namespace = "forged-namespace"
	definition.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "v1", Kind: "Pod", Name: "forged", UID: "forged-owner",
	}}
	definition.Finalizers = []string{"forged.opendatahub.io/finalizer"}

	owner, err := ensured(t.Context(), t, g, server, definition)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(owner.UID).To(Equal(types.UID(definitionUID)))
	g.Expect(owner.ResourceVersion).To(Equal("1"))
	g.Expect(owner.TypeMeta).To(Equal(metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind}))
	g.Expect(server.definitionWriteCount()).To(Equal(1))
	g.Expect(server.crdWriteCount()).To(Equal(1))

	bodies := server.definitionCreateBodies("synthetic")
	g.Expect(bodies).To(HaveLen(1))
	g.Expect(bodies[0]).To(SatisfyAll(
		jq.Match(`.metadata.name == "synthetic"`),
		jq.Match(`.metadata.labels.module == "synthetic"`),
		jq.Match(`.metadata.annotations.note == "authored"`),
		jq.Match(`.metadata | has("uid") | not`),
		jq.Match(`.metadata | has("resourceVersion") | not`),
		jq.Match(`.metadata | has("namespace") | not`),
		jq.Match(`.metadata | has("ownerReferences") | not`),
		jq.Match(`.metadata | has("finalizers") | not`),
		jq.Match(`.spec.instance.name == "synthetic"`),
		jq.Match(`.spec.manifests.format == "helm"`),
		jq.Match(`.spec.manifests.helm.values.replicas == 2`),
		jq.Match(`.spec.manifests.helm.values.nested.enabled == true`),
		jq.Match(`.spec.manifests.helm.values.nested.items == ["one", null, 3]`),
		jq.Match(`.spec.manifests.helm.values.unset == null`),
		jq.Match(`.spec.controller.deployment.container.image == "{{RELATED_IMAGE_CONTROLLER}}"`),
		jq.Match(`.spec.controller.deployment.container.env[0].value == "debug"`),
		jq.Match(`.spec.controller.deployment.container.env[1].value == null`),
	))
}

// TestEnsureRepeatPublicationAlreadyMatching proves a re-run of an
// unchanged definition reuses the published identity with no further
// writes: the create is refused as already existing and the read
// confirms the desired spec.
func TestEnsureRepeatPublicationAlreadyMatching(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)

	first, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())
	g.Expect(err).NotTo(HaveOccurred())

	server.resetCounters()

	second, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(second.UID).To(Equal(first.UID))
	g.Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
	g.Expect(second.TypeMeta).To(Equal(metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind}))
	g.Expect(server.definitionWriteCount()).To(Equal(0))
	g.Expect(server.definitionCreateBodies("synthetic")).To(BeEmpty())
	g.Expect(server.definitionUpdateBodies("synthetic")).To(BeEmpty())
}

// TestEnsureUpdatesChangedSpecPreservingIdentity proves a changed
// authored spec publishes through a merged update: the live identity,
// status, and unrelated metadata are preserved, the previously
// unbound definition is reused without any binding annotation, and
// arbitrary Helm values and null env entries survive the update.
func TestEnsureUpdatesChangedSpecPreservingIdentity(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.seedDefinition(t, marshalDefinition(t, g, newLiveDefinition()))

	changed := newAuthoredDefinition()
	changed.Spec.Manifests.Helm.Values["replicas"] = apiextensionsv1.JSON{Raw: []byte(`3`)}
	changed.Spec.Manifests.Helm.Values["added"] = apiextensionsv1.JSON{Raw: []byte(`{"list":[1,null,"three"]}`)}
	changed.Spec.Controller.Deployment.Container.Env = append(
		changed.Spec.Controller.Deployment.Container.Env,
		v1alpha1.EnvVar{Name: "EXTRA", Value: new("value")},
	)

	owner, err := ensured(t.Context(), t, g, server, changed)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(owner.UID).To(Equal(types.UID(definitionUID)))
	g.Expect(owner.ResourceVersion).To(Equal("2"))
	g.Expect(owner.Status.Phase).To(Equal("Ready"))

	g.Expect(server.definitionWriteCount()).To(Equal(1))

	bodies := server.definitionUpdateBodies("synthetic")
	g.Expect(bodies).To(HaveLen(1))
	g.Expect(bodies[0]).To(SatisfyAll(
		jq.Match(`.metadata.uid == "definition-uid"`),
		jq.Match(`.metadata.resourceVersion == "1"`),
		jq.Match(`.metadata.labels.module == "synthetic"`),
		jq.Match(`.metadata.labels["unrelated.opendatahub.io/live"] == "preserved"`),
		jq.Match(`.metadata.annotations.note == "authored"`),
		jq.Match(`.metadata.annotations["unrelated.opendatahub.io/live"] == "preserved"`),
		jq.Match(`.metadata | has("finalizers") | not`),
		jq.Match(`.status.phase == "Ready"`),
		jq.Match(`.spec.manifests.helm.values.replicas == 3`),
		jq.Match(`.spec.manifests.helm.values.added.list == [1, null, "three"]`),
		jq.Match(`.spec.manifests.helm.values.nested.items == ["one", null, 3]`),
		jq.Match(`.spec.manifests.helm.values.unset == null`),
		jq.Match(`.spec.controller.deployment.container.env[2].name == "EXTRA"`),
		jq.Match(`.spec.controller.deployment.container.env[2].value == "value"`),
	))

	for _, annotation := range []string{"binding", "bound", "locked"} {
		g.Expect(bodies[0]).NotTo(ContainSubstring(annotation))
	}
}

// TestEnsureRejectsUnusableLiveDefinitions proves definitions this run
// must not adopt or overwrite — missing UID, being deleted, or
// carrying foreign owner references — fail as rejected publications
// that retain the confirmed owner pointer and never mutate the live
// object.
func TestEnsureRejectsUnusableLiveDefinitions(t *testing.T) {
	t.Parallel()

	cases := map[string]func(live *v1alpha1.PlatformModuleDefinition){
		"missing uid": func(live *v1alpha1.PlatformModuleDefinition) {
			live.UID = ""
		},
		"being deleted": func(live *v1alpha1.PlatformModuleDefinition) {
			now := metav1.Now()
			live.DeletionTimestamp = &now
		},
		"foreign owner references": func(live *v1alpha1.PlatformModuleDefinition) {
			live.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Pod", Name: "foreign", UID: "foreign-uid",
			}}
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)
			server := newFakeOwnerServer(t)

			live := newLiveDefinition()
			mutate(live)
			seeded := marshalDefinition(t, g, live)
			server.seedDefinition(t, seeded)

			definition := newAuthoredDefinition()
			definition.Spec.RelatedImages = []string{"CHANGED"}

			owner, err := ensured(t.Context(), t, g, server, definition)

			g.Expect(owner).NotTo(BeNil())
			g.Expect(owner.UID).To(Equal(live.UID))
			g.Expect(owner.TypeMeta).To(Equal(metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind}))

			typed := ownerError(g, err)
			g.Expect(typed.Outcome).To(Equal(OutcomeRejected))
			g.Expect(typed.Owner).NotTo(BeNil())
			g.Expect(typed.Owner.UID).To(Equal(live.UID))

			g.Expect(server.definitionWriteCount()).To(Equal(0))
			g.Expect(server.storedDefinition(t, "synthetic")).To(SatisfyAll(
				jq.Match(`.spec.relatedImages == ["RELATED_IMAGE_OPERAND"]`),
				jq.Match(`.metadata.resourceVersion == "1"`),
			))
		})
	}
}

// TestEnsureLostCreateResponseIsUnknown proves an accepted create
// whose response never arrives is an unknown publication with no
// confirmed identity — the write may have landed, no UID is invented,
// and the ambiguous create is never retried.
func TestEnsureLostCreateResponseIsUnknown(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.loseCreate = true

	owner, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())

	g.Expect(owner).To(BeNil())

	typed := ownerError(g, err)
	g.Expect(typed.Outcome).To(Equal(OutcomeUnknown))
	g.Expect(typed.Owner).To(BeNil())
	g.Expect(typed.Error()).To(ContainSubstring("not reported"))

	// The server stored the write; only the response was lost.
	g.Expect(server.definitionWriteCount()).To(Equal(1))
	stored := server.storedDefinition(t, "synthetic")
	g.Expect(stored).To(SatisfyAll(
		jq.Match(`.metadata.uid == "definition-uid"`),
		jq.Match(`.spec.manifests.helm.values.replicas == 2`),
	))
}

// TestEnsureLostUpdateResponseIsUnknown proves an accepted update
// whose response never arrives leaves the desired spec unknown while
// the prior read still confirms the owner identity.
func TestEnsureLostUpdateResponseIsUnknown(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.seedDefinition(t, marshalDefinition(t, g, newLiveDefinition()))
	server.loseUpdate = true

	changed := newAuthoredDefinition()
	changed.Spec.Manifests.Helm.Values["replicas"] = apiextensionsv1.JSON{Raw: []byte(`3`)}

	owner, err := ensured(t.Context(), t, g, server, changed)

	g.Expect(owner).NotTo(BeNil())
	g.Expect(owner.UID).To(Equal(types.UID(definitionUID)))

	typed := ownerError(g, err)
	g.Expect(typed.Outcome).To(Equal(OutcomeUnknown))
	g.Expect(typed.Owner).NotTo(BeNil())
	g.Expect(typed.Owner.UID).To(Equal(types.UID(definitionUID)))

	// The server stored the new spec; only the response was lost.
	g.Expect(server.definitionWriteCount()).To(Equal(1))
	g.Expect(server.storedDefinition(t, "synthetic")).To(
		jq.Match(`.spec.manifests.helm.values.replicas == 3`),
	)
}

// TestEnsureRetriesUpdateConflicts proves a competing writer's
// conflicts are retried and the publication still confirms the
// desired spec.
func TestEnsureRetriesUpdateConflicts(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.seedDefinition(t, marshalDefinition(t, g, newLiveDefinition()))
	server.conflicts = 2

	changed := newAuthoredDefinition()
	changed.Spec.Manifests.Helm.Values["replicas"] = apiextensionsv1.JSON{Raw: []byte(`3`)}

	owner, err := ensured(t.Context(), t, g, server, changed)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(owner.UID).To(Equal(types.UID(definitionUID)))
	g.Expect(owner.ResourceVersion).To(Equal("2"))
	g.Expect(server.definitionUpdateAttemptCount("synthetic")).To(Equal(3))
	g.Expect(server.definitionUpdateBodies("synthetic")).To(HaveLen(1))
}

// TestEnsureRetriesRacedDeletion proves a definition that exists at
// creation time but vanishes before the read is created afresh rather
// than failing the publication.
func TestEnsureRetriesRacedDeletion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.seedDefinition(t, marshalDefinition(t, g, newLiveDefinition()))
	server.vanishOnGet = true

	owner, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(owner.UID).To(Equal(types.UID(definitionUID)))
	g.Expect(owner.ResourceVersion).To(Equal("1"))
	g.Expect(server.definitionCreateBodies("synthetic")).To(HaveLen(1))
}

// TestEnsureCancelledInvocationIsNotAttempted proves a cancelled
// invocation fails before any write with the cancellation identity
// preserved through the typed error's cause chain.
func TestEnsureCancelledInvocationIsNotAttempted(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)

	connection := connected(context.Background(), t, g, server)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	owner, err := Ensure(ctx, connection, newAuthoredDefinition())

	g.Expect(owner).To(BeNil())

	typed := ownerError(g, err)
	g.Expect(typed.Outcome).To(Equal(OutcomeNotAttempted))
	g.Expect(typed.Unwrap()).To(MatchError(context.Canceled))
	g.Expect(server.requestCount()).To(Equal(0))
}

// TestEnsureDefinitiveCreateFailureIsRejected proves a create the
// server definitively refuses — a forbidden request — is a rejected
// publication, not an unknown one.
func TestEnsureDefinitiveCreateFailureIsRejected(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.forbidDefinitionCreate = true

	owner, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())

	g.Expect(owner).To(BeNil())

	typed := ownerError(g, err)
	g.Expect(typed.Outcome).To(Equal(OutcomeRejected))
	g.Expect(typed.Owner).To(BeNil())
	g.Expect(apierrors.IsForbidden(typed.Unwrap())).To(BeTrue())
	g.Expect(server.definitionWriteCount()).To(Equal(0))
}
