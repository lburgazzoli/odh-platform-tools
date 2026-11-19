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
	"encoding/json/v2"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/util/retry"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	validatecli "github.com/lburgazzoli/odh-platform-tools/cmd/module/validate"
)

// validateCLIOutcome is one live validate invocation's observable
// result: the decoded JSON report, stderr, and the exit code.
type validateCLIOutcome struct {
	report   validatecli.Report
	stderr   string
	exitCode int
}

// validateProcess is one started validate invocation whose result is
// collected later, so scenarios can start validation before a state
// change and prove the same invocation waited through it.
type validateProcess struct {
	command *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
}

// startValidateCLI starts the real CLI's validate command against the
// engine's cluster through the standard inherited client flags — the
// engine's private kubeconfig and explicit context — with JSON output.
// The command dies with the test context; wait collects its result.
func (s *scenario) startValidateCLI(t *testing.T, extra ...string) *validateProcess {
	t.Helper()

	live := engine(t)

	args := []string{
		"module", "validate",
		"--config", filepath.Join(s.root, s.definitionFile),
		"--kubeconfig", live.KubeconfigPath(),
		"--context", live.ContextName(),
		"--namespace", s.runNS,
		"--output", "json",
	}
	args = append(args, extra...)

	process := &validateProcess{
		command: exec.CommandContext(t.Context(), cliBinary, args...),
	}
	process.command.Stdout = &process.stdout
	process.command.Stderr = &process.stderr

	if err := process.command.Start(); err != nil {
		t.Fatalf("starting module validate: %v", err)
	}

	return process
}

// wait collects the started invocation's result and decodes its
// report.
func (p *validateProcess) wait(t *testing.T) validateCLIOutcome {
	t.Helper()

	exitCode := validatecli.ExitSuccess

	if err := p.command.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = validatecli.ExitFailure
		}
	}

	outcome := validateCLIOutcome{
		stderr:   p.stderr.String(),
		exitCode: exitCode,
	}

	if p.stdout.Len() > 0 {
		if err := json.Unmarshal(p.stdout.Bytes(), &outcome.report); err != nil {
			t.Fatalf("decoding the validate report: %v\nstdout: %s\nstderr: %s",
				err, p.stdout.String(), outcome.stderr)
		}
	}

	return outcome
}

// runValidateCLI executes one complete live validate invocation.
func (s *scenario) runValidateCLI(t *testing.T, extra ...string) validateCLIOutcome {
	t.Helper()

	return s.startValidateCLI(t, extra...).wait(t)
}

// validateChecks indexes one report's checks by ID.
func validateChecks(report validatecli.Report) map[string]validatecli.Check {
	checks := make(map[string]validatecli.Check, len(report.Checks))
	for i := range report.Checks {
		checks[report.Checks[i].ID] = report.Checks[i]
	}

	return checks
}

// scaleController scales the run-owned controller Deployment to the
// given replica count, preconditioned on the tracked UID so a
// replacement object is never mutated.
func (s *scenario) scaleController(t *testing.T, replicas int32) {
	t.Helper()

	g := NewWithT(t)

	kube := engine(t).Client()

	key := crclient.ObjectKey{Namespace: s.targetNS, Name: mockControllerName}
	uid := s.trackedUID(deploymentGVK, key)
	g.Expect(uid).NotTo(BeEmpty(), "expected the controller Deployment %s to be tracked", key)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &appsv1.Deployment{}
		if err := kube.Get(t.Context(), key, current); err != nil {
			return err
		}

		if current.UID != uid {
			return fmt.Errorf("controller Deployment %s has unexpected UID %s", key, current.UID)
		}

		before := current.DeepCopy()
		current.Spec.Replicas = &replicas

		return kube.Patch(t.Context(), current,
			crclient.MergeFromWithOptions(before, crclient.MergeFromWithOptimisticLock{}))
	})
	g.Expect(err).To(Succeed())
}

// awaitControllerScale proves the live Deployment converged to the
// given available replica count.
func (s *scenario) awaitControllerScale(t *testing.T, available int32) {
	t.Helper()

	NewWithT(t).Eventually(func(g Gomega) {
		deployment := s.deployment(t, s.targetNS, mockControllerName)
		g.Expect(deployment.Status.AvailableReplicas).To(Equal(available))
	}).WithContext(t.Context()).WithTimeout(2 * time.Minute).WithPolling(250 * time.Millisecond).Should(Succeed())
}
