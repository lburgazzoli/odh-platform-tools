//go:build integration

// The offline run chain harness.
//
// Real boundaries: the strict loader over real files, Viper image
// resolution over a real dotenv file and the process environment,
// both shared manifest engines with the library apply-order
// post-renderer and both struct injectors, the real runner-internal
// owner preparation, the real framework deploy action with its
// default enriched sort, and the real public Deployment status viewer
// behind the requested wait. The composed chain is the production
// runner.New wiring with exactly one seam substituted: the cluster
// connection is a controller-runtime fake client wrapped in the
// connection's public NewWithClient test constructor, reached
// through the real connect signature.
//
// Simulated boundaries — and only these: API identity assignment
// (deterministic server UIDs stamped into applies and creates so
// re-applies and read-backs keep one identity, exactly as a server
// assigns), CRD establishment status on creation (so the runner's
// real CRD wait observes a served definition API), and scripted
// Deployment status responses for the requested wait (live rollout
// states a real cluster would report). Everything else — SSA request
// shape, field ownership, merge, ordering, and the write sequence —
// is the production code under test.
//
// Provenance checklist, reconciled against the recorded operator and
// framework symbols:
//
//   - Env injection ports the operator's injectModuleEnv
//     (internal/controller/modules/modules_controller_actions_inject_env.go)
//     reduced to a single module with a typed-target boundary: only
//     the exact Deployment group/version/kind matching the configured
//     controller name (module-name fallback) is converted to an
//     appsv1.Deployment and mutated, so a malformed typed field on
//     that target fails the render and unknown fields may be dropped,
//     while unrelated resources are never converted. An empty
//     configured Deployment name follows deploymentNameFor's
//     release/module-name fallback. Both are exercised below through
//     the real chain.
//   - Image source precedence (environment over file, empty
//     environment masks the file, undeclared ambient variables never
//     injected) ports the operator's related-images contract; the
//     images service unit tests hold the Viper codec semantics.
//   - The pre-migration ConfigMap winner: separate Helm templates
//     with same-named ConfigMaps in distinct namespaces keep the
//     engine apply-order (namespace-sorted) ordering the old
//     framework produced, asserted here at the deploy boundary, not
//     just at the renderer.
//   - Gate-labeled ConfigMaps stay ordinary rendered resources: no
//     gates subsystem exists (upgrade gates were removed from scope
//     upstream of this plan), and the operator's UpgradeGateLabel
//     (pkg/controller/gates/gates.go, commit 5a1d17f57b32) is
//     referenced only to pin the label spelling.
//   - Framework deploy parity — force-SSA adoption, field ownership,
//     the default SortByApplyOrder with the platform ConfigMap
//     participating, continue-on-error first report, and the
//     core/v1 Namespace and CRD ownership exclusions — is the
//     framework action's own behavior (odh-platform-utilities
//     pkg/deploy), driven here unmodified; its unit suite and the
//     Kind suite cover the server-side semantics the fake client
//     cannot prove.
//   - Approved standalone differences: single-module injection
//     instead of the operator's aggregated ModuleEnvInjection, no
//     XKS cert-manager bootstrap keys, no gates, no automatic lint,
//     and no upgrade-gate extraction. These are scoping decisions of
//     this tool, recorded in the plan, not claims of full-platform
//     equivalence.
//   - Upstream skip/error/merge behavior accounted for: the fake
//     client's SSA implements field-manager merge but not admission,
//     quota, or final API validation — the Kind suite (011/012)
//     owns those; a bundled custom resource whose CRD was just
//     created may fail on first run and succeed on re-run, exactly
//     as the operator behaves, so no bundled custom resource is
//     asserted here.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/deploy"
)

// runCRDEstablishedStatusJSON is the status a served CRD carries:
// established with its names accepted.
const runCRDEstablishedStatusJSON = `{"conditions":[{"type":"Established","status":"True"},{"type":"NamesAccepted","status":"True"}]}
`

// runFrame is one scripted GET reaction for a waited Deployment: an
// error, or a live object map returned as the observation. The last
// frame of a sequence repeats.
type runFrame struct {
	err  error
	live map[string]any
}

// runHarness is the complete offline run chain over one fake
// cluster: the standard getter resolves the namespace from a real
// kubeconfig, every mutating client call is recorded in order, GET
// identities are recorded for workload-observation assertions, and
// scripted frames replace live lookups of objects that exist — so
// only the applied workload is ever scripted.
type runHarness struct {
	connection *cluster.Connection
	getter     genericclioptions.RESTClientGetter

	writes []string
	gets   []string

	// frames maps one identity to its scripted observation sequence.
	frames map[string][]runFrame

	// failApply maps one identity to the error its apply returns.
	failApply map[string]error

	// crdNeverEstablished suppresses the establishment status the
	// simulated server stamps onto an applied CRD, so the owner's
	// real CRD wait polls until the invocation expires.
	crdNeverEstablished bool

	// quiet suppresses write recording while the simulated server
	// performs its own internal identity writes.
	quiet bool

	// connected reports whether the chain reached the connect seam.
	connected bool
}

// newRunHarness builds the harness and its kubeconfig-backed getter.
func newRunHarness(t *testing.T) *runHarness {
	t.Helper()

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(runKubeconfigYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	getter := genericclioptions.NewConfigFlags(true)
	getter.KubeConfig = &kubeconfig

	harness := &runHarness{
		getter:    getter,
		frames:    map[string][]runFrame{},
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

	objectIdentity := func(obj crclient.Object) string {
		gvk, err := apiutil.GVKForObject(obj, scheme)
		if err != nil {
			t.Fatal(err)
		}

		return runIdentity(gvk, obj.GetNamespace(), obj.GetName())
	}

	applyIdentity := func(obj runtime.ApplyConfiguration) (string, unstructured.Unstructured) {
		encoded, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}

		var probe unstructured.Unstructured
		if err := json.Unmarshal(encoded, &probe); err != nil {
			t.Fatal(err)
		}

		return runIdentity(probe.GroupVersionKind(), probe.GetNamespace(), probe.GetName()), probe
	}

	// stampUID simulates the server assigning one deterministic identity
	// to a newly created object: after the fake merge stores the apply
	// result, the UID is stamped into both the apply response — which
	// the caller decodes, exactly as a server echoes its assigned
	// identity — and the stored copy, so re-applies and read-backs keep
	// one identity. SSA requests stay UID-free, as real ones must.
	stampUID := func(
		c crclient.WithWatch,
		obj runtime.ApplyConfiguration,
		id string,
		merged unstructured.Unstructured,
	) {
		if merged.GetUID() != "" {
			return
		}

		object := merged.Object
		if object == nil {
			object = map[string]any{}
		}

		metadata, _ := object["metadata"].(map[string]any)
		if metadata == nil {
			metadata = map[string]any{}
			object["metadata"] = metadata
		}
		metadata["uid"] = runServerUID(id)

		encoded, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}

		// The apply response carries the assigned identity.
		if err := json.Unmarshal(encoded, obj); err != nil {
			t.Fatal(err)
		}

		// So does the stored copy, through unrecorded server-side
		// writes — the identity assignment a real server performs
		// inside the apply itself. Kinds the fake client treats with
		// a status subresource, like the definition CRD, need the
		// status written through the status subresource: a plain
		// update would drop it. The status write runs first and
		// refreshes the resource version, so the identity update
		// stays conflict-free.
		stored := &unstructured.Unstructured{}
		if err := json.Unmarshal(encoded, stored); err != nil {
			t.Fatal(err)
		}

		harness.quiet = true
		defer func() { harness.quiet = false }()

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
			id, probe := applyIdentity(obj)
			harness.writes = append(harness.writes, "apply "+id)

			for key, injected := range harness.failApply {
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
			// the harness suppresses it to hold the owner's real
			// wait.
			if merged.GetKind() == "CustomResourceDefinition" && !harness.crdNeverEstablished {
				var crdStatus map[string]any
				if err := json.Unmarshal([]byte(runCRDEstablishedStatusJSON), &crdStatus); err != nil {
					t.Fatal(err)
				}

				merged.Object["status"] = crdStatus
			}

			stampUID(c, obj, id, merged)

			return nil
		},
		Create: func(
			ctx context.Context,
			c crclient.WithWatch,
			obj crclient.Object,
			opts ...crclient.CreateOption,
		) error {
			harness.writes = append(harness.writes, "create "+objectIdentity(obj))

			if obj.GetUID() == "" {
				obj.SetUID(types.UID(runServerUID(objectIdentity(obj))))
			}

			return c.Create(ctx, obj, opts...)
		},
		Update: func(
			ctx context.Context,
			c crclient.WithWatch,
			obj crclient.Object,
			opts ...crclient.UpdateOption,
		) error {
			if !harness.quiet {
				harness.writes = append(harness.writes, "update "+objectIdentity(obj))
			}

			return c.Update(ctx, obj, opts...)
		},
		Patch: func(
			ctx context.Context,
			c crclient.WithWatch,
			obj crclient.Object,
			patch crclient.Patch,
			opts ...crclient.PatchOption,
		) error {
			harness.writes = append(harness.writes, "patch "+objectIdentity(obj))

			return c.Patch(ctx, obj, patch, opts...)
		},
		Get: func(
			ctx context.Context,
			c crclient.WithWatch,
			key crclient.ObjectKey,
			obj crclient.Object,
			opts ...crclient.GetOption,
		) error {
			gvk, err := apiutil.GVKForObject(obj, scheme)
			if err != nil {
				t.Fatal(err)
			}

			id := runIdentity(gvk, key.Namespace, key.Name)
			harness.gets = append(harness.gets, id)

			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}

			// Scripted observations replace only lookups of objects
			// that exist — the applied workload — so the deploy
			// action's pre-apply lookups of absent objects keep the
			// fake client's real NotFound behavior.
			frames := harness.frames[id]
			if len(frames) == 0 {
				return nil
			}

			frame := frames[0]
			if len(frames) > 1 {
				harness.frames[id] = frames[1:]
			}

			if frame.err != nil {
				return frame.err
			}

			if live, ok := obj.(*unstructured.Unstructured); ok {
				live.Object = frame.live
			}

			return nil
		},
	}

	harness.connection = cluster.NewWithClient(
		fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(funcs).
			Build(),
	)

	return harness
}

// connect is the single substituted service boundary: the real
// connect signature returning the simulated connection.
func (h *runHarness) connect(_ context.Context, _ genericclioptions.RESTClientGetter) (*cluster.Connection, error) {
	h.connected = true

	return h.connection, nil
}

// run composes the production runner wiring — the real loader, both
// real renderers, and the real framework deploy action, with the
// runner's own owner preparation — over the harness connection, and
// runs it once.
func (h *runHarness) run(ctx context.Context, options runner.Options) (runner.Result, error) {
	composed := runner.New(
		options,
		h.getter,
		loader.LoadFromPath,
		renderer.Render,
		h.connect,
		deploy.Apply,
	)

	return composed.Run(ctx)
}

// runOptions returns valid invocation settings for one module root.
func runOptions(root string) runner.Options {
	return runner.Options{
		DefinitionFile:  filepath.Join(root, "definition.yaml"),
		PlatformVersion: "2.19.0",
		Timeout:         30 * time.Second,
	}
}

// runImagesFile writes the dotenv related-images file into one
// module root and returns its path.
func runImagesFile(t *testing.T, root string, content string) string {
	t.Helper()

	path := filepath.Join(root, "images.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// runIdentity renders one recorded operation identity.
func runIdentity(gvk schema.GroupVersionKind, namespace string, name string) string {
	if namespace == "" {
		return gvk.String() + " " + name
	}

	return gvk.String() + " " + namespace + "/" + name
}

// runServerUID renders the deterministic server identity of one
// object identity.
func runServerUID(id string) string {
	return "server-uid-" + strings.ReplaceAll(
		strings.ReplaceAll(id, "/", "-"),
		" ", "-",
	)
}

// writeCount returns how many recorded writes carry the given
// substring, and the index of the first one.
func (h *runHarness) writeIndex(substr string) (int, int) {
	count, first := 0, -1
	for i, write := range h.writes {
		if strings.Contains(write, substr) {
			count++
			if first < 0 {
				first = i
			}
		}
	}

	return count, first
}

// getCount returns how many recorded GETs targeted the identity
// carrying the given substring.
func (h *runHarness) getCount(substr string) int {
	count := 0
	for _, get := range h.gets {
		if strings.Contains(get, substr) {
			count++
		}
	}

	return count
}

// stored reads one live object back through the connection client.
func (h *runHarness) stored(
	t *testing.T,
	gvk schema.GroupVersionKind,
	namespace string,
	name string,
) unstructured.Unstructured {
	t.Helper()

	probe := &unstructured.Unstructured{}
	probe.SetGroupVersionKind(gvk)

	err := h.connection.Client().Get(t.Context(), crclient.ObjectKey{Namespace: namespace, Name: name}, probe)
	if err != nil {
		t.Fatal(err)
	}

	return *probe
}

// runDeploymentStatus builds one scripted live controller Deployment
// observation with a stable server identity: the default is rolled
// out, and mutations adjust it.
func runDeploymentStatus(mutate func(*appsv1.Deployment)) map[string]any {
	oneReplica := int32(1)

	deployment := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "run-namespace", Name: "synthetic-controller", UID: "deployment-uid"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &oneReplica,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "synthetic"}},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           1,
			UpdatedReplicas:    1,
			AvailableReplicas:  1,
		},
	}

	if mutate != nil {
		mutate(deployment)
	}

	live, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deployment)
	if err != nil {
		panic(fmt.Sprintf("building scripted deployment status: %v", err))
	}

	return live
}

// runGroupVersionKinds are the payload identities the assertions
// read back.
var (
	runDeploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	runConfigMapGVK  = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}
	runNamespaceGVK  = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}
)
