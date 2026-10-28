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

package deployer

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// TestApplyAppliesOnlyOrchestration proves Apply sequences Load →
// Render → Deploy over one bounded invocation context derived from
// the caller's context and the options' timeout, and its Result
// reports exactly the apply-only facts: the server-confirmed owner
// and apply completion — with no readiness claim.
func TestApplyAppliesOnlyOrchestration(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, testOptions(), fixture, root)

	result, err := target.Apply(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	// The whole remote flow ran: preparation writes, then payload
	// writes.
	g.Expect(fixture.applies).To(HaveLen(4))

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

	// Every write ran over the one bounded invocation context: it
	// carries a deadline no later than the options' timeout — the
	// timer is created inside Apply, so the sound bound is measured
	// after it returned — and every apply saw the same deadline, the
	// one shared invocation timer.
	deadline, ok := fixture.applyCtxs[0].Deadline()
	g.Expect(ok).To(BeTrue())
	g.Expect(deadline).To(BeTemporally("<=", time.Now().Add(testOptions().Timeout)))
	for _, ctx := range fixture.applyCtxs {
		ctxDeadline, ctxOK := ctx.Deadline()
		g.Expect(ctxOK).To(BeTrue())
		g.Expect(ctxDeadline.Equal(deadline)).To(BeTrue())
	}
}

// TestApplyRejectsInvalidOptionsBeforeTimer proves a nonpositive
// timeout is an options error, not an artificial deadline error, and
// no stage or client call runs.
func TestApplyRejectsInvalidOptionsBeforeTimer(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := testOptions()
	options.Timeout = 0

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, options, fixture, root)

	result, err := target.Apply(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(Equal("invalid deploy options: Timeout must be positive"))
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDeployOptions)).To(BeTrue())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeFalse())

	g.Expect(result).To(Equal(Result{}))
	g.Expect(fixture.applies).To(BeEmpty())
}

// TestApplyRejectsInvalidFieldManagerBeforeTimer proves an invalid
// field manager fails the complete invocation as an options error
// before the invocation timer or any client call runs.
func TestApplyRejectsInvalidFieldManagerBeforeTimer(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := testOptions()
	options.FieldManager = "team/manager"

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, options, fixture, root)

	result, err := target.Apply(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(Equal(
		"invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters"))
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDeployOptions)).To(BeTrue())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeFalse())

	g.Expect(result).To(Equal(Result{}))
	g.Expect(fixture.applies).To(BeEmpty())
}

// TestApplyAcceptsBoundaryFieldManagers proves the boundary cases the
// strict label-value contract allows: the dotted default, one
// character, the full 63 characters, and internal dots, underscores,
// hyphens, and uppercase.
func TestApplyAcceptsBoundaryFieldManagers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		manager string
	}{
		{"dotted platform default", "platform.opendatahub.io"},
		{"one character", "a"},
		{"sixty-three characters", strings.Repeat("a", 63)},
		{"uppercase and underscore", "Team_Example.Deployer-1"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			options := testOptions()
			options.FieldManager = testCase.manager

			fixture := newPrepareCluster(t)
			root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

			target := newTestDeployer(t, options, fixture, root)

			g.Expect(target.Load(t.Context())).To(Succeed())
			g.Expect(target.options.FieldManager).To(Equal(testCase.manager))
		})
	}
}

// TestApplyHonorsCallerCancellation proves an already-cancelled caller
// fails with its own cancellation identity before the invocation
// timer exists and no stage or client call runs.
func TestApplyHonorsCallerCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, testOptions(), fixture, root)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := target.Apply(ctx)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

	g.Expect(result).To(Equal(Result{}))
	g.Expect(fixture.applies).To(BeEmpty())
	g.Expect(fixture.gets).To(BeEmpty())
}

// TestApplyRequiresFreshDeployer proves Apply rejects a Deployer whose
// stages were already driven, directly or by a previous Apply.
func TestApplyRequiresFreshDeployer(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	driven := newTestDeployer(t, testOptions(), fixture, root)
	g.Expect(driven.Load(t.Context())).To(Succeed())

	result, err := driven.Apply(t.Context())
	g.Expect(errors.Is(err, moduleerrors.ErrStageSequence)).To(BeTrue())
	g.Expect(result).To(Equal(Result{}))
	g.Expect(fixture.applies).To(BeEmpty())

	fresh := newTestDeployer(t, testOptions(), newPrepareCluster(t), root)
	_, err = fresh.Apply(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	_, err = fresh.Apply(t.Context())
	g.Expect(errors.Is(err, moduleerrors.ErrStageSequence)).To(BeTrue())
}

// TestApplyLoadFailureReports proves a real Load failure — a
// definition file the loader cannot read — reports the attempted
// stage, no completed stage, and nothing confirmed.
func TestApplyLoadFailureReports(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	options := testOptions()
	options.DefinitionFile = filepath.Join(root, "missing.yaml")

	target := New(options, WithClient(fixture.client))

	result, err := target.Apply(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())

	g.Expect(result).To(Equal(Result{
		Attempted: StageLoad,
	}))
	g.Expect(fixture.applies).To(BeEmpty())
}

// TestApplyRenderFailureReports proves a real Render failure — a
// chart template the backend cannot parse — reports Load as the last
// successful stage and never reaches a client call.
func TestApplyRenderFailureReports(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, brokenTemplateYAML)

	target := newTestDeployer(t, testOptions(), fixture, root)

	result, err := target.Apply(t.Context())
	g.Expect(err).To(HaveOccurred())

	g.Expect(result).To(Equal(Result{
		Attempted: StageRender,
		Completed: StageLoad,
	}))
	g.Expect(fixture.applies).To(BeEmpty())
	g.Expect(fixture.gets).To(BeEmpty())
}

// TestApplyCancellationAtDeployEntry proves the Deploy stage checks
// the invocation context before any remote work: a caller cancelled
// between Render and Deploy stops the deploy with nothing confirmed
// and no write.
func TestApplyCancellationAtDeployEntry(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, testOptions(), fixture, root)

	g.Expect(target.Load(t.Context())).To(Succeed())
	g.Expect(target.Render(t.Context())).To(Succeed())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := target.Deploy(ctx)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

	g.Expect(fixture.applies).To(BeEmpty())
	g.Expect(target.owner).To(BeNil())
	g.Expect(target.applied).To(BeFalse())
}

// TestApplyCancellationBeforePayload proves the invocation checks the
// context after the owner preparation and before the payload write:
// the confirmed owner survives, the apply never runs, and apply
// completion stays false.
func TestApplyCancellationBeforePayload(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)

	target := newTestDeployer(t, testOptions(), fixture, root)

	ctx, cancel := context.WithCancel(t.Context())
	fixture.applyHook = func(_ context.Context, id string) {
		// Cancel once the definition apply — the preparation's last
		// write — has landed, ahead of the payload apply.
		if strings.Contains(id, prepareDefinitionWrite) {
			cancel()
		}
	}

	result, err := target.Apply(ctx)
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

	// The preparation wrote, the payload never did.
	crdApplies, _ := fixture.applyIndex(prepareCRDWrite)
	definitionApplies, _ := fixture.applyIndex(prepareDefinitionWrite)
	payloadApplies, _ := fixture.applyIndex(preparePayloadDeploymentWrite)
	g.Expect(crdApplies).To(Equal(1))
	g.Expect(definitionApplies).To(Equal(1))
	g.Expect(payloadApplies).To(Equal(0))
}

// TestApplyPreparationFailureReportsNilOwner proves a preparation
// failure reports no owner — which never claims that no write
// occurred — with the payload apply never running and the raw cause
// preserved under the safe public text.
func TestApplyPreparationFailureReportsNilOwner(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cause := &markerError{marker: "RUN-MARKER-preparation"}

	fixture := newPrepareCluster(t)
	fixture.failApply[prepareDefinitionWrite] = cause

	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)
	target := newTestDeployer(t, testOptions(), fixture, root)

	result, err := target.Apply(t.Context())
	g.Expect(err).To(HaveOccurred())

	g.Expect(result).To(Equal(Result{
		Attempted: StageDeploy,
		Completed: StageRender,
	}))

	payloadApplies, _ := fixture.applyIndex(preparePayloadDeploymentWrite)
	g.Expect(payloadApplies).To(Equal(0))

	// The safe text names the failed operation; the raw cause stays
	// reachable through Unwrap.
	g.Expect(err.Error()).To(SatisfyAll(
		ContainSubstring("applying the module definition"),
		Not(ContainSubstring("RUN-MARKER-preparation")),
	))
	g.Expect(errors.As(err, &cause)).To(BeTrue())
}

// TestApplyApplyFailureReports proves a payload apply failure keeps
// the server-confirmed owner while reporting apply completion as
// false, with the deployment write refused and reported as partial
// state.
func TestApplyApplyFailureReports(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.failApply[preparePayloadDeploymentWrite] = errors.New("apply failure")

	root := composeRoot(t, literalControllerDefinitionYAML, composeTemplateYAML)
	target := newTestDeployer(t, testOptions(), fixture, root)
	// The schedule is shortened to a single attempt so the test
	// observes exactly the refused write as the framework's report;
	// the retry behavior has its own tests.
	target.payloadRetryBackoff = []time.Duration{}

	result, err := target.Apply(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrDeployApply)).To(BeTrue())

	g.Expect(result).To(SatisfyAll(
		HaveField("Attempted", StageDeploy),
		HaveField("Completed", StageRender),
		HaveField("Owner", Not(BeNil())),
		HaveField("ApplyCompleted", BeFalse()),
	))
	g.Expect(result.Owner.Name).To(Equal("synthetic"))
	g.Expect(string(result.Owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))

	// The preparation wrote and the payload was attempted once: the
	// refused deployment apply stands as the framework's report.
	crdApplies, _ := fixture.applyIndex(prepareCRDWrite)
	definitionApplies, _ := fixture.applyIndex(prepareDefinitionWrite)
	payloadApplies, payloadFirst := fixture.applyIndex(preparePayloadDeploymentWrite)
	g.Expect(crdApplies).To(Equal(1))
	g.Expect(definitionApplies).To(Equal(1))
	g.Expect(payloadApplies).To(Equal(1))
	g.Expect(payloadFirst).To(BeNumerically(">", 0))
}
