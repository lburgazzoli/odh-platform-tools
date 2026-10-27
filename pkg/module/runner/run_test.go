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

package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// TestRunAppliesOnlyOrchestration proves Run sequences Load → Render
// → Deploy over one invocation context derived from the caller's
// context and the options' timeout, and its Result reports exactly
// the apply-only facts: the server-confirmed owner and apply
// completion — with no readiness claim.
func TestRunAppliesOnlyOrchestration(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "apply"}))

	g.Expect(result).To(SatisfyAll(
		HaveField("Attempted", StageDeploy),
		HaveField("Completed", StageDeploy),
		HaveField("Owner", Not(BeNil())),
		HaveField("ApplyCompleted", BeTrue()),
	))

	// The confirmed owner is the server-backed identity the
	// preparation's definition apply returned.
	g.Expect(result.Owner.Name).To(Equal("synthetic"))
	g.Expect(string(result.Owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))

	// The invocation context the seams saw is the bounded one: it
	// carries a deadline no later than the options' timeout from the
	// run's start.
	deadline, ok := stub.renderCtx.Deadline()
	g.Expect(ok).To(BeTrue())
	g.Expect(deadline).To(BeTemporally("<=", time.Now().Add(testOptions().Timeout)))
	g.Expect(stub.connectCtx).To(Equal(stub.renderCtx))
}

// TestRunRejectsInvalidOptionsBeforeTimer proves a nonpositive
// timeout is an options error, not an artificial deadline error, and
// no stage or service runs.
func TestRunRejectsInvalidOptionsBeforeTimer(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := testOptions()
	options.Timeout = 0

	stub := newStubRunner(t, options)

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(Equal("invalid run options: Timeout must be positive"))
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidRunOptions)).To(BeTrue())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeFalse())

	g.Expect(result).To(Equal(Result{}))
	g.Expect(*stub.calls).To(BeEmpty())
}

// TestRunHonorsCallerCancellation proves an already-cancelled caller
// fails with its own cancellation identity before the invocation
// timer exists and no stage runs.
func TestRunHonorsCallerCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := stub.runner.Run(ctx)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

	g.Expect(result).To(Equal(Result{}))
	g.Expect(*stub.calls).To(BeEmpty())
}

// TestRunRequiresFreshRunner proves Run rejects a Runner whose stages
// were already driven, directly or by a previous Run.
func TestRunRequiresFreshRunner(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())
	g.Expect(stub.runner.Load(t.Context())).To(Succeed())

	result, err := stub.runner.Run(t.Context())
	g.Expect(errors.Is(err, moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(result).To(Equal(Result{}))
	g.Expect(*stub.calls).To(Equal([]string{"load"}))

	fresh := newStubRunner(t, testOptions())
	_, err = fresh.runner.Run(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	_, err = fresh.runner.Run(t.Context())
	g.Expect(errors.Is(err, moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(*fresh.calls).To(Equal([]string{"load", "render", "connect", "apply"}))
}

// TestRunLoadFailureReports proves a Load failure reports the
// attempted stage, no completed stage, and nothing confirmed.
func TestRunLoadFailureReports(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())
	stub.loadError = errors.New("definition failure")

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(MatchError(stub.loadError))

	g.Expect(result).To(Equal(Result{
		Attempted: StageLoad,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load"}))
}

// TestRunRenderFailureReports proves a Render failure reports Load as
// the last successful stage and never reaches a remote seam.
func TestRunRenderFailureReports(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())
	stub.renderError = errors.New("render failure")

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(MatchError(stub.renderError))

	g.Expect(result).To(Equal(Result{
		Attempted: StageRender,
		Completed: StageLoad,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render"}))
}

// TestRunCancellationDuringRenderRejectsLateOutput proves a render
// returning successfully after the invocation expired never becomes
// successful output: the stage fails with the cancellation identity,
// no payload is accepted, and Deploy never runs.
func TestRunCancellationDuringRenderRejectsLateOutput(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())

	ctx, cancel := context.WithCancel(t.Context())
	stub.renderHook = func(context.Context) {
		// The synchronous render finished, but the caller cancelled
		// while it ran: the late return must be rejected.
		cancel()
	}

	result, err := stub.runner.Run(ctx)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

	g.Expect(result).To(Equal(Result{
		Attempted: StageRender,
		Completed: StageLoad,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render"}))
	g.Expect(stub.runner.resources).To(BeNil())

	// The failed invocation is terminal: Deploy never runs.
	g.Expect(errors.Is(stub.runner.Deploy(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(*stub.calls).To(Equal([]string{"load", "render"}))
}

// TestRunCancellationBeforePreparation proves the invocation checks
// the context after connecting and before any preparation or write:
// cancelling there stops the run with nothing confirmed and no
// payload write.
func TestRunCancellationBeforePreparation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())

	ctx, cancel := context.WithCancel(t.Context())
	stub.connectHook = func(context.Context) {
		cancel()
	}

	result, err := stub.runner.Run(ctx)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

	g.Expect(result).To(Equal(Result{
		Attempted: StageDeploy,
		Completed: StageRender,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect"}))
	g.Expect(stub.cluster.applies).To(BeEmpty())
}

// TestRunCancellationBeforeApply proves the invocation checks the
// context after the owner preparation and before the payload write:
// the confirmed owner survives, the apply never runs, and apply
// completion stays false.
func TestRunCancellationBeforeApply(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())

	ctx, cancel := context.WithCancel(t.Context())
	stub.cluster.applyHook = func(_ context.Context, id string) {
		// Cancel once the definition apply — the preparation's last
		// write — has landed, ahead of the payload apply.
		if strings.Contains(id, prepareDefinitionWrite) {
			cancel()
		}
	}

	result, err := stub.runner.Run(ctx)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

	g.Expect(result).To(SatisfyAll(
		HaveField("Attempted", StageDeploy),
		HaveField("Completed", StageRender),
		HaveField("Owner", Not(BeNil())),
		HaveField("ApplyCompleted", BeFalse()),
	))
	g.Expect(result.Owner.Name).To(Equal("synthetic"))
	g.Expect(string(result.Owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect"}))
	g.Expect(stub.appliedOwner).To(BeNil())
}

// TestRunPreparationFailureReportsNilOwner proves a preparation
// failure reports no owner — which never claims that no write
// occurred — with the payload apply never running and the raw cause
// preserved under the safe public text.
func TestRunPreparationFailureReportsNilOwner(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cause := &markerError{marker: "RUN-MARKER-preparation"}

	stub := newStubRunner(t, testOptions())
	stub.cluster.failApply[prepareDefinitionWrite] = cause

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(HaveOccurred())

	g.Expect(result).To(Equal(Result{
		Attempted: StageDeploy,
		Completed: StageRender,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect"}))
	g.Expect(stub.appliedOwner).To(BeNil())

	// The safe text names the failed operation; the raw cause stays
	// reachable through Unwrap.
	g.Expect(err.Error()).To(SatisfyAll(
		ContainSubstring("applying the module definition"),
		Not(ContainSubstring("RUN-MARKER-preparation")),
	))
	g.Expect(errors.As(err, &cause)).To(BeTrue())
}

// TestRunApplyFailureReports proves an apply failure keeps the
// server-confirmed owner while reporting apply completion as false.
func TestRunApplyFailureReports(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())
	stub.applyError = errors.New("apply failure")

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(MatchError(stub.applyError))

	g.Expect(result).To(SatisfyAll(
		HaveField("Attempted", StageDeploy),
		HaveField("Completed", StageRender),
		HaveField("Owner", Not(BeNil())),
		HaveField("ApplyCompleted", BeFalse()),
	))
	g.Expect(result.Owner.Name).To(Equal("synthetic"))
	g.Expect(string(result.Owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "apply"}))
}
