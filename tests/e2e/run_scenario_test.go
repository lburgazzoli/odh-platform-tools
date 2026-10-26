//go:build e2e

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

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	runcli "github.com/lburgazzoli/odh-platform-tools/cmd/module/run"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// The identities every scenario tracks and cleans up. The shared
// definition CRD the run bootstraps is deliberately absent: it is
// shared infrastructure — foreign on an external cluster — and is
// never deleted by scenario code.
var (
	definitionGVK = toolApi.GroupVersion.WithKind("PlatformModuleDefinition")
	namespaceGVK  = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}
	crdGVK        = apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")
	configMapGVK  = corev1.SchemeGroupVersion.WithKind("ConfigMap")
)

// cliBinary is the real CLI the suite executes, built once by
// TestMain.
var cliBinary string

// trackedObject is one object the scenario owns — created by the run
// or by the scenario itself — with the server identity that makes its
// cleanup exact: only this UID is ever deleted.
type trackedObject struct {
	gvk       schema.GroupVersionKind
	namespace string
	name      string
	uid       types.UID
}

// scenario is one live run's unique identity: a fresh suffix under
// which every cluster-scoped resource — the definition, the payload
// CRD and its group, and the payload-supplied Namespace — is named,
// a private module tree, and the tracked objects its cleanup owns.
// The authored values (message, replicas) are the live tuning the
// spec-update scenario changes between runs; runNS is the namespace
// the CLI renders into.
type scenario struct {
	suffix   string
	root     string
	name     string
	group    string
	targetNS string
	runNS    string
	auxNS    string
	message  string
	replicas string

	tracked []trackedObject
}

// newScenario returns a scenario with a fresh unique identity and its
// cleanup registered: tracked objects are deleted by exact UID, in
// reverse tracking order, and nothing else is ever touched.
func newScenario(t *testing.T) *scenario {
	t.Helper()

	suffix := uniqueSuffix()

	s := &scenario{
		suffix:   suffix,
		root:     t.TempDir(),
		name:     "synthetic-" + suffix,
		group:    "e2e" + suffix + ".platform.opendatahub.io",
		targetNS: "run-" + suffix,
		runNS:    "run-" + suffix,
		auxNS:    "aux-" + suffix,
		message:  "authored-value",
		replicas: "2",
	}

	t.Cleanup(func() {
		client := engine(t).Client()

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		// Reverse tracking order: namespaces first remove the
		// contained objects, the definition goes last.
		for i := len(s.tracked) - 1; i >= 0; i-- {
			owned := s.tracked[i]

			victim := &unstructured.Unstructured{}
			victim.SetGroupVersionKind(owned.gvk)
			victim.SetNamespace(owned.namespace)
			victim.SetName(owned.name)

			uid := owned.uid

			err := client.Delete(ctx, victim, &crclient.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &uid},
			})
			if err == nil || apierrors.IsNotFound(err) {
				continue
			}

			t.Errorf("cleaning up %s %s/%s (uid %s): %v",
				owned.gvk.Kind, owned.namespace, owned.name, owned.uid, err)
		}
	})

	return s
}

// render substitutes the scenario's unique suffix, its controller
// image strategy, and its authored values into one fixture template.
func (s *scenario) render(template string, controllerImage string) string {
	rendered := strings.ReplaceAll(template, "UNIQUESUFFIX", s.suffix)
	rendered = strings.ReplaceAll(rendered, "CONTROLLERIMAGE", controllerImage)
	rendered = strings.ReplaceAll(rendered, "VALUEMESSAGE", s.message)
	rendered = strings.ReplaceAll(rendered, "REPLICAS", s.replicas)

	return rendered
}

// writeModuleFile writes content at a module-root-relative path.
func (s *scenario) writeModuleFile(t *testing.T, rel string, content string) {
	t.Helper()

	path := filepath.Join(s.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeHelmModule materializes the Helm scenario tree: the controller
// package, the payload CRD, the value, co-located, and gate maps, the
// payload-supplied Namespace, and any scenario-specific extra
// templates.
func (s *scenario) writeHelmModule(
	t *testing.T,
	controllerImage string,
	extraTemplates map[string]string,
) {
	t.Helper()

	s.writeModuleFile(t, "definition.yaml", s.render(helmDefinitionTemplate, controllerImage))
	s.writeModuleFile(t, "manifests/chart/Chart.yaml", chartYAML)
	s.writeModuleFile(t, "manifests/chart/values.yaml", chartValuesYAML)
	s.writeModuleFile(t, "manifests/chart/templates/deployment.yaml", deploymentTemplate)
	s.writeModuleFile(t, "manifests/chart/templates/unrelated.yaml", unrelatedDeploymentTemplate)
	s.writeModuleFile(t, "manifests/chart/templates/serviceaccount.yaml", serviceAccountTemplate)
	s.writeModuleFile(t, "manifests/chart/templates/role.yaml", roleTemplate)
	s.writeModuleFile(t, "manifests/chart/templates/rolebinding.yaml", roleBindingTemplate)
	s.writeModuleFile(t, "manifests/chart/templates/crd.yaml", s.render(payloadCRDTemplate, controllerImage))
	s.writeModuleFile(t, "manifests/chart/templates/values-cm.yaml", valuesConfigMapTemplate)
	s.writeModuleFile(t, "manifests/chart/templates/shared-target.yaml", s.render(sharedTargetConfigMapTemplate, controllerImage))
	s.writeModuleFile(t, "manifests/chart/templates/shared-aux.yaml", s.render(sharedAuxConfigMapTemplate, controllerImage))
	s.writeModuleFile(t, "manifests/chart/templates/gate-cm.yaml", gateConfigMapTemplate)
	s.writeModuleFile(t, "manifests/chart/templates/namespace.yaml", s.render(auxNamespaceTemplate, controllerImage))

	for _, name := range sortedKeys(extraTemplates) {
		s.writeModuleFile(t, "manifests/chart/templates/"+name, s.render(extraTemplates[name], controllerImage))
	}
}

// writeKustomizeModule materializes the Kustomize scenario tree:
// plain resources placed by the engine's real namespace plugin.
func (s *scenario) writeKustomizeModule(t *testing.T, controllerImage string) {
	t.Helper()

	s.writeModuleFile(t, "definition.yaml", s.render(kustomizeDefinitionTemplate, controllerImage))
	s.writeModuleFile(t, "manifests/kustomization.yaml", kustomizationYAML)
	s.writeModuleFile(t, "manifests/base/kustomization.yaml", baseKustomizationYAML)
	s.writeModuleFile(t, "manifests/base/deployment.yaml", kustomizeDeploymentYAML)
	s.writeModuleFile(t, "manifests/crd.yaml", s.render(payloadCRDTemplate, controllerImage))
	s.writeModuleFile(t, "manifests/namespace.yaml", s.render(auxNamespaceTemplate, controllerImage))
	s.writeModuleFile(t, "manifests/serviceaccount.yaml", kustomizeServiceAccountYAML)
	s.writeModuleFile(t, "manifests/role.yaml", kustomizeRoleYAML)
	s.writeModuleFile(t, "manifests/rolebinding.yaml", kustomizeRoleBindingYAML)
	s.writeModuleFile(t, "manifests/values.yaml", kustomizeValuesConfigMapYAML)
	s.writeModuleFile(t, "manifests/gate.yaml", kustomizeGateConfigMapYAML)
	s.writeModuleFile(t, "manifests/shared.yaml", kustomizeSharedConfigMapYAML)
}

// sortedKeys returns the map's keys in sorted order, so module trees
// materialize deterministically.
func sortedKeys(entries map[string]string) []string {
	return slices.Sorted(maps.Keys(entries))
}

// failClosed proves the scenario's identities are unused before any
// CLI call: the definition, the payload CRD, and both namespaces must
// be absent. Any hit is a foreign object — on an external cluster
// especially — and the scenario refuses to run over it. A kind the
// server does not serve yet is absent too: on a fresh cluster the
// definition CRD is only bootstrapped by the first run.
func (s *scenario) failClosed(t *testing.T) {
	t.Helper()

	g := NewWithT(t)

	client := engine(t).Client()

	checks := []struct {
		gvk  schema.GroupVersionKind
		name string
		what string
	}{
		{definitionGVK, s.name, "definition"},
		{crdGVK, "syntheticmodules." + s.group, "payload CRD"},
		{namespaceGVK, s.targetNS, "target namespace"},
		{namespaceGVK, s.auxNS, "payload namespace"},
	}

	for _, check := range checks {
		found := &unstructured.Unstructured{}
		found.SetGroupVersionKind(check.gvk)

		err := client.Get(t.Context(), crclient.ObjectKey{Name: check.name}, found)
		if err == nil {
			t.Fatalf("fail closed: %s %q already exists; refusing to run over a foreign object",
				check.what, check.name)
		}

		g.Expect(isAbsent(err)).To(BeTrue(),
			"checking the absence of the %s %q: %v", check.what, check.name, err)
	}
}

// isAbsent reports whether a lookup error proves the object cannot
// exist: a NotFound status, or the kind itself not being served yet.
func isAbsent(err error) bool {
	return apierrors.IsNotFound(err) || meta.IsNoMatchError(err)
}

// createNamespace creates one namespace the scenario owns: tracked
// and cleaned up by exact UID. The payload-supplied Namespace is
// deliberately not created here — the framework's own apply must
// create it.
func (s *scenario) createNamespace(t *testing.T, name string) {
	t.Helper()

	g := NewWithT(t)

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}

	g.Expect(engine(t).Client().Create(t.Context(), namespace)).To(Succeed())

	s.track(t, namespaceGVK, "", name)
}

// track records one existing object's server identity for the
// scenario's exact-UID cleanup.
func (s *scenario) track(t *testing.T, gvk schema.GroupVersionKind, namespace string, name string) {
	t.Helper()

	client := engine(t).Client()

	found := &unstructured.Unstructured{}
	found.SetGroupVersionKind(gvk)

	err := client.Get(t.Context(), crclient.ObjectKey{Namespace: namespace, Name: name}, found)
	if err != nil {
		t.Fatalf("tracking %s %s/%s: %v", gvk.Kind, namespace, name, err)
	}

	s.tracked = append(s.tracked, trackedObject{
		gvk:       gvk,
		namespace: namespace,
		name:      name,
		uid:       found.GetUID(),
	})
}

// trackRunObjects records the objects a successful run created: the
// persisted definition, the payload CRD, and the payload-supplied
// Namespace.
func (s *scenario) trackRunObjects(t *testing.T) {
	t.Helper()

	s.track(t, definitionGVK, "", s.name)
	s.track(t, crdGVK, "", "syntheticmodules."+s.group)
	s.track(t, namespaceGVK, "", s.auxNS)
}

// runCLI executes the real CLI binary against the engine's cluster
// through the standard inherited client flags — the engine's private
// kubeconfig and explicit context — and decodes the JSON report.
func (s *scenario) runCLI(t *testing.T, extra ...string) (runcli.Report, string, int) {
	t.Helper()

	return s.runCLIWithEnv(t, nil, extra...)
}

// runCLIWithEnv is runCLI with additional environment entries, for
// the ambient related-image source cases.
func (s *scenario) runCLIWithEnv(
	t *testing.T,
	env []string,
	extra ...string,
) (runcli.Report, string, int) {
	t.Helper()

	live := engine(t)

	args := []string{
		"module",
		"run",
		"--config", filepath.Join(s.root, "definition.yaml"),
		"--platform-version", "2.19.0",
		"--kubeconfig", live.KubeconfigPath(),
		"--context", live.ContextName(),
		"--namespace", s.runNS,
		"--timeout", "3m",
		"--output", "json",
	}
	args = append(args, extra...)

	var stdout, stderr bytes.Buffer

	command := exec.CommandContext(t.Context(), cliBinary, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if env != nil {
		command.Env = append(os.Environ(), env...)
	}

	exitCode := runcli.ExitSuccess
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = runcli.ExitFailure
		}
	}

	var report runcli.Report
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("decoding the run report: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
		}
	}

	return report, stderr.String(), exitCode
}

// imagesFile writes a dotenv related-images file into the scenario's
// module root and returns its path.
func (s *scenario) imagesFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(s.root, "images.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// deployment reads one live Deployment.
func (s *scenario) deployment(t *testing.T, namespace string, name string) *appsv1.Deployment {
	t.Helper()

	deployment := &appsv1.Deployment{}

	if err := engine(t).Client().Get(t.Context(),
		crclient.ObjectKey{Namespace: namespace, Name: name}, deployment); err != nil {
		t.Fatalf("reading deployment %s/%s: %v", namespace, name, err)
	}

	return deployment
}

// configMap reads one live ConfigMap.
func (s *scenario) configMap(t *testing.T, namespace string, name string) *corev1.ConfigMap {
	t.Helper()

	configMap := &corev1.ConfigMap{}

	if err := engine(t).Client().Get(t.Context(),
		crclient.ObjectKey{Namespace: namespace, Name: name}, configMap); err != nil {
		t.Fatalf("reading config map %s/%s: %v", namespace, name, err)
	}

	return configMap
}

// namespace reads one live Namespace.
func (s *scenario) namespace(t *testing.T, name string) *corev1.Namespace {
	t.Helper()

	found := &corev1.Namespace{}

	if err := engine(t).Client().Get(t.Context(),
		crclient.ObjectKey{Name: name}, found); err != nil {
		t.Fatalf("reading namespace %s: %v", name, err)
	}

	return found
}

// definition reads the persisted module definition.
func (s *scenario) definition(t *testing.T) *toolApi.PlatformModuleDefinition {
	t.Helper()

	definition := &toolApi.PlatformModuleDefinition{}

	if err := engine(t).Client().Get(t.Context(),
		crclient.ObjectKey{Name: s.name}, definition); err != nil {
		t.Fatalf("reading the persisted definition %s: %v", s.name, err)
	}

	return definition
}

// payloadCRD reads the payload type's live CRD.
func (s *scenario) payloadCRD(t *testing.T) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()

	crd := &apiextensionsv1.CustomResourceDefinition{}

	if err := engine(t).Client().Get(t.Context(),
		crclient.ObjectKey{Name: "syntheticmodules." + s.group}, crd); err != nil {
		t.Fatalf("reading the payload CRD: %v", err)
	}

	return crd
}

// roleBinding reads one live RoleBinding.
func (s *scenario) roleBinding(t *testing.T, namespace string, name string) *rbacv1.RoleBinding {
	t.Helper()

	binding := &rbacv1.RoleBinding{}

	if err := engine(t).Client().Get(t.Context(),
		crclient.ObjectKey{Namespace: namespace, Name: name}, binding); err != nil {
		t.Fatalf("reading role binding %s/%s: %v", namespace, name, err)
	}

	return binding
}

// containerByName returns one container of a pod spec.
func containerByName(t *testing.T, podSpec corev1.PodSpec, name string) corev1.Container {
	t.Helper()

	for _, container := range podSpec.Containers {
		if container.Name == name {
			return container
		}
	}

	for _, container := range podSpec.InitContainers {
		if container.Name == name {
			return container
		}
	}

	t.Fatalf("container %q not found", name)

	return corev1.Container{}
}

// envVar returns one container env var and whether it is present.
func envVar(container corev1.Container, name string) (corev1.EnvVar, bool) {
	for _, env := range container.Env {
		if env.Name == name {
			return env, true
		}
	}

	return corev1.EnvVar{}, false
}

// uniqueSuffix renders one collision-free scenario suffix.
func uniqueSuffix() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}

	return hex.EncodeToString(buffer)
}
