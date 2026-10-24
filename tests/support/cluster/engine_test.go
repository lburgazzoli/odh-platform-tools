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

package cluster

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"k8s.io/client-go/tools/clientcmd"
	kindcluster "sigs.k8s.io/kind/pkg/cluster"
)

// fakeProvider is the injected Kind provider seam: it records every
// operation with its arguments and answers from scripted results.
// It has no List method at all — the engine structurally cannot
// enumerate or purge clusters it does not own.
type fakeProvider struct {
	mu sync.Mutex

	created  []string
	deleted  []string
	logged   []string
	configs  map[string]string
	createFn func(name string) error

	createErr      error
	deleteErr      error
	kubeconfigErr  error
	logsErr        error
	deleteRequests []string
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{}
}

// Create records the cluster creation.
func (f *fakeProvider) Create(name string, _ ...kindcluster.CreateOption) error {
	f.mu.Lock()
	f.created = append(f.created, name)
	f.mu.Unlock()

	if f.createFn != nil {
		return f.createFn(name)
	}

	return f.createErr
}

// Delete records the exact cluster and kubeconfig path deletion.
func (f *fakeProvider) Delete(name, kubeconfigPath string) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, name)
	f.deleteRequests = append(f.deleteRequests, kubeconfigPath)
	f.mu.Unlock()

	return f.deleteErr
}

// KubeConfig returns the scripted kubeconfig content.
func (f *fakeProvider) KubeConfig(name string, _ bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.kubeconfigErr != nil {
		return "", f.kubeconfigErr
	}

	// The "" entry is the default every created cluster resolves to.
	content, ok := f.configs[name]
	if !ok {
		content = f.configs[""]
	}

	return content, nil
}

// CollectLogs records the log collection target.
func (f *fakeProvider) CollectLogs(name, dir string) error {
	f.mu.Lock()
	f.logged = append(f.logged, name+"@"+dir)
	f.mu.Unlock()

	return f.logsErr
}

// apiServer is a minimal discovery double: the engine's Setup proves
// connectivity through one real ServerVersion request.
type apiServer struct {
	server *httptest.Server
}

func newAPIServer(t *testing.T) *apiServer {
	t.Helper()

	api := &apiServer{}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"major":"1","minor":"37","gitVersion":"v1.37.0"}`))
	}))
	t.Cleanup(api.server.Close)

	return api
}

// kubeconfig is a standalone kubeconfig addressing the given server
// under the given context.
func kubeconfig(serverURL string, contextName string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: cluster
  cluster:
    server: %s
contexts:
- name: %s
  context:
    cluster: cluster
    user: user
current-context: %s
users:
- name: user
  user: {}
`, serverURL, contextName, contextName)
}

// newTestKindOptions returns engine options over the injected
// provider and one answering API server.
func newTestKindOptions(t *testing.T, fake *fakeProvider, opts ...Option) options {
	t.Helper()

	api := newAPIServer(t)
	fake.configs = map[string]string{"": kubeconfig(api.server.URL, "kind-test")}

	resolved := newOptions(opts...)
	resolved.provider = fake

	return resolved
}

// TestKindEngineLifecycle proves the managed lifecycle: one unique
// prefixed cluster created with the private kubeconfig path, a
// connected client proven against the answering API, and a Stop that
// deletes exactly the created cluster and removes the private
// directory.
func TestKindEngineLifecycle(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fake := newFakeProvider()
	options := newTestKindOptions(t, fake)

	engine, err := newKind(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(fake.created).To(HaveLen(1))
	g.Expect(fake.created[0]).To(HavePrefix(DefaultNamePrefix))
	g.Expect(engine.Name()).To(Equal(fake.created[0]))

	// The private kubeconfig exists with restrictive permissions and
	// carries the server the engine connected to.
	info, err := os.Stat(engine.KubeconfigPath())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

	g.Expect(engine.ContextName()).To(Equal("kind-" + engine.Name()))
	g.Expect(engine.Config()).NotTo(BeNil())
	g.Expect(engine.Client()).To(BeNil())

	g.Expect(engine.Setup(t.Context())).To(Succeed())
	g.Expect(engine.Client()).NotTo(BeNil())

	g.Expect(engine.Stop(t.Context())).To(Succeed())

	g.Expect(fake.deleted).To(Equal([]string{engine.Name()}))
	g.Expect(fake.deleteRequests).To(Equal([]string{engine.KubeconfigPath()}))

	_, err = os.Stat(engine.KubeconfigPath())
	g.Expect(os.IsNotExist(err)).To(BeTrue())
}

// TestKindEngineUniqueClusterIDs proves two engines never share a
// cluster identity.
func TestKindEngineUniqueClusterIDs(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	first, err := newKind(t.Context(), newTestKindOptions(t, newFakeProvider()))
	g.Expect(err).NotTo(HaveOccurred())

	second, err := newKind(t.Context(), newTestKindOptions(t, newFakeProvider()))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(first.Name()).NotTo(Equal(second.Name()))
}

// TestKindEngineCreateFailureRetainsExactCluster proves a failed
// create reports the exact cluster name, collects its logs into the
// private directory, and attempts an exact-cluster cleanup — nothing
// else is ever deleted.
func TestKindEngineCreateFailureRetainsExactCluster(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fake := newFakeProvider()
	fake.createErr = errors.New("docker unavailable")

	options := newTestKindOptions(t, fake)

	engine, err := newKind(t.Context(), options)
	g.Expect(engine).To(BeNil())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("creating kind cluster"))
	g.Expect(err.Error()).To(ContainSubstring(DefaultNamePrefix))

	// The exact cluster was the only one cleaned up, and its logs
	// were collected beside it.
	g.Expect(fake.created).To(HaveLen(1))
	g.Expect(fake.deleted).To(Equal(fake.created))
	g.Expect(fake.logged).To(HaveLen(1))
	g.Expect(fake.logged[0]).To(SatisfyAll(
		ContainSubstring(fake.created[0]),
		ContainSubstring("logs"),
	))
}

// TestKindEngineDeleteFailureRetainsReport proves a failed deletion
// reports the exact retained cluster and keeps the private artifacts
// for inspection.
func TestKindEngineDeleteFailureRetainsReport(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fake := newFakeProvider()
	fake.deleteErr = errors.New("container runtime is gone")

	options := newTestKindOptions(t, fake)

	engine, err := newKind(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	err = engine.Stop(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(SatisfyAll(
		ContainSubstring(engine.Name()),
		ContainSubstring("NOT deleted"),
		ContainSubstring("removed manually"),
	))

	// The private artifacts survive for inspection.
	_, statErr := os.Stat(engine.KubeconfigPath())
	g.Expect(statErr).NotTo(HaveOccurred())
}

// TestKindEngineCollectLogsIsSeparatelyBounded proves log collection
// is its own supervised operation, callable on failure paths, that
// never deletes anything.
func TestKindEngineCollectLogsIsSeparatelyBounded(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fake := newFakeProvider()

	options := newTestKindOptions(t, fake, WithLogsBudget(50*time.Millisecond))

	engine, err := newKind(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(engine.CollectLogs(t.Context())).To(Succeed())

	g.Expect(fake.logged).To(HaveLen(1))
	g.Expect(fake.deleted).To(BeEmpty())
}

// TestKindEngineKubeConfigFailureCleansUp proves a failed kubeconfig
// read still cleans the exact cluster up and collects logs.
func TestKindEngineKubeConfigFailureCleansUp(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fake := newFakeProvider()
	fake.kubeconfigErr = errors.New("cluster is gone")

	options := newTestKindOptions(t, fake)

	engine, err := newKind(t.Context(), options)
	g.Expect(engine).To(BeNil())
	g.Expect(err).To(HaveOccurred())

	g.Expect(fake.deleted).To(Equal(fake.created))
	g.Expect(fake.logged).To(HaveLen(1))
}

// TestExternalEngineRequiresExplicitConfirmation proves the external
// engine fails clearly without both a kubeconfig path and a context
// name — there is no ambient fallback.
func TestExternalEngineRequiresExplicitConfirmation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	_, err := NewExternal(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("explicit kubeconfig"))

	kubeconfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	if writeErr := os.WriteFile(kubeconfigPath, []byte(kubeconfig("https://example.com", "explicit")), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	_, err = NewExternal(t.Context(), WithKubeconfig(kubeconfigPath))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("explicit context"))
}

// TestExternalEngineUsesExplicitContextNotCurrent proves the
// confirmed context wins over the file's current-context: the
// configuration addresses the explicit context's cluster.
func TestExternalEngineUsesExplicitContextNotCurrent(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters:
- name: current-cluster
  cluster:
    server: https://current.example.com
- name: explicit-cluster
  cluster:
    server: https://explicit.example.com
contexts:
- name: current
  context:
    cluster: current-cluster
    user: user
- name: explicit
  context:
    cluster: explicit-cluster
    user: user
current-context: current
users:
- name: user
  user: {}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	engine, err := NewExternal(t.Context(), WithKubeconfig(path), WithContext("explicit"))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(engine.Name()).To(Equal("explicit"))
	g.Expect(engine.ContextName()).To(Equal("explicit"))
	g.Expect(engine.Config().Host).To(Equal("https://explicit.example.com"))

	// The external engine owns nothing: Stop never deletes.
	g.Expect(engine.Stop(t.Context())).To(Succeed())
}

// TestNewRejectsUnknownEngineType proves a clear failure for an
// unsupported engine selection.
func TestNewRejectsUnknownEngineType(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	_, err := New(t.Context(), WithType("k3s"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("unsupported engine type"))
}

// TestNewDispatchesConfirmedExternal proves the dispatcher routes an
// explicitly confirmed external configuration without creating
// anything.
func TestNewDispatchesConfirmedExternal(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(kubeconfig("https://example.com", "explicit")), 0o600); err != nil {
		t.Fatal(err)
	}

	engine, err := New(t.Context(),
		WithType(TypeExternal),
		WithKubeconfig(path),
		WithContext("explicit"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(engine.Name()).To(Equal("explicit"))
}

// TestExternalEngineLoadsOffline proves the external configuration
// resolves without any network contact: a syntactically valid
// kubeconfig pointing at an unreachable server still builds the
// engine; only Setup connects.
func TestExternalEngineLoadsOffline(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(kubeconfig("https://127.0.0.1:1", "explicit")), 0o600); err != nil {
		t.Fatal(err)
	}

	engine, err := NewExternal(t.Context(), WithKubeconfig(path), WithContext("explicit"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(engine.Config().Host).To(Equal("https://127.0.0.1:1"))

	// Setup proves connectivity and fails clearly against the
	// unreachable confirmed server.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	g.Expect(engine.Setup(ctx)).NotTo(Succeed())
}

// TestEngineOptionsDefaults pins the recorded defaults: the Kind
// engine's prefix, node image pin, readiness wait, and supervision
// budgets.
func TestEngineOptionsDefaults(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := newOptions()

	g.Expect(options.NamePrefix).To(Equal(DefaultNamePrefix))
	g.Expect(options.NodeImage).To(Equal(DefaultNodeImage))
	g.Expect(options.WaitForReady).To(Equal(DefaultWaitForReady))
	g.Expect(options.CreateBudget).To(Equal(DefaultCreateBudget))
	g.Expect(options.DeleteBudget).To(Equal(DefaultDeleteBudget))
	g.Expect(options.LogsBudget).To(Equal(DefaultLogsBudget))
	g.Expect(options.SuperviseGrace).To(Equal(DefaultSuperviseGrace))
}

// TestEngineOptionsOverrides proves the recorded defaults are
// selectable: prefix, node image, and budget overrides apply.
func TestEngineOptionsOverrides(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := newOptions(
		WithNamePrefix("custom-"),
		WithNodeImage("kindest/node:v1.36.4@sha256:test"),
		WithWaitForReady(45*time.Second),
		WithCreateBudget(time.Minute),
		WithDeleteBudget(2*time.Minute),
		WithLogsBudget(3*time.Minute),
		WithSuperviseGrace(4*time.Second),
	)

	g.Expect(options.NamePrefix).To(Equal("custom-"))
	g.Expect(options.NodeImage).To(Equal("kindest/node:v1.36.4@sha256:test"))
	g.Expect(options.WaitForReady).To(Equal(45 * time.Second))
	g.Expect(options.CreateBudget).To(Equal(time.Minute))
	g.Expect(options.DeleteBudget).To(Equal(2 * time.Minute))
	g.Expect(options.LogsBudget).To(Equal(3 * time.Minute))
	g.Expect(options.SuperviseGrace).To(Equal(4 * time.Second))
}

// TestEngineOptionsValidateRejectsUnsafe proves empty prefixes,
// images, and log functions cannot produce an engine.
func TestEngineOptionsValidateRejectsUnsafe(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	g.Expect((&options{}).validate()).To(MatchError(ContainSubstring("name prefix is empty")))

	withPrefix := newOptions(WithNamePrefix("x"))
	withPrefix.NodeImage = ""

	g.Expect(withPrefix.validate()).To(MatchError(ContainSubstring("node image is empty")))

	withoutLog := newOptions(WithNamePrefix("x"))
	withoutLog.LogFn = nil

	g.Expect(withoutLog.validate()).To(MatchError(ContainSubstring("log function is nil")))
}

// TestKubeconfigRoundTrip proves the fixture helper itself decodes —
// keeping the test kubeconfigs honest.
func TestKubeconfigRoundTrip(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	config, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig("https://example.com", "explicit")))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(config.Host).To(Equal("https://example.com"))
}
