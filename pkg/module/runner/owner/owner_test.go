package owner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
)

// requestDecoder decodes protobuf request bodies — should a client
// send them on the wire — through the real apimachinery serializer,
// so the fake can store them as JSON.
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
// storage with UID/resourceVersion assignment, apply-conflict
// injection, raced-deletion simulation, and lost apply responses
// that abort the connection after the write is stored. It records
// every submitted definition apply body, query, and content type.
type fakeOwnerServer struct {
	*httptest.Server

	loseApply   bool
	vanishOnGet bool

	forbidDefinitionApply bool
	applyConflicts        int

	// raceCRDCreation simulates another writer creating the CRD
	// between the missing read and our creation attempt: reads report
	// it missing until the raced creation is refused.
	raceCRDCreation string

	mu                sync.Mutex
	crds              map[string]string
	crdWrites         int
	definitions       map[string]string
	definitionWrites  map[string]int
	applyAttempts     map[string]int
	applyBodies       map[string][]string
	applyQueries      []string
	applyContentTypes []string
	requests          int
	discoveryRequests int
}

// newFakeOwnerServer starts the double; the server closes with the
// test.
func newFakeOwnerServer(t *testing.T) *fakeOwnerServer {
	t.Helper()

	fake := &fakeOwnerServer{
		crds:             map[string]string{},
		definitions:      map[string]string{},
		definitionWrites: map[string]int{},
		applyAttempts:    map[string]int{},
		applyBodies:      map[string][]string{},
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
func (s *fakeOwnerServer) storedDefinition(t *testing.T) map[string]any {
	t.Helper()

	s.mu.Lock()
	body := s.definitions[syntheticName]
	s.mu.Unlock()

	var object map[string]any
	if err := json.Unmarshal([]byte(body), &object); err != nil {
		t.Fatal(err)
	}

	return object
}

// definitionWriteCount returns how many applies the server accepted
// across all definitions.
func (s *fakeOwnerServer) definitionWriteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	writes := 0
	for _, count := range s.definitionWrites {
		writes += count
	}

	return writes
}

// definitionApplyBodies returns the submitted apply bodies for the
// synthetic definition.
func (s *fakeOwnerServer) definitionApplyBodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.applyBodies[syntheticName]...)
}

// definitionApplyAttemptCount returns how many apply requests —
// accepted or conflict-refused — the synthetic definition received.
func (s *fakeOwnerServer) definitionApplyAttemptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.applyAttempts[syntheticName]
}

// definitionApplyQueries returns the query strings of the submitted
// definition applies.
func (s *fakeOwnerServer) definitionApplyQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.applyQueries...)
}

// definitionApplyContentTypes returns the content types of the
// submitted definition applies.
func (s *fakeOwnerServer) definitionApplyContentTypes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.applyContentTypes...)
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

// requestCount returns how many requests the server received beyond
// the client's internal discovery reads: the controller-runtime
// client's lazy mapper fetches /api, /apis, and per-group resource
// documents on first use, which are not observable owner behavior.
func (s *fakeOwnerServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.requests - s.discoveryRequests
}

// countDiscovery records a client-internal discovery read, which
// requestCount excludes.
func (s *fakeOwnerServer) countDiscovery() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.discoveryRequests++
}

// resetCounters drops the recorded apply bodies and counters, keeping
// the stored objects.
func (s *fakeOwnerServer) resetCounters() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.applyBodies = map[string][]string{}
	s.applyQueries = nil
	s.applyContentTypes = nil
	s.definitionWrites = map[string]int{}
	s.applyAttempts = map[string]int{}
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
		s.countDiscovery()
		writeJSON(http.StatusOK, ownerAPIVersionsJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1":
		s.countDiscovery()
		writeJSON(http.StatusOK, ownerCoreV1ResourcesJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/apis":
		s.countDiscovery()
		writeJSON(http.StatusOK, ownerGroupsJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/apis/apiextensions.k8s.io/v1":
		s.countDiscovery()
		writeJSON(http.StatusOK, ownerAPIExtensionsResourcesJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/apis/config.opendatahub.io/v1alpha1":
		s.countDiscovery()
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

	case r.Method == http.MethodPatch &&
		strings.HasPrefix(r.URL.Path, "/apis/config.opendatahub.io/v1alpha1/platformmoduledefinitions/"):
		name := strings.TrimPrefix(r.URL.Path, "/apis/config.opendatahub.io/v1alpha1/platformmoduledefinitions/")
		body, object := readObject()
		status := http.StatusOK

		if s.forbidDefinitionApply {
			writeStatus(http.StatusForbidden, "Forbidden")

			return
		}

		s.mu.Lock()
		stored := s.definitions[name]
		s.applyAttempts[name]++
		s.mu.Unlock()

		if s.applyConflicts > 0 {
			s.mu.Lock()
			s.applyConflicts--
			s.mu.Unlock()

			writeStatus(http.StatusConflict, "Conflict")

			return
		}

		var storedObject map[string]any
		if stored != "" {
			if err := json.Unmarshal([]byte(stored), &storedObject); err != nil {
				panic(http.ErrAbortHandler)
			}
		}

		// Server-side apply merge: the applied metadata and spec
		// replace the stored ones — labels and annotations are whole
		// maps — while the server keeps the identity, the status, and
		// fields other field managers own.
		mergedMetadata := map[string]any{}
		if storedObject != nil {
			for key, value := range metadata(storedObject) {
				mergedMetadata[key] = value
			}
		}
		for key, value := range metadata(object) {
			mergedMetadata[key] = value
		}

		uid := definitionUID
		version := 1
		if storedObject != nil {
			uid = metadata(storedObject)["uid"].(string)
			if current, ok := mergedMetadata["resourceVersion"].(string); ok {
				if parsed, err := strconv.Atoi(current); err == nil {
					version = parsed + 1
				}
			}
		} else {
			status = http.StatusCreated
		}
		mergedMetadata["uid"] = uid
		mergedMetadata["resourceVersion"] = fmt.Sprintf("%d", version)

		merged := object
		merged["metadata"] = mergedMetadata
		if storedObject != nil {
			if liveStatus, ok := storedObject["status"]; ok {
				merged["status"] = liveStatus
			}
		}

		encoded, err := json.Marshal(merged)
		if err != nil {
			panic(http.ErrAbortHandler)
		}

		s.mu.Lock()
		s.definitions[name] = string(encoded)
		s.applyBodies[name] = append(s.applyBodies[name], body)
		s.applyQueries = append(s.applyQueries, r.URL.RawQuery)
		s.applyContentTypes = append(s.applyContentTypes, r.Header.Get("Content-Type"))
		s.definitionWrites[name]++
		lose := s.loseApply
		s.mu.Unlock()

		if lose {
			// The write is stored but the response never arrives.
			panic(http.ErrAbortHandler)
		}

		writeJSON(status, string(encoded))

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

	default:
		writeStatus(http.StatusNotFound, "NotFound")
	}
}

// definitionUID is the server-assigned identity of the synthetic
// definition; syntheticName is its metadata.name. The double stores
// only this definition, so its readers address it directly.
const (
	definitionUID = "definition-uid"
	syntheticName = "synthetic"
)

// errUnreportedWrite is the sentinel the injected-error publication
// test fails the apply with: a non-status failure the generic
// write-outcome policy classifies as an unreported request.
var errUnreportedWrite = errors.New("connection reset by peer")

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
// run applies the definition behind a real server identity: the
// returned owner carries the assigned UID and runtime GVK, and the
// submitted apply body publishes exactly the authored name, labels,
// annotations, and normalized unexpanded spec — never forged
// authority fields, and no status — behind the run's field owner.
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

	g.Expect(server.definitionApplyContentTypes()).To(Equal([]string{"application/apply-patch+yaml"}))

	queries := server.definitionApplyQueries()
	g.Expect(queries).To(HaveLen(1))
	parsed, err := url.ParseQuery(queries[0])
	g.Expect(err).To(Succeed())
	g.Expect(parsed.Get("fieldManager")).To(Equal(fieldOwner))
	g.Expect(parsed.Get("force")).To(Equal("true"))

	bodies := server.definitionApplyBodies()
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
		jq.Match(`.metadata | has("creationTimestamp") | not`),
		jq.Match(`. | has("status") | not`),
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

// TestEnsureRepeatPublicationAppliesAgain proves a re-run of an
// unchanged definition reuses the published identity through the
// same single idempotent apply: one write, one field manager, no
// read-only shortcut that could race a competing writer.
func TestEnsureRepeatPublicationAppliesAgain(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)

	first, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())
	g.Expect(err).NotTo(HaveOccurred())

	server.resetCounters()

	second, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(second.UID).To(Equal(first.UID))
	g.Expect(second.TypeMeta).To(Equal(metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind}))
	g.Expect(server.definitionWriteCount()).To(Equal(1))
	g.Expect(server.definitionApplyAttemptCount()).To(Equal(1))
	g.Expect(server.definitionApplyBodies()).To(HaveLen(1))
}

// TestEnsureAppliesChangedSpecPreservingIdentity proves a changed
// authored spec publishes through the same single apply: the live
// identity and controller-written status survive the server-side
// merge, the previously unbound definition is reused without any
// binding annotation, arbitrary Helm values and null env entries
// survive, and authored labels and annotations are the whole desired
// state — a live key the re-run no longer authors is replaced.
func TestEnsureAppliesChangedSpecPreservingIdentity(t *testing.T) {
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

	bodies := server.definitionApplyBodies()
	g.Expect(bodies).To(HaveLen(1))
	g.Expect(bodies[0]).To(SatisfyAll(
		jq.Match(`.metadata.name == "synthetic"`),
		jq.Match(`.metadata.labels.module == "synthetic"`),
		jq.Match(`.metadata.annotations.note == "authored"`),
		jq.Match(`.metadata | has("uid") | not`),
		jq.Match(`.metadata | has("resourceVersion") | not`),
		jq.Match(`.metadata | has("finalizers") | not`),
		jq.Match(`. | has("status") | not`),
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

	// The stored object keeps the server identity and status, while
	// the authored labels and annotations replace the live maps.
	stored := server.storedDefinition(t)
	g.Expect(stored).To(SatisfyAll(
		jq.Match(`.metadata.uid == "definition-uid"`),
		jq.Match(`.metadata.resourceVersion == "2"`),
		jq.Match(`.metadata.labels.module == "synthetic"`),
		jq.Match(`.metadata.labels | has("unrelated.opendatahub.io/live") | not`),
		jq.Match(`.metadata.annotations.note == "authored"`),
		jq.Match(`.metadata.annotations | has("unrelated.opendatahub.io/live") | not`),
		jq.Match(`.status.phase == "Ready"`),
		jq.Match(`.spec.manifests.helm.values.replicas == 3`),
	))
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
			g.Expect(server.storedDefinition(t)).To(SatisfyAll(
				jq.Match(`.spec.relatedImages == ["RELATED_IMAGE_OPERAND"]`),
				jq.Match(`.metadata.resourceVersion == "1"`),
			))
		})
	}
}

// TestEnsureLostApplyResponseIsUnknown proves an accepted apply over
// an absent owner whose response never arrives is an unknown
// publication with no confirmed identity — the write may have landed,
// no UID is invented, and the ambiguous apply is never retried.
func TestEnsureLostApplyResponseIsUnknown(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.loseApply = true

	owner, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())

	g.Expect(owner).To(BeNil())

	typed := ownerError(g, err)
	g.Expect(typed.Outcome).To(Equal(OutcomeUnknown))
	g.Expect(typed.Owner).To(BeNil())
	g.Expect(typed.Error()).To(ContainSubstring("not reported"))

	// The server stored the write; only the response was lost.
	g.Expect(server.definitionWriteCount()).To(Equal(1))
	stored := server.storedDefinition(t)
	g.Expect(stored).To(SatisfyAll(
		jq.Match(`.metadata.uid == "definition-uid"`),
		jq.Match(`.spec.manifests.helm.values.replicas == 2`),
	))
}

// TestEnsureLostApplyResponseRetainsIdentity proves an accepted apply
// over a live owner whose response never arrives leaves the desired
// spec unknown while the prior read still confirms the owner
// identity.
func TestEnsureLostApplyResponseRetainsIdentity(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.seedDefinition(t, marshalDefinition(t, g, newLiveDefinition()))
	server.loseApply = true

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
	g.Expect(server.storedDefinition(t)).To(
		jq.Match(`.spec.manifests.helm.values.replicas == 3`),
	)
}

// TestPublishUnreportedWriteIsUnknown proves a definition apply whose
// failure the generic write-outcome policy classifies as unreported
// propagates as an unknown publication with no confirmed identity and
// the sentinel cause reachable through errors.Is. The classification
// table itself lives in the cluster package; this test pins only the
// owner mapping, with the error injected straight into the apply.
func TestPublishUnreportedWriteIsUnknown(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	scheme := runtime.NewScheme()
	g.Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())

	// The pre-read answers NotFound — the absent-owner case — so the
	// apply is the only write the sentinel fails.
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Apply: func(
				context.Context,
				crclient.WithWatch,
				runtime.ApplyConfiguration,
				...crclient.ApplyOption,
			) error {
				return errUnreportedWrite
			},
		}).
		Build()

	owner, err := publish(t.Context(), cluster.NewWithClient(client), newAuthoredDefinition())

	g.Expect(owner).To(BeNil())

	typed := ownerError(g, err)
	g.Expect(typed.Outcome).To(Equal(OutcomeUnknown))
	g.Expect(typed.Owner).To(BeNil())
	g.Expect(typed.Error()).To(ContainSubstring("not reported"))
	g.Expect(errors.Is(err, errUnreportedWrite)).To(BeTrue())
}

// TestEnsureApplyConflictIsRejected proves a competing field manager's
// apply conflict is a definitive rejection — never retried, never
// silently unknown — with the conflict identity preserved and the
// stored state untouched.
func TestEnsureApplyConflictIsRejected(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.seedDefinition(t, marshalDefinition(t, g, newLiveDefinition()))
	server.applyConflicts = 1

	changed := newAuthoredDefinition()
	changed.Spec.Manifests.Helm.Values["replicas"] = apiextensionsv1.JSON{Raw: []byte(`3`)}

	owner, err := ensured(t.Context(), t, g, server, changed)

	g.Expect(owner).NotTo(BeNil())
	g.Expect(owner.UID).To(Equal(types.UID(definitionUID)))

	typed := ownerError(g, err)
	g.Expect(typed.Outcome).To(Equal(OutcomeRejected))
	g.Expect(typed.Owner).NotTo(BeNil())
	g.Expect(typed.Owner.UID).To(Equal(types.UID(definitionUID)))
	g.Expect(apierrors.IsConflict(typed.Unwrap())).To(BeTrue())

	// The apply was attempted exactly once and stored nothing.
	g.Expect(server.definitionApplyAttemptCount()).To(Equal(1))
	g.Expect(server.definitionWriteCount()).To(Equal(0))
	g.Expect(server.storedDefinition(t)).To(SatisfyAll(
		jq.Match(`.metadata.resourceVersion == "1"`),
		jq.Match(`.spec.manifests.helm.values.replicas == 2`),
	))
}

// TestEnsureAppliesAfterRacedDeletion proves a definition that exists
// at read time but vanishes before the apply is created afresh by the
// same apply rather than failing the publication.
func TestEnsureAppliesAfterRacedDeletion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.seedDefinition(t, marshalDefinition(t, g, newLiveDefinition()))
	server.vanishOnGet = true

	owner, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(owner.UID).To(Equal(types.UID(definitionUID)))
	g.Expect(owner.ResourceVersion).To(Equal("1"))
	g.Expect(server.definitionApplyBodies()).To(HaveLen(1))
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

// TestEnsureDefinitiveApplyFailureIsRejected proves an apply the
// server definitively refuses — a forbidden request — is a rejected
// publication, not an unknown one.
func TestEnsureDefinitiveApplyFailureIsRejected(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)
	server.forbidDefinitionApply = true

	owner, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())

	g.Expect(owner).To(BeNil())

	typed := ownerError(g, err)
	g.Expect(typed.Outcome).To(Equal(OutcomeRejected))
	g.Expect(typed.Owner).To(BeNil())
	g.Expect(apierrors.IsForbidden(typed.Unwrap())).To(BeTrue())
	g.Expect(server.definitionWriteCount()).To(Equal(0))
}
