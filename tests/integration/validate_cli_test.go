//go:build integration

// Validate CLI scenarios: the real process, the real default Action,
// and the real validator service against the scripted read-only
// endpoint — success, bounded waiting, rediscovery, held negative
// states, stale and malformed status, denied reads, invalid local
// input, and signal cancellation.
package integration

import (
	"os"
	"os/exec"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	validatecli "github.com/lburgazzoli/odh-platform-tools/cmd/module/validate"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/validator"
)

// TestCLIValidateSuccess runs the complete real-process chain against
// an all-pass endpoint whose platform versions match only after
// tolerant normalization on both sides. No render source exists.
func TestCLIValidateSuccess(t *testing.T) {
	t.Parallel()

	for name, format := range map[string][]string{
		"text": {"--output", "text"},
		"json": {"--output", "json"},
	} {
		format := format

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			definition := writeValidateDefinition(t)

			outcome := runValidateCLI(t, healthyValidateAPI(), definition, format...)

			g.Expect(outcome.exitCode).To(Equal(0), outcome.stderr)
			g.Expect(outcome.stderr).To(BeEmpty())

			if format[1] == "json" {
				report := validateJSONReport(t, outcome.stdout)

				g.Expect(report.Status).To(Equal("pass"))
				g.Expect(report.Namespace).To(Equal(validateNamespace))
				g.Expect(report.Definition).To(Equal(definition))
				g.Expect(report.Failure).To(BeNil())

				g.Expect(report.Checks).To(HaveLen(7))
				for i := range report.Checks {
					g.Expect(report.Checks[i].Status).To(Equal(validator.StatusPass),
						"check %s", report.Checks[i].ID)
				}

				g.Expect(report.Checks[0].ID).To(Equal(validator.CheckModuleAPI))
				g.Expect(report.Checks[6].ID).To(Equal(validator.CheckPlatformConsumed))

				return
			}

			g.Expect(outcome.stdout).To(ContainSubstring(
				"module validate " + definition + ": pass"))
			g.Expect(outcome.stdout).To(ContainSubstring(
				"namespace: " + validateNamespace))
			g.Expect(outcome.stdout).To(ContainSubstring("MODULE_API: pass"))
			g.Expect(outcome.stdout).To(ContainSubstring("PLATFORM_CONSUMED: pass"))
		})
	}
}

// TestCLIValidateWaitsUntilHealthy proves the default invocation
// waits through an initially unready module, a mid-rollout
// Deployment, and a mismatched platform version until every check
// passes together in one cycle.
func TestCLIValidateWaitsUntilHealthy(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	api := healthyValidateAPI()
	api.module = []validateResponse{
		validateOK(validateUnreadyModuleJSON),
		validateOK(validateHealthyModuleJSON),
	}
	api.deployment = []validateResponse{
		validateOK(validateUnreadyDeploymentJSON),
		validateOK(validateHealthyDeploymentJSON),
	}
	api.configMap = []validateResponse{
		validateOK(validateMismatchedConfigMapJSON),
		validateOK(validateHealthyConfigMapJSON),
	}

	outcome := runValidateCLI(t, api, writeValidateDefinition(t))

	g.Expect(outcome.exitCode).To(Equal(0), outcome.stderr)
	g.Expect(outcome.stderr).To(BeEmpty())
	g.Expect(outcome.stdout).To(ContainSubstring(": pass"))
}

// TestCLIValidateMissingAPIBecomesDiscoverable proves a group/version that is
// initially absent becomes discoverable and passes during the same
// invocation.
func TestCLIValidateMissingAPIBecomesDiscoverable(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	api := healthyValidateAPI()
	api.discovery = []validateResponse{
		validateMissing(),
		validateOK(validateDiscoveryJSON),
	}

	outcome := runValidateCLI(t, api, writeValidateDefinition(t))

	g.Expect(outcome.exitCode).To(Equal(0), outcome.stderr)
	g.Expect(outcome.stdout).To(ContainSubstring(": pass"))
}

// TestCLIValidateModuleCreatedDuringInvocation proves the
// before-creation case inside one invocation: a custom resource that
// is initially missing is picked up by a later cycle.
func TestCLIValidateModuleCreatedDuringInvocation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	api := healthyValidateAPI()
	api.module = []validateResponse{
		validateMissing(),
		validateOK(validateHealthyModuleJSON),
	}

	outcome := runValidateCLI(t, api, writeValidateDefinition(t))

	g.Expect(outcome.exitCode).To(Equal(0), outcome.stderr)
	g.Expect(outcome.stdout).To(ContainSubstring(": pass"))
}

// TestCLIValidateTimeoutWithFindings proves a persistent missing
// custom resource plus independent defects exit 1 at a short explicit
// budget, with every independent finding and the dependent skips in
// the final report.
func TestCLIValidateTimeoutWithFindings(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	api := healthyValidateAPI()
	api.module = []validateResponse{validateMissing()}
	api.deployment = []validateResponse{validateOK(validateScaledZeroDeploymentJSON)}

	outcome := runValidateCLI(t, api, writeValidateDefinition(t),
		"--timeout", "2s", "--output", "json")

	g.Expect(outcome.exitCode).To(Equal(1))
	g.Expect(outcome.stderr).To(ContainSubstring("timeout"))

	report := validateJSONReport(t, outcome.stdout)

	g.Expect(report.Status).To(Equal("failed"))
	g.Expect(report.Namespace).To(Equal(validateNamespace))
	g.Expect(report.Failure).NotTo(BeNil())
	g.Expect(report.Failure.Category).To(Equal("timeout"))

	statuses := map[string]validatecli.Check{}
	for i := range report.Checks {
		statuses[report.Checks[i].ID] = report.Checks[i]
	}

	// Independent findings stay observable.
	g.Expect(statuses[validator.CheckModuleAPI].Status).To(Equal(validator.StatusPass))
	g.Expect(statuses[validator.CheckModuleExists].Status).To(Equal(validator.StatusFail))
	g.Expect(statuses[validator.CheckDeployment].Status).To(Equal(validator.StatusFail))
	g.Expect(statuses[validator.CheckDeployment].Field).To(Equal("spec.replicas"))
	g.Expect(statuses[validator.CheckPlatformConfig].Status).To(Equal(validator.StatusPass))

	// Dependent checks skip: no custom resource to diagnose.
	g.Expect(statuses[validator.CheckModuleContract].Status).To(Equal(validator.StatusSkipped))
	g.Expect(statuses[validator.CheckModuleReady].Status).To(Equal(validator.StatusSkipped))
	g.Expect(statuses[validator.CheckPlatformConsumed].Status).To(Equal(validator.StatusSkipped))
}

// TestCLIValidateHoldsNegativeStateThenRecovers proves a negative
// state is held for the whole budget — the final report asserts the
// failed checks, fields, and skips — and that a second invocation
// after the state is restored succeeds.
func TestCLIValidateHoldsNegativeStateThenRecovers(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	definition := writeValidateDefinition(t)

	api := healthyValidateAPI()
	api.module = []validateResponse{validateOK(validateUnreadyModuleJSON)}

	first := runValidateCLI(t, api, definition, "--timeout", "2s", "--output", "json")

	g.Expect(first.exitCode).To(Equal(1))
	g.Expect(first.stderr).To(ContainSubstring("timeout"))

	report := validateJSONReport(t, first.stdout)

	g.Expect(report.Status).To(Equal("failed"))

	statuses := map[string]validatecli.Check{}
	for i := range report.Checks {
		statuses[report.Checks[i].ID] = report.Checks[i]
	}

	g.Expect(statuses[validator.CheckModuleAPI].Status).To(Equal(validator.StatusPass))
	g.Expect(statuses[validator.CheckModuleExists].Status).To(Equal(validator.StatusPass))
	g.Expect(statuses[validator.CheckModuleContract].Status).To(Equal(validator.StatusPass))
	g.Expect(statuses[validator.CheckModuleReady].Status).To(Equal(validator.StatusFail))
	g.Expect(statuses[validator.CheckModuleReady].Field).To(Equal("status.phase"))
	g.Expect(statuses[validator.CheckDeployment].Status).To(Equal(validator.StatusPass))
	g.Expect(statuses[validator.CheckPlatformConfig].Status).To(Equal(validator.StatusPass))
	g.Expect(statuses[validator.CheckPlatformConsumed].Status).To(Equal(validator.StatusPass))

	// Restoring the state makes a second invocation succeed.
	second := runValidateCLI(t, healthyValidateAPI(), definition)

	g.Expect(second.exitCode).To(Equal(0), second.stderr)
	g.Expect(second.stdout).To(ContainSubstring(": pass"))
}

// TestCLIValidateStaleAndMalformedModule proves a stale or malformed
// custom resource cannot pass against an otherwise healthy endpoint,
// covering both generation staleness forms.
func TestCLIValidateStaleAndMalformedModule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		body  string
		check string
		field string
		skip  string
	}{
		{
			name:  "stale status generation",
			body:  validateStaleModuleJSON,
			check: validator.CheckModuleReady,
			field: "status.observedGeneration",
			skip:  "",
		},
		{
			name:  "stale condition generation",
			body:  validateStaleConditionModuleJSON,
			check: validator.CheckModuleReady,
			field: "status.conditions",
			skip:  "",
		},
		{
			name:  "malformed status",
			body:  validateMalformedModuleJSON,
			check: validator.CheckModuleContract,
			field: "status.phase",
			skip:  validator.CheckModuleReady,
		},
	}

	for i := range tests {
		test := tests[i]

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			api := healthyValidateAPI()
			api.module = []validateResponse{validateOK(test.body)}

			outcome := runValidateCLI(t, api, writeValidateDefinition(t),
				"--timeout", "2s", "--output", "json")

			g.Expect(outcome.exitCode).To(Equal(1))
			g.Expect(outcome.stderr).To(ContainSubstring("timeout"))

			report := validateJSONReport(t, outcome.stdout)

			g.Expect(report.Status).To(Equal("failed"))

			statuses := map[string]validatecli.Check{}
			for j := range report.Checks {
				statuses[report.Checks[j].ID] = report.Checks[j]
			}

			g.Expect(statuses[test.check].Status).To(Equal(validator.StatusFail))
			g.Expect(statuses[test.check].Field).To(Equal(test.field))

			if test.skip != "" {
				g.Expect(statuses[test.skip].Status).To(Equal(validator.StatusSkipped))
			}

			// Independent checks stay healthy.
			g.Expect(statuses[validator.CheckDeployment].Status).To(Equal(validator.StatusPass))
			g.Expect(statuses[validator.CheckPlatformConfig].Status).To(Equal(validator.StatusPass))
		})
	}
}

// TestCLIValidateDeniedReadIsRuntime proves a forbidden read is an
// execution failure classified as runtime, not an absence: the CLI
// fails promptly instead of holding the finding to the budget.
func TestCLIValidateDeniedReadIsRuntime(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	api := healthyValidateAPI()
	api.module = []validateResponse{validateDenied()}

	started := time.Now()

	outcome := runValidateCLI(t, api, writeValidateDefinition(t),
		"--timeout", "30s", "--output", "json")

	g.Expect(outcome.exitCode).To(Equal(1))
	g.Expect(outcome.stderr).To(ContainSubstring("runtime"))

	// The runtime failure returns well before the configured budget.
	g.Expect(time.Since(started)).To(BeNumerically("<", 20*time.Second))

	report := validateJSONReport(t, outcome.stdout)

	g.Expect(report.Failure).NotTo(BeNil())
	g.Expect(report.Failure.Category).To(Equal("runtime"))

	statuses := map[string]validatecli.Check{}
	for i := range report.Checks {
		statuses[report.Checks[i].ID] = report.Checks[i]
	}

	g.Expect(statuses[validator.CheckModuleExists].Status).To(Equal(validator.StatusError))
	g.Expect(statuses[validator.CheckDeployment].Status).To(Equal(validator.StatusPass))
}

// TestCLIValidateInvalidInputNeverContactsCluster proves invalid
// local input exits 2 before any endpoint contact.
func TestCLIValidateInvalidInputNeverContactsCluster(t *testing.T) {
	t.Parallel()

	t.Run("invalid definition", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		api := healthyValidateAPI()

		definition := writeValidateDefinitionContent(t, validateDefinitionNoTypeYAML)

		outcome := runValidateCLI(t, api, definition)

		g.Expect(outcome.exitCode).To(Equal(2))
		g.Expect(outcome.stderr).To(ContainSubstring("definition"))
		g.Expect(api.requestCount()).To(Equal(0))
	})

	t.Run("invalid options", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		api := healthyValidateAPI()

		outcome := runValidateCLI(t, api, writeValidateDefinition(t),
			"--timeout", "0")

		g.Expect(outcome.exitCode).To(Equal(2))
		g.Expect(outcome.stderr).To(ContainSubstring("invalid-options"))
		g.Expect(api.requestCount()).To(Equal(0))
	})
}

// TestCLIValidateCancellation proves an interrupt during a blocked
// context-aware read ends the process promptly with the cancellation
// classification and a valid final report. The signal follows a
// synchronization barrier — the endpoint has already served a request
// — and the watchdog is the only forced kill.
func TestCLIValidateCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	api := healthyValidateAPI()
	api.discovery = []validateResponse{validateHeld()}

	definition := writeValidateDefinition(t)

	outcome := startValidateCLI(t, api, definition, func(command *exec.Cmd) {
		// Barrier: wait until the endpoint has served the blocked
		// read before signaling, so the signal lands during the
		// blocked request rather than before it.
		g.Eventually(func() int {
			return api.requestCount()
		}, 10*time.Second, 50*time.Millisecond).Should(BeNumerically(">", 0))

		g.Expect(command.Process.Signal(os.Interrupt)).To(Succeed())
	}, "--output", "json")

	g.Expect(outcome.exitCode).To(Equal(1))
	g.Expect(outcome.stderr).To(ContainSubstring("cancelled"))

	report := validateJSONReport(t, outcome.stdout)

	g.Expect(report.Status).To(Equal("failed"))
	g.Expect(report.Failure).NotTo(BeNil())
	g.Expect(report.Failure.Category).To(Equal("cancelled"))
	g.Expect(report.Namespace).To(Equal(validateNamespace))
	g.Expect(report.Checks).To(HaveLen(7))
}
