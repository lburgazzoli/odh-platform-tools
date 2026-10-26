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

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// waitTargetKey is the wait's single target identity: the test
// definition's controller Deployment in the invocation namespace.
const waitTargetKey = "run-namespace/synthetic-controller"

// stageOverheadSlack bounds the time the stub load/render/deploy
// stages take between the run's start and the wait budget's start —
// including the real owner preparation's two fake-client applies of
// the large embedded CRD; real boundaries are proven against budget
// magnitudes far larger than this slack.
const stageOverheadSlack = 2 * time.Second

// waitDeployment builds one controller Deployment observation. The
// default is rolled out: observed generation, updated, and available
// replicas all match one desired replica.
func waitDeployment(mutate func(*appsv1.Deployment)) *appsv1.Deployment {
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

	return deployment
}

// waitLive converts one typed Deployment into the live unstructured
// map a GET observation returns.
func waitLive(t *testing.T, deployment *appsv1.Deployment) map[string]any {
	t.Helper()

	live, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deployment)
	if err != nil {
		t.Fatal(err)
	}

	return live
}

// renderedDeployment builds one rendered apps/v1 Deployment resource
// for target selection.
func renderedDeployment(namespace, name string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}}
}

// renderedPayloadConfigMap builds one rendered non-Deployment
// resource whose name collides with the controller target.
func renderedPayloadConfigMap(name string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": name, "namespace": "run-namespace"},
	}}
}

// newWaitStub wires the stub harness for a wait: polling goes through
// the intercepted prepareCluster connection, the render payload is
// the given resources, and the poll interval is instant. Deploy's
// owner preparation runs for real over the same fixture.
func newWaitStub(
	t *testing.T,
	options Options,
	fixture *prepareCluster,
	rendered []unstructured.Unstructured,
) *stubRunner {
	t.Helper()

	stub := newStubRunner(t, options)
	stub.cluster = fixture
	stub.renderResult = rendered
	stub.runner.waitPollInterval = time.Millisecond

	return stub
}

// waitOptions returns valid invocation settings with the wait
// requested and an explicit budget.
func waitOptions() Options {
	options := testOptions()
	options.Wait = true
	options.WaitTimeout = 5 * time.Second

	return options
}

// deployed drives the stub through its stages up to a successful
// Deploy, so Wait can run.
func deployed(t *testing.T, stub *stubRunner) {
	t.Helper()

	if err := stub.runner.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := stub.runner.Render(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := stub.runner.Deploy(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestWaitRequiresSuccessfulDeploy proves Wait rejects a Runner that
// has not successfully deployed — before any stage, after local
// stages only, and after a failed stage — with the sequence sentinel
// and no observation.
func TestWaitRequiresSuccessfulDeploy(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	freshFixture := newPrepareCluster(t)
	fresh := newWaitStub(t, waitOptions(), freshFixture, nil)
	g.Expect(errors.Is(fresh.runner.Wait(t.Context()), ErrStageSequence)).To(BeTrue())

	local := newWaitStub(t, waitOptions(), newPrepareCluster(t), nil)
	g.Expect(local.runner.Load(t.Context())).To(Succeed())
	g.Expect(local.runner.Render(t.Context())).To(Succeed())
	g.Expect(errors.Is(local.runner.Wait(t.Context()), ErrStageSequence)).To(BeTrue())

	failed := newWaitStub(t, waitOptions(), newPrepareCluster(t), nil)
	failed.loadError = errors.New("definition failure")
	g.Expect(failed.runner.Load(t.Context())).To(HaveOccurred())
	g.Expect(errors.Is(failed.runner.Wait(t.Context()), ErrStageSequence)).To(BeTrue())

	g.Expect(freshFixture.gets).To(BeEmpty())
}

// TestWaitRunsOnce proves a successful wait accepts no second wait.
func TestWaitRunsOnce(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{live: waitLive(t, waitDeployment(nil))},
	}

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})
	deployed(t, stub)

	g.Expect(stub.runner.Wait(t.Context())).To(Succeed())
	g.Expect(errors.Is(stub.runner.Wait(t.Context()), ErrStageSequence)).To(BeTrue())
}

// TestRunDefaultMakesNoWaitGet proves the default run performs no
// workload observation of any kind and its report makes no wait
// claim, even when a wait would have completed.
func TestRunDefaultMakesNoWaitGet(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{live: waitLive(t, waitDeployment(nil))},
	}

	stub := newWaitStub(t, testOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(fixture.gets).To(BeEmpty())
	g.Expect(result).To(SatisfyAll(
		HaveField("WaitRequested", BeFalse()),
		HaveField("WaitCompleted", BeFalse()),
		HaveField("WaitTargets", BeEmpty()),
	))
}

// TestRunRequestedWaitReportsCompletion proves a requested wait
// completes over the live observation and the report carries the
// safe target identity beside the retained deploy facts.
func TestRunRequestedWaitReportsCompletion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{live: waitLive(t, waitDeployment(nil))},
	}

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(result).To(SatisfyAll(
		HaveField("Attempted", StageDeploy),
		HaveField("Completed", StageDeploy),
		HaveField("Owner", Not(BeNil())),
		HaveField("ApplyCompleted", BeTrue()),
		HaveField("WaitRequested", BeTrue()),
		HaveField("WaitCompleted", BeTrue()),
		HaveField("WaitTargets", Equal([]Target{{Namespace: "run-namespace", Name: "synthetic-controller"}})),
	))
	g.Expect(result.Owner.Name).To(Equal("synthetic"))
	g.Expect(string(result.Owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))

	g.Expect(fixture.gets).To(ContainElement(waitTargetKey))
}

// TestWaitTargetSelectionAndDedup proves the wait selects exactly the
// rendered apps/v1 Deployments matching the configured controller
// name — deduplicated by namespace and name, in their actual
// namespaces — and ignores unrelated Deployments and same-named
// other kinds.
func TestWaitTargetSelectionAndDedup(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames["ns-a/synthetic-controller"] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Namespace = "ns-a"
		}))},
	}
	fixture.frames["ns-b/synthetic-controller"] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Namespace = "ns-b"
		}))},
	}

	rendered := []unstructured.Unstructured{
		renderedDeployment("ns-a", "synthetic-controller"),
		renderedDeployment("ns-a", "synthetic-controller"),
		renderedDeployment("ns-b", "synthetic-controller"),
		renderedDeployment("ns-a", "other-controller"),
		renderedPayloadConfigMap("synthetic-controller"),
	}

	stub := newWaitStub(t, waitOptions(), fixture, rendered)
	deployed(t, stub)

	g.Expect(stub.runner.Wait(t.Context())).To(Succeed())

	g.Expect(stub.runner.waitTargets).To(Equal([]Target{
		{Namespace: "ns-a", Name: "synthetic-controller"},
		{Namespace: "ns-b", Name: "synthetic-controller"},
	}))

	for _, get := range fixture.gets {
		g.Expect(get).To(SatisfyAny(
			Equal("ns-a/synthetic-controller"),
			Equal("ns-b/synthetic-controller"),
		))
	}

	g.Expect(fixture.gets).To(ContainElement("ns-a/synthetic-controller"))
	g.Expect(fixture.gets).To(ContainElement("ns-b/synthetic-controller"))
	g.Expect(fixture.gets).NotTo(ContainElement("ns-a/other-controller"))
}

// TestWaitConfiguredNameFallback proves an unconfigured controller
// deployment name falls back to the module's metadata.name, matching
// the env injector's target selection.
func TestWaitConfiguredNameFallback(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames["run-namespace/synthetic"] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Name = "synthetic"
		}))},
	}

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic"),
	})
	stub.definition.Spec.Controller.Deployment.Name = ""
	deployed(t, stub)

	g.Expect(stub.runner.Wait(t.Context())).To(Succeed())
	g.Expect(stub.runner.waitTargets).To(Equal([]Target{
		{Namespace: "run-namespace", Name: "synthetic"},
	}))
}

// TestWaitMissingTargetFailsAfterApply proves a payload with no
// matching controller Deployment fails the requested wait after the
// successful apply — never during local validation — with every
// deploy-confirmed fact retained.
func TestWaitMissingTargetFailsAfterApply(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "other-controller"),
	})

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, ErrWaitTargetMissing)).To(BeTrue())

	g.Expect(result).To(SatisfyAll(
		HaveField("Attempted", StageDeploy),
		HaveField("Completed", StageDeploy),
		HaveField("Owner", Not(BeNil())),
		HaveField("ApplyCompleted", BeTrue()),
		HaveField("WaitRequested", BeTrue()),
		HaveField("WaitCompleted", BeFalse()),
	))
	g.Expect(result.Owner.Name).To(Equal("synthetic"))
	g.Expect(string(result.Owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))

	g.Expect(fixture.gets).To(BeEmpty())
}

// TestWaitInitialNotFoundMayWait proves a target that is absent on
// the first observation keeps waiting and completes once it appears
// rolled out.
func TestWaitInitialNotFoundMayWait(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{err: apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "synthetic-controller")},
		{live: waitLive(t, waitDeployment(nil))},
	}

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})
	deployed(t, stub)

	g.Expect(stub.runner.Wait(t.Context())).To(Succeed())
	g.Expect(fixture.gets).To(HaveLen(2))
}

// TestWaitRejectsTerminatingFirstGet proves a target with a
// deletionTimestamp fails on the very first observation even when its
// replica counts look complete.
func TestWaitRejectsTerminatingFirstGet(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	now := metav1.Now()

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.DeletionTimestamp = &now
		}))},
	}

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})
	deployed(t, stub)

	err := stub.runner.Wait(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("terminating"))
	g.Expect(stub.runner.waitCompleted).To(BeFalse())
	g.Expect(stub.runner.applied).To(BeTrue())
}

// TestWaitRejectsUIDReplacement proves the first live UID is pinned
// and a replacement object fails the wait.
func TestWaitRejectsUIDReplacement(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.UID = "deployment-uid-1"
			d.Status.UpdatedReplicas = 0
		}))},
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.UID = "deployment-uid-2"
			d.Status.UpdatedReplicas = 0
		}))},
	}

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})
	deployed(t, stub)

	err := stub.runner.Wait(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("replaced"))
	g.Expect(fixture.gets).To(HaveLen(2))
}

// TestWaitRejectsDisappearance proves a target that was live and then
// vanishes fails the wait instead of waiting forever.
func TestWaitRejectsDisappearance(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		}))},
		{err: apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "synthetic-controller")},
	}

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})
	deployed(t, stub)

	err := stub.runner.Wait(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("disappeared"))
}

// TestWaitProgressDeadlineFails proves the public helper's
// progress-deadline judgement fails the wait.
func TestWaitProgressDeadlineFails(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Status.Conditions = []appsv1.DeploymentCondition{{
				Type:   appsv1.DeploymentProgressing,
				Status: "False",
				Reason: "ProgressDeadlineExceeded",
			}}
		}))},
	}

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})
	deployed(t, stub)

	g.Expect(stub.runner.Wait(t.Context())).To(HaveOccurred())
	g.Expect(stub.runner.waitCompleted).To(BeFalse())
}

// TestWaitPollsUntilRolledOut proves the wait keeps observing until
// the public helper reports rollout, across the helper's own
// not-done judgements, and completes immediately for replica counts
// the helper already accepts.
func TestWaitPollsUntilRolledOut(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		frames []waitFrame
		gets   int
	}{
		{
			name: "incomplete update then complete",
			frames: []waitFrame{
				{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
					d.Status.UpdatedReplicas = 0
				}))},
				{live: waitLive(t, waitDeployment(nil))},
			},
			gets: 2,
		},
		{
			name: "old replicas pending then complete",
			frames: []waitFrame{
				{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
					d.Status.Replicas = 2
				}))},
				{live: waitLive(t, waitDeployment(nil))},
			},
			gets: 2,
		},
		{
			name: "unavailable then complete",
			frames: []waitFrame{
				{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
					d.Status.AvailableReplicas = 0
				}))},
				{live: waitLive(t, waitDeployment(nil))},
			},
			gets: 2,
		},
		{
			name: "generation lag then observed",
			frames: []waitFrame{
				{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
					d.Generation = 2
					d.Status.ObservedGeneration = 1
				}))},
				{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
					d.Generation = 2
					d.Status.ObservedGeneration = 2
				}))},
			},
			gets: 2,
		},
		{
			name: "three replicas complete on first observation",
			frames: []waitFrame{
				{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
					replicas := int32(3)
					d.Spec.Replicas = &replicas
					d.Status.Replicas = 3
					d.Status.UpdatedReplicas = 3
					d.Status.AvailableReplicas = 3
				}))},
			},
			gets: 1,
		},
		{
			name: "zero replicas complete on first observation",
			frames: []waitFrame{
				{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
					replicas := int32(0)
					d.Spec.Replicas = &replicas
					d.Status.Replicas = 0
					d.Status.UpdatedReplicas = 0
					d.Status.AvailableReplicas = 0
				}))},
			},
			gets: 1,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			fixture := newPrepareCluster(t)
			fixture.frames[waitTargetKey] = testCase.frames

			stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
				renderedDeployment("run-namespace", "synthetic-controller"),
			})
			deployed(t, stub)

			g.Expect(stub.runner.Wait(t.Context())).To(Succeed())
			g.Expect(fixture.gets).To(HaveLen(testCase.gets))
		})
	}
}

// TestWaitBudgetBoundaries proves one shared budget starts at the
// wait, capped by the explicit override, and never extends the
// remaining overall invocation deadline.
func TestWaitBudgetBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("explicit cap", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		options := testOptions()
		options.Wait = true
		options.WaitTimeout = 400 * time.Millisecond

		fixture := newPrepareCluster(t)
		fixture.frames[waitTargetKey] = []waitFrame{
			{live: waitLive(t, waitDeployment(nil))},
		}

		stub := newWaitStub(t, options, fixture, []unstructured.Unstructured{
			renderedDeployment("run-namespace", "synthetic-controller"),
		})

		start := time.Now()
		_, err := stub.runner.Run(t.Context())
		g.Expect(err).NotTo(HaveOccurred())

		deadline, ok := fixture.getCtxs[0].Deadline()
		g.Expect(ok).To(BeTrue())
		// The wait budget starts after the apply, so its deadline is
		// the run start plus stub-stage overhead plus the budget; the
		// slack bounds that overhead while the explicit 400ms cap —
		// not the five-minute overall timeout — bounds the wait.
		g.Expect(deadline).To(BeTemporally("<=", start.Add(400*time.Millisecond+stageOverheadSlack)))
	})

	t.Run("earlier overall deadline wins", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		options := testOptions()
		options.Timeout = 300 * time.Millisecond
		options.Wait = true
		options.WaitTimeout = 30 * time.Second

		fixture := newPrepareCluster(t)
		fixture.frames[waitTargetKey] = []waitFrame{
			{live: waitLive(t, waitDeployment(nil))},
		}

		stub := newWaitStub(t, options, fixture, []unstructured.Unstructured{
			renderedDeployment("run-namespace", "synthetic-controller"),
		})

		start := time.Now()
		_, err := stub.runner.Run(t.Context())
		g.Expect(err).NotTo(HaveOccurred())

		deadline, ok := fixture.getCtxs[0].Deadline()
		g.Expect(ok).To(BeTrue())
		// The earlier overall deadline caps the wait: a 30-second
		// budget never extends the 300ms invocation deadline (plus
		// the same stub-stage slack).
		g.Expect(deadline).To(BeTemporally("<=", start.Add(300*time.Millisecond+stageOverheadSlack)))
	})
}

// TestWaitTimeoutIsRuntimeError proves an expiring wait budget fails
// as a runtime deadline error while the report retains the confirmed
// owner, apply completion, and the requested wait's non-completion.
func TestWaitTimeoutIsRuntimeError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := testOptions()
	options.Wait = true
	options.WaitTimeout = 40 * time.Millisecond

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		}))},
	}

	stub := newWaitStub(t, options, fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())

	g.Expect(result).To(SatisfyAll(
		HaveField("Attempted", StageDeploy),
		HaveField("Completed", StageDeploy),
		HaveField("Owner", Not(BeNil())),
		HaveField("ApplyCompleted", BeTrue()),
		HaveField("WaitRequested", BeTrue()),
		HaveField("WaitCompleted", BeFalse()),
		HaveField("WaitTargets", Equal([]Target{{Namespace: "run-namespace", Name: "synthetic-controller"}})),
	))
	g.Expect(result.Owner.Name).To(Equal("synthetic"))
	g.Expect(string(result.Owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))

	// The shared budget was spent polling the one target until it
	// expired — never reset, never abandoned.
	g.Expect(len(fixture.gets)).To(BeNumerically(">=", 2))
	g.Expect(fixture.gets).To(ContainElement(waitTargetKey))
}

// TestWaitAPIErrorFails proves an API failure on observation fails
// the wait with its cause preserved and the deploy facts retained.
func TestWaitAPIErrorFails(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cause := errors.New("api failure")

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{err: apierrors.NewInternalError(cause)},
	}

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})
	deployed(t, stub)

	err := stub.runner.Wait(t.Context())
	g.Expect(err).To(HaveOccurred())
	// The API error's server-side identity is preserved through the
	// wait's wrapping.
	g.Expect(apierrors.IsInternalError(err)).To(BeTrue())

	g.Expect(stub.runner.applied).To(BeTrue())
	g.Expect(stub.runner.owner).NotTo(BeNil())
	g.Expect(stub.runner.owner.Name).To(Equal("synthetic"))
	g.Expect(string(stub.runner.owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))
}

// TestWaitErrorIsSafeText proves the wait failure never echoes
// API-returned text into its public message: a distinctive marker the
// server error carries appears nowhere in err.Error(), while the raw
// cause stays reachable through Unwrap for errors.Is and errors.As.
func TestWaitErrorIsSafeText(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	const marker = "WAIT-MARKER-8f3d91"

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetKey] = []waitFrame{
		{err: apierrors.NewInternalError(errors.New(marker))},
	}

	stub := newWaitStub(t, waitOptions(), fixture, []unstructured.Unstructured{
		renderedDeployment("run-namespace", "synthetic-controller"),
	})
	deployed(t, stub)

	err := stub.runner.Wait(t.Context())
	g.Expect(err).To(HaveOccurred())

	g.Expect(err.Error()).NotTo(ContainSubstring(marker))
	g.Expect(err.Error()).To(ContainSubstring("run-namespace/synthetic-controller"))
	g.Expect(err.Error()).To(ContainSubstring("errors.Is"))

	var status *apierrors.StatusError
	g.Expect(errors.As(err, &status)).To(BeTrue())
	g.Expect(apierrors.IsInternalError(err)).To(BeTrue())
	g.Expect(status.Error()).To(ContainSubstring(marker))
}

// TestRunRejectsNegativeWaitTimeout proves a negative wait budget on
// a requested wait is an options error before any stage or service
// runs.
func TestRunRejectsNegativeWaitTimeout(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := testOptions()
	options.Wait = true
	options.WaitTimeout = -time.Second

	stub := newStubRunner(t, options)

	result, err := stub.runner.Run(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, ErrInvalidOptions)).To(BeTrue())

	g.Expect(result).To(Equal(Result{}))
	g.Expect(*stub.calls).To(BeEmpty())
}
