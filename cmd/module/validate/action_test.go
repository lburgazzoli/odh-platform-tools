package validate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/validator"
)

// validateDefinitionYAML is a complete module definition: rendering
// sources and image tokens are deliberately absent from the check
// paths, so validation never needs them.
const validateDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: kustomize-module
spec:
  instance:
    name: default-kustomize-module
    type:
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: KustomizeModule
  manifests:
    format: kustomize
  relatedImages:
  - RELATED_IMAGE_MOCK_CONTROLLER
  controller:
    deployment:
      name: mock-controller
      container:
        image: "{{RELATED_IMAGE_MOCK_CONTROLLER}}"
`

// writeDefinition stages the definition fixture and returns its path.
func writeDefinition(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "module.yaml")
	if err := os.WriteFile(path, []byte(validateDefinitionYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// blockingServer accepts every request and answers only when the
// client goes away, so context deadlines fire deterministically.
func blockingServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(
		func(_ http.ResponseWriter, request *http.Request) {
			<-request.Context().Done()
		}))

	t.Cleanup(server.Close)

	return server
}

// TestActionRejectsInvalidOptionsBeforeClusterAccess checks option
// validation happens before any configuration or cluster access: a
// nil getter cannot be dereferenced.
func TestActionRejectsInvalidOptionsBeforeClusterAccess(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	_, err := Action(t.Context(), nil, validator.Options{
		DefinitionFile: " ",
		Timeout:        time.Minute,
	})
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidValidateOptions)).To(BeTrue())

	_, err = Action(t.Context(), nil, validator.Options{
		DefinitionFile: "definition.yaml",
		Timeout:        0,
	})
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidValidateOptions)).To(BeTrue())
}

// TestActionRejectsExpiredContextBeforeClusterAccess checks the
// pre-flight context check precedes configuration access.
func TestActionRejectsExpiredContextBeforeClusterAccess(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Action(ctx, nil, validator.Options{
		DefinitionFile: "definition.yaml",
		Timeout:        time.Minute,
	})

	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
}

// TestActionDefinitionFailureRetainsNamespace checks that a missing
// definition file is a definition input failure whose report keeps the
// resolved namespace.
func TestActionDefinitionFailureRetainsNamespace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result, err := Action(t.Context(), kubeconfigFor(t, "https://127.0.0.1:6443"),
		validator.Options{
			DefinitionFile: filepath.Join(t.TempDir(), "missing.yaml"),
			Timeout:        time.Minute,
		})

	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	g.Expect(result.Namespace).To(Equal("run-namespace"))
}

// TestActionResolvesContextNamespaceUnderDeadline checks namespace
// resolution inside the timed action: the context namespace is
// retained on the timeout outcome.
func TestActionResolvesContextNamespaceUnderDeadline(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := blockingServer(t)

	result, err := Action(t.Context(), kubeconfigFor(t, server.URL),
		validator.Options{
			DefinitionFile: writeDefinition(t),
			Timeout:        300 * time.Millisecond,
		})

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(result.Namespace).To(Equal("run-namespace"))
}

// TestActionCallerDeadlineShortensBudget checks that an earlier caller
// deadline wins over the configured timeout.
func TestActionCallerDeadlineShortensBudget(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := blockingServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()

	result, err := Action(ctx, kubeconfigFor(t, server.URL), validator.Options{
		DefinitionFile: writeDefinition(t),
		Timeout:        time.Minute,
	})

	elapsed := time.Since(started)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(result.Namespace).To(Equal("run-namespace"))
	// The caller's 200ms deadline fired, not the configured minute.
	g.Expect(elapsed).To(BeNumerically("<", 30*time.Second))
}

// TestActionExplicitNamespaceWins checks the standard precedence:
// the explicit namespace flag overrides the kubeconfig context.
func TestActionExplicitNamespaceWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	server := blockingServer(t)

	getter := getterFromKubeconfig(t, kubeconfigYAMLFor(server.URL), "explicit-ns")

	result, err := Action(t.Context(), getter, validator.Options{
		DefinitionFile: writeDefinition(t),
		Timeout:        300 * time.Millisecond,
	})

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(result.Namespace).To(Equal("explicit-ns"))
}

// kubeconfigYAMLFor renders the standalone kubeconfig document for one
// server URL.
func kubeconfigYAMLFor(serverURL string) string {
	return `apiVersion: v1
kind: Config
clusters:
- name: test-cluster
  cluster:
    server: ` + serverURL + `
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
    namespace: run-namespace
current-context: test-context
users:
- name: test-user
  user: {}
`
}

// TestActionRuntimeFailureFromUnreachableCluster checks that a
// connection failure is a runtime failure, never an absence, and the
// resolved namespace is retained.
func TestActionRuntimeFailureFromUnreachableCluster(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result, err := Action(t.Context(), kubeconfigFor(t, "https://127.0.0.1:1"),
		validator.Options{
			DefinitionFile: writeDefinition(t),
			Timeout:        30 * time.Second,
		})

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrValidateRuntime)).To(BeTrue())
	g.Expect(result.Namespace).To(Equal("run-namespace"))
}
