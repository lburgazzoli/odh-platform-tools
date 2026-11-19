//go:build integration

// The offline validate CLI harness.
//
// Real boundaries: the production CLI process with its default Action
// — the real loader, selector validation, standard kubeconfig
// resolution, context-aware discovery client, dynamic client, and the
// validator service — over a scripted Kubernetes HTTP endpoint. Only
// the server is simulated: it serves real discovery, custom-resource,
// Deployment, and ConfigMap GET responses that change between polls,
// exactly as a cluster under reconciliation would, and rejects every
// mutation method so the read-only contract is asserted on every
// request.
package integration

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	validatecli "github.com/lburgazzoli/odh-platform-tools/cmd/module/validate"
)

// validateWatchdog is the generous process watchdog for every CLI
// execution: scenarios use short budgets, so a healthy process always
// exits well inside it, and a watchdog kill fails the scenario.
const validateWatchdog = 60 * time.Second

// validateResponse is one scripted HTTP response: a body with 200, a
// status with its JSON document, or a held request that only ends
// when the client disconnects.
type validateResponse struct {
	status int
	body   string
	block  bool
}

// validateOK serves one object body.
func validateOK(body string) validateResponse {
	return validateResponse{body: body}
}

// validateMissing serves the NotFound status.
func validateMissing() validateResponse {
	return validateResponse{status: http.StatusNotFound, body: validateNotFoundJSON}
}

// validateDenied serves the Forbidden status.
func validateDenied() validateResponse {
	return validateResponse{status: http.StatusForbidden, body: validateForbiddenJSON}
}

// validateHeld holds the request until the client disconnects, so a
// blocked context-aware read ends only through cancellation.
func validateHeld() validateResponse {
	return validateResponse{block: true}
}

// validateAPI is the scripted endpoint: per-target response sequences
// that advance once per request and repeat their last response, plus
// a complete request journal for the read-only assertion.
type validateAPI struct {
	mu         sync.Mutex
	discovery  []validateResponse
	module     []validateResponse
	deployment []validateResponse
	configMap  []validateResponse

	discoveryCalls  int
	moduleCalls     int
	deploymentCalls int
	configMapCalls  int

	requests  []string
	mutations []string
}

// served picks the next response of one target, repeating the last
// response once the sequence is exhausted. The caller holds the lock.
func served(responses []validateResponse, calls *int) validateResponse {
	index := *calls
	*calls++

	if index >= len(responses) {
		index = len(responses) - 1
	}

	return responses[index]
}

func (a *validateAPI) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	a.mu.Lock()

	a.requests = append(a.requests, request.Method+" "+request.URL.Path)

	if request.Method != http.MethodGet {
		a.mutations = append(a.mutations, request.Method+" "+request.URL.Path)
		a.mu.Unlock()

		writer.WriteHeader(http.StatusMethodNotAllowed)

		return
	}

	var response validateResponse

	switch {
	case request.URL.Path == "/apis":
		response = validateOK(validateAPIGroupListJSON)
	case request.URL.Path == "/api":
		response = validateOK(validateAPIVersionsJSON)
	case request.URL.Path == "/apis/"+validateModuleGroup+"/"+validateModuleVersion:
		response = served(a.discovery, &a.discoveryCalls)
	case request.URL.Path == "/apis/"+validateModuleGroup+"/"+validateModuleVersion+"/"+validateModulePlural+"/"+validateInstanceName:
		response = served(a.module, &a.moduleCalls)
	case request.URL.Path == "/apis/apps/v1/namespaces/"+validateNamespace+"/deployments/"+validateDeploymentName:
		response = served(a.deployment, &a.deploymentCalls)
	case request.URL.Path == "/api/v1/namespaces/"+validateNamespace+"/configmaps/"+validateConfigMapName:
		response = served(a.configMap, &a.configMapCalls)
	default:
		// Unknown paths are visible in the journal so unexpected
		// client bootstrap requests surface in failure diagnostics.
		a.mu.Unlock()

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(validateNotFoundJSON))

		return
	}

	a.mu.Unlock()

	if response.block {
		// The held read ends only when the CLI's context tears the
		// connection down.
		<-request.Context().Done()

		return
	}

	writer.Header().Set("Content-Type", "application/json")

	if response.status != 0 {
		writer.WriteHeader(response.status)
	}

	_, _ = writer.Write([]byte(response.body))
}

// requestCount reports how many requests the endpoint served.
func (a *validateAPI) requestCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return len(a.requests)
}

// mutationAttempts reports every non-GET request the CLI attempted.
func (a *validateAPI) mutationAttempts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.mutations...)
}

// validateCLIOutcome is one real CLI execution's observable result.
type validateCLIOutcome struct {
	stdout   string
	stderr   string
	exitCode int
}

// healthyValidateAPI scripts an all-pass endpoint.
func healthyValidateAPI() *validateAPI {
	return &validateAPI{
		discovery:  []validateResponse{validateOK(validateDiscoveryJSON)},
		module:     []validateResponse{validateOK(validateHealthyModuleJSON)},
		deployment: []validateResponse{validateOK(validateHealthyDeploymentJSON)},
		configMap:  []validateResponse{validateOK(validateHealthyConfigMapJSON)},
	}
}

// writeValidateDefinition stages the validate definition fixture and
// returns its path.
func writeValidateDefinition(t *testing.T) string {
	t.Helper()

	return writeValidateDefinitionContent(t, validateDefinitionYAML)
}

// writeValidateDefinitionContent stages one definition document and
// returns its path.
func writeValidateDefinitionContent(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "definition.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// startValidateCLI runs the built CLI's validate command against the
// scripted endpoint with an explicit temporary kubeconfig, a
// generous watchdog, and an optional hook invoked right after the
// process starts. It never uses the ambient kubeconfig or current
// context.
func startValidateCLI(
	t *testing.T,
	api *validateAPI,
	definitionFile string,
	onStarted func(command *exec.Cmd),
	flags ...string,
) validateCLIOutcome {
	t.Helper()

	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig,
		[]byte(validateKubeconfigYAMLFor(server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}

	args := append([]string{
		"module", "validate",
		"--config", definitionFile,
		"--namespace", validateNamespace,
		"--kubeconfig", kubeconfig,
	}, flags...)

	// The watchdog is the only forced kill in the suite; every
	// scenario budget is far shorter.
	ctx, cancel := context.WithTimeout(context.Background(), validateWatchdog)
	defer cancel()

	var stdout, stderr bytes.Buffer

	command := exec.CommandContext(ctx, cliBinary, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	if onStarted != nil {
		onStarted(command)
	}

	exitCode := 0

	if err := command.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			// The watchdog fired: report it as an invalid exit
			// status so the scenario fails loudly.
			exitCode = -1
		}
	}

	return validateCLIOutcome{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		exitCode: exitCode,
	}
}

// runValidateCLI executes the CLI and asserts the read-only contract:
// no mutation method was ever attempted.
func runValidateCLI(
	t *testing.T,
	api *validateAPI,
	definitionFile string,
	flags ...string,
) validateCLIOutcome {
	t.Helper()

	outcome := startValidateCLI(t, api, definitionFile, nil, flags...)

	NewWithT(t).Expect(api.mutationAttempts()).To(BeEmpty(),
		"the validate CLI attempted cluster mutations")

	return outcome
}

// validateJSONReport decodes one executed JSON report document.
func validateJSONReport(t *testing.T, data string) validatecli.Report {
	t.Helper()

	var report validatecli.Report

	if err := jsonv2.Unmarshal([]byte(data), &report); err != nil {
		t.Fatalf("decoding validate report: %v", err)
	}

	return report
}
