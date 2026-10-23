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
	"testing"
	"time"

	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	moduleowner "github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/owner"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// confirmedOwner returns a server-backed owner identity fixture.
func confirmedOwner() *v1alpha1.PlatformModuleDefinition {
	return &v1alpha1.PlatformModuleDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic", UID: "definition-uid"},
	}
}

// TestRunAppliesOnlyOrchestration proves Run sequences Load → Render
// → Deploy over one invocation context derived from the caller's
// context and the options' timeout, and its Result reports exactly
// the apply-only facts: the confirmed owner, the confirmed
// publication, and apply completion — with no readiness claim.
func TestRunAppliesOnlyOrchestration(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	confirmed := confirmedOwner()

	stub := newStubRunner(t, testOptions())
	stub.ensureOwner = confirmed

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "ensure", "apply"}))

	g.Expect(result).To(Equal(Result{
		Attempted:      StageDeploy,
		Completed:      StageDeploy,
		Owner:          confirmed,
		Publication:    moduleowner.OutcomeConfirmed,
		ApplyCompleted: true,
	}))

	// The invocation context the seams saw is the bounded one: it
	// carries a deadline no later than the options' timeout from the
	// run's start.
	deadline, ok := stub.renderCtx.Deadline()
	g.Expect(ok).To(BeTrue())
	g.Expect(deadline).To(BeTemporally("<=", time.Now().Add(testOptions().Timeout)))
	g.Expect(stub.connectCtx).To(Equal(stub.renderCtx))
	g.Expect(stub.ensureCtx).To(Equal(stub.renderCtx))
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
	g.Expect(errors.Is(err, ErrInvalidOptions)).To(BeTrue())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeFalse())

	g.Expect(result).To(Equal(Result{
		Publication: moduleowner.OutcomeNotAttempted,
	}))
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

	g.Expect(result).To(Equal(Result{
		Publication: moduleowner.OutcomeNotAttempted,
	}))
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
	g.Expect(errors.Is(err, ErrStageSequence)).To(BeTrue())
	g.Expect(result.Publication).To(Equal(moduleowner.OutcomeNotAttempted))
	g.Expect(*stub.calls).To(Equal([]string{"load"}))

	fresh := newStubRunner(t, testOptions())
	_, err = fresh.runner.Run(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	_, err = fresh.runner.Run(t.Context())
	g.Expect(errors.Is(err, ErrStageSequence)).To(BeTrue())
	g.Expect(*fresh.calls).To(Equal([]string{"load", "render", "connect", "ensure", "apply"}))
}

// TestRunLoadFailureReports proves a Load failure reports the
// attempted stage, no completed stage, and nothing published.
func TestRunLoadFailureReports(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())
	stub.loadError = errors.New("definition failure")

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(MatchError(stub.loadError))

	g.Expect(result).To(Equal(Result{
		Attempted:   StageLoad,
		Publication: moduleowner.OutcomeNotAttempted,
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
		Attempted:   StageRender,
		Completed:   StageLoad,
		Publication: moduleowner.OutcomeNotAttempted,
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
		Attempted:   StageRender,
		Completed:   StageLoad,
		Publication: moduleowner.OutcomeNotAttempted,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render"}))
	g.Expect(stub.runner.resources).To(BeNil())

	// The failed invocation is terminal: Deploy never runs.
	g.Expect(errors.Is(stub.runner.Deploy(t.Context()), ErrStageSequence)).To(BeTrue())
	g.Expect(*stub.calls).To(Equal([]string{"load", "render"}))
}

// TestRunCancellationBeforePublication proves the invocation checks
// the context after connecting and before any publication or write:
// cancelling there stops the run with nothing published and no
// payload write.
func TestRunCancellationBeforePublication(t *testing.T) {
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
		Attempted:   StageDeploy,
		Completed:   StageRender,
		Publication: moduleowner.OutcomeNotAttempted,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect"}))
}

// TestRunCancellationBeforeApply proves the invocation checks the
// context after publication and before the payload write: the
// confirmed owner and publication survive, the apply never runs, and
// apply completion stays false.
func TestRunCancellationBeforeApply(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	confirmed := confirmedOwner()

	stub := newStubRunner(t, testOptions())
	stub.ensureOwner = confirmed

	ctx, cancel := context.WithCancel(t.Context())
	stub.ensureHook = func(context.Context) {
		cancel()
	}

	result, err := stub.runner.Run(ctx)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

	g.Expect(result).To(Equal(Result{
		Attempted:   StageDeploy,
		Completed:   StageRender,
		Owner:       confirmed,
		Publication: moduleowner.OutcomeConfirmed,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "ensure"}))
	g.Expect(stub.appliedOwner).To(BeNil())
}

// TestRunEnsureFailureRetainsUncertainOwner proves a lost
// publication response is not reported as if no write occurred: the
// still-confirmed identity and the unknown outcome survive in the
// Result, and the payload apply never runs.
func TestRunEnsureFailureRetainsUncertainOwner(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	confirmed := confirmedOwner()

	stub := newStubRunner(t, testOptions())
	stub.ensureError = &moduleowner.Error{
		Outcome: moduleowner.OutcomeUnknown,
		Owner:   confirmed,
	}

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(HaveOccurred())

	g.Expect(result).To(Equal(Result{
		Attempted:   StageDeploy,
		Completed:   StageRender,
		Owner:       confirmed,
		Publication: moduleowner.OutcomeUnknown,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "ensure"}))
	g.Expect(stub.appliedOwner).To(BeNil())
}

// TestRunEnsureRejectionReportsNilOwner proves a definitive server
// refusal publishes nothing and confirms no identity.
func TestRunEnsureRejectionReportsNilOwner(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stub := newStubRunner(t, testOptions())
	stub.ensureError = &moduleowner.Error{Outcome: moduleowner.OutcomeRejected}

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(HaveOccurred())

	g.Expect(result).To(Equal(Result{
		Attempted:   StageDeploy,
		Completed:   StageRender,
		Publication: moduleowner.OutcomeRejected,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "ensure"}))
}

// TestRunApplyFailureReports proves an apply failure keeps the
// confirmed owner and publication while reporting apply completion
// as false.
func TestRunApplyFailureReports(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	confirmed := confirmedOwner()

	stub := newStubRunner(t, testOptions())
	stub.ensureOwner = confirmed
	stub.applyError = errors.New("apply failure")

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(MatchError(stub.applyError))

	g.Expect(result).To(Equal(Result{
		Attempted:   StageDeploy,
		Completed:   StageRender,
		Owner:       confirmed,
		Publication: moduleowner.OutcomeConfirmed,
	}))
	g.Expect(*stub.calls).To(Equal([]string{"load", "render", "connect", "ensure", "apply"}))
}
