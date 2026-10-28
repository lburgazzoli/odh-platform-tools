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
	"testing"
	"time"

	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// stageOverheadSlack bounds the time the real local stages and the
// deploy's remote work take between the invocation's start and the
// wait budget's start — including the owner preparation's two
// fake-client applies of the large embedded CRD and the whole payload
// apply; real boundaries are proven against budget magnitudes far
// larger than this slack.
const stageOverheadSlack = 2 * time.Second

// waitFallbackDefinitionYAML is the definition with no configured
// controller deployment name: the wait target falls back to the
// module's metadata.name.
const waitFallbackDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: synthetic
spec:
  instance:
    type:
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: SyntheticModule
  manifests:
    format: helm
    helm:
      path: ./manifests/chart
  controller:
    deployment:
      container:
        image: quay.io/example/controller:v1
`

// waitFallbackNameTemplateYAML renders the controller Deployment
// under the module's metadata.name.
const waitFallbackNameTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic
  namespace: "{{ .Release.Namespace }}"
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      containers:
      - name: manager
        image: quay.io/example/chart-default:v1
`

// waitOtherNameTemplateYAML renders a Deployment no wait target
// matches.
const waitOtherNameTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: other-controller
  namespace: "{{ .Release.Namespace }}"
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      containers:
      - name: manager
        image: quay.io/example/chart-default:v1
`

// waitMultiTargetTemplateYAML renders the controller Deployment
// twice in one namespace — the wait deduplicates by namespace and
// name — once in another namespace, one unrelated Deployment, and a
// same-named ConfigMap: exactly the target-selection corpus. The
// duplicate document means the payload apply's pre-apply lookup of
// the second copy finds the first applied object and consumes one
// scripted frame, the fixture's documented rerun caveat.
const waitMultiTargetTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  namespace: ns-a
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      containers:
      - name: manager
        image: quay.io/example/chart-default:v1
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  namespace: ns-a
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      containers:
      - name: manager
        image: quay.io/example/chart-default:v1
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
  namespace: ns-b
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      containers:
      - name: manager
        image: quay.io/example/chart-default:v1
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: other-controller
  namespace: ns-a
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: synthetic
    spec:
      containers:
      - name: manager
        image: quay.io/example/chart-default:v1
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: synthetic-controller
  namespace: run-namespace
`

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

// waitOptions returns valid invocation settings with the wait
// requested, an explicit budget, and an instant poll interval.
func waitOptions() Options {
	options := testOptions()
	options.Wait = true
	options.WaitTimeout = 5 * time.Second
	options.WaitPollInterval = time.Millisecond

	return options
}

// newWaitDeployer wires the real-operation harness for a wait: the
// given definition and chart template variants render the payload,
// and polling goes through the fixture client.
func newWaitDeployer(
	t *testing.T,
	options Options,
	fixture *prepareCluster,
	definition string,
	template string,
) *Deployer {
	t.Helper()

	return newTestDeployer(t, options, fixture, composeRoot(t, definition, template))
}

// deployed drives the target through its stages up to a successful
// Deploy, so Wait can run.
func deployed(t *testing.T, target *Deployer) {
	t.Helper()

	if err := target.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := target.Render(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := target.Deploy(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestWaitRequiresSuccessfulDeploy proves Wait rejects a Deployer that
// has not successfully deployed — before any stage, after local
// stages only, and after a failed stage — with the sequence sentinel
// and no observation.
func TestWaitRequiresSuccessfulDeploy(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	freshFixture := newPrepareCluster(t)
	fresh := newWaitDeployer(t, waitOptions(), freshFixture,
		literalControllerDefinitionYAML, composeTemplateYAML)
	g.Expect(errors.Is(fresh.Wait(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())

	local := newWaitDeployer(t, waitOptions(), newPrepareCluster(t),
		literalControllerDefinitionYAML, composeTemplateYAML)
	g.Expect(local.Load(t.Context())).To(Succeed())
	g.Expect(local.Render(t.Context())).To(Succeed())
	g.Expect(errors.Is(local.Wait(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())

	failedFixture := newPrepareCluster(t)
	failed := newWaitDeployer(t, waitOptions(), failedFixture,
		literalControllerDefinitionYAML, brokenTemplateYAML)
	g.Expect(failed.Load(t.Context())).To(Succeed())
	g.Expect(failed.Render(t.Context())).To(HaveOccurred())
	g.Expect(errors.Is(failed.Wait(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())

	g.Expect(freshFixture.gets).To(BeEmpty())
}

// TestWaitRunsOnce proves a successful wait accepts no second wait.
func TestWaitRunsOnce(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{live: waitLive(t, waitDeployment(nil))},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)
	deployed(t, target)

	g.Expect(target.Wait(t.Context())).To(Succeed())
	g.Expect(errors.Is(target.Wait(t.Context()), moduleerrors.ErrStageSequence)).To(BeTrue())
}

// TestApplyDefaultMakesNoWaitGet proves the default invocation performs no
// workload observation of any kind and its report makes no wait
// claim, even when a wait would have completed.
func TestApplyDefaultMakesNoWaitGet(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{live: waitLive(t, waitDeployment(nil))},
	}

	target := newWaitDeployer(t, testOptions(), fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)

	result, err := target.Apply(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(fixture.gets).To(BeEmpty())
	g.Expect(result).To(SatisfyAll(
		HaveField("WaitRequested", BeFalse()),
		HaveField("WaitCompleted", BeFalse()),
		HaveField("WaitTargets", BeEmpty()),
	))
}

// TestApplyRequestedWaitReportsCompletion proves a requested wait
// completes over the live observation and the report carries the
// safe target identity beside the retained deploy facts.
func TestApplyRequestedWaitReportsCompletion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{live: waitLive(t, waitDeployment(nil))},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)

	result, err := target.Apply(t.Context())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(result).To(SatisfyAll(
		HaveField("Attempted", StageDeploy),
		HaveField("Completed", StageDeploy),
		HaveField("Owner", Not(BeNil())),
		HaveField("ApplyCompleted", BeTrue()),
		HaveField("WaitRequested", BeTrue()),
		HaveField("WaitCompleted", BeTrue()),
		HaveField("WaitTargets", Equal([]types.NamespacedName{{Namespace: "run-namespace", Name: "synthetic-controller"}})),
	))
	g.Expect(result.Owner.Name).To(Equal("synthetic"))
	g.Expect(string(result.Owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))

	g.Expect(fixture.gets).To(ContainElement(waitTargetIdentity("run-namespace", "synthetic-controller")))
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

	// The duplicate ns-a document's pre-apply lookup consumes the
	// first ns-a frame, so the scripted sequence carries one extra
	// rolled-out observation.
	fixture.frames[waitTargetIdentity("ns-a", "synthetic-controller")] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Namespace = "ns-a"
		}))},
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Namespace = "ns-a"
		}))},
	}
	fixture.frames[waitTargetIdentity("ns-b", "synthetic-controller")] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Namespace = "ns-b"
		}))},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, waitMultiTargetTemplateYAML)
	deployed(t, target)

	g.Expect(target.Wait(t.Context())).To(Succeed())

	g.Expect(target.waitTargets).To(Equal([]types.NamespacedName{
		{Namespace: "ns-a", Name: "synthetic-controller"},
		{Namespace: "ns-b", Name: "synthetic-controller"},
	}))

	for _, get := range fixture.gets {
		g.Expect(get).To(SatisfyAny(
			Equal(waitTargetIdentity("ns-a", "synthetic-controller")),
			Equal(waitTargetIdentity("ns-b", "synthetic-controller")),
		))
	}

	g.Expect(fixture.gets).To(ContainElement(waitTargetIdentity("ns-a", "synthetic-controller")))
	g.Expect(fixture.gets).To(ContainElement(waitTargetIdentity("ns-b", "synthetic-controller")))
	g.Expect(fixture.gets).NotTo(ContainElement(waitTargetIdentity("ns-a", "other-controller")))
}

// TestWaitConfiguredNameFallback proves an unconfigured controller
// deployment name falls back to the module's metadata.name, matching
// the env injector's target selection.
func TestWaitConfiguredNameFallback(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic")] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Name = "synthetic"
		}))},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		waitFallbackDefinitionYAML, waitFallbackNameTemplateYAML)
	deployed(t, target)

	g.Expect(target.Wait(t.Context())).To(Succeed())
	g.Expect(target.waitTargets).To(Equal([]types.NamespacedName{
		{Namespace: "run-namespace", Name: "synthetic"},
	}))
}

// A missing controller fails rendering even when rollout waiting is
// requested, before publishing the owner or applying any resources.
func TestMissingTargetFailsBeforeApply(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, waitOtherNameTemplateYAML)

	result, err := target.Apply(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(
		`no rendered apps/v1 Deployment matched controller "synthetic-controller"`))
	g.Expect(errors.Is(err, moduleerrors.ErrWaitTargetMissing)).To(BeFalse())
	g.Expect(errors.Is(err, moduleerrors.ErrRolloutWait)).To(BeFalse())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDeployOptions)).To(BeFalse())

	g.Expect(result).To(SatisfyAll(
		HaveField("Attempted", StageRender),
		HaveField("Completed", StageLoad),
		HaveField("Owner", BeNil()),
		HaveField("ApplyCompleted", BeFalse()),
		HaveField("WaitRequested", BeTrue()),
		HaveField("WaitCompleted", BeFalse()),
	))
	g.Expect(fixture.gets).To(BeEmpty())
	g.Expect(fixture.applies).To(BeEmpty())
}

// TestWaitInitialNotFoundMayWait proves a target that is absent on
// the first observation keeps waiting and completes once it appears
// rolled out.
func TestWaitInitialNotFoundMayWait(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{err: apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "synthetic-controller")},
		{live: waitLive(t, waitDeployment(nil))},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)
	deployed(t, target)

	g.Expect(target.Wait(t.Context())).To(Succeed())
	g.Expect(fixture.gets).To(HaveLen(2))
}

// TestWaitTerminatingTargetStaysPending proves a target with a
// deletionTimestamp is pending — never done, never a failure — and
// the wait completes once a live object under the same identity rolls
// out.
func TestWaitTerminatingTargetStaysPending(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	now := metav1.Now()

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.DeletionTimestamp = &now
		}))},
		{live: waitLive(t, waitDeployment(nil))},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)
	deployed(t, target)

	g.Expect(target.Wait(t.Context())).To(Succeed())
	g.Expect(target.waitCompleted).To(BeTrue())
	g.Expect(fixture.gets).To(HaveLen(2))
}

// TestWaitFollowsReplacedDeployment proves the wait follows its
// targets by namespace and name only: a replacement object under the
// same identity is waited on like any other, and a rolled-out
// replacement completes the wait.
func TestWaitFollowsReplacedDeployment(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.UID = "deployment-uid-1"
			d.Status.UpdatedReplicas = 0
		}))},
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.UID = "deployment-uid-2"
		}))},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)
	deployed(t, target)

	g.Expect(target.Wait(t.Context())).To(Succeed())
	g.Expect(fixture.gets).To(HaveLen(2))
}

// TestWaitDisappearanceStaysPending proves a target that was live and
// then vanishes stays pending — the wait follows the identity, not
// one object instance — and completes when a rolled-out object
// returns under it.
func TestWaitDisappearanceStaysPending(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		}))},
		{err: apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "synthetic-controller")},
		{live: waitLive(t, waitDeployment(nil))},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)
	deployed(t, target)

	g.Expect(target.Wait(t.Context())).To(Succeed())
	g.Expect(fixture.gets).To(HaveLen(3))
}

// TestWaitProgressDeadlineFails proves the public helper's
// progress-deadline judgement fails the wait.
func TestWaitProgressDeadlineFails(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Status.Conditions = []appsv1.DeploymentCondition{{
				Type:   appsv1.DeploymentProgressing,
				Status: "False",
				Reason: "ProgressDeadlineExceeded",
			}}
		}))},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)
	deployed(t, target)

	g.Expect(target.Wait(t.Context())).To(HaveOccurred())
	g.Expect(target.waitCompleted).To(BeFalse())
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
			fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = testCase.frames

			target := newWaitDeployer(t, waitOptions(), fixture,
				literalControllerDefinitionYAML, composeTemplateYAML)
			deployed(t, target)

			g.Expect(target.Wait(t.Context())).To(Succeed())
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
		options.WaitPollInterval = time.Millisecond

		fixture := newPrepareCluster(t)
		fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
			{live: waitLive(t, waitDeployment(nil))},
		}

		target := newWaitDeployer(t, options, fixture,
			literalControllerDefinitionYAML, composeTemplateYAML)

		start := time.Now()
		_, err := target.Apply(t.Context())
		g.Expect(err).NotTo(HaveOccurred())

		deadline, ok := fixture.getCtxs[0].Deadline()
		g.Expect(ok).To(BeTrue())
		// The wait budget starts after the apply, so its deadline is
		// the invocation start plus the real stages' overhead plus the
		// budget; the slack bounds that overhead while the explicit
		// 400ms cap — not the five-minute overall timeout — bounds
		// the wait.
		g.Expect(deadline).To(BeTemporally("<=", start.Add(400*time.Millisecond+stageOverheadSlack)))
	})

	t.Run("earlier overall deadline wins", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		options := testOptions()
		// The overall timeout is deliberately well under the 30-second
		// wait budget yet generous for the real local stages, so the
		// deadline the wait observes is the invocation's, not the
		// stages' overhead under load.
		options.Timeout = 2 * time.Second
		options.Wait = true
		options.WaitTimeout = 30 * time.Second
		options.WaitPollInterval = time.Millisecond

		fixture := newPrepareCluster(t)
		fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
			{live: waitLive(t, waitDeployment(nil))},
		}

		target := newWaitDeployer(t, options, fixture,
			literalControllerDefinitionYAML, composeTemplateYAML)

		start := time.Now()
		_, err := target.Apply(t.Context())
		g.Expect(err).NotTo(HaveOccurred())

		deadline, ok := fixture.getCtxs[0].Deadline()
		g.Expect(ok).To(BeTrue())
		// The earlier overall deadline caps the wait: a 30-second
		// budget never extends the two-second invocation deadline
		// (plus the same real-stage slack).
		g.Expect(deadline).To(BeTemporally("<=", start.Add(2*time.Second+stageOverheadSlack)))
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
	options.WaitPollInterval = time.Millisecond

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{live: waitLive(t, waitDeployment(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		}))},
	}

	target := newWaitDeployer(t, options, fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)

	result, err := target.Apply(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(errors.Is(err, moduleerrors.ErrRolloutWait)).To(BeTrue())
	g.Expect(errors.Is(err, moduleerrors.ErrWaitTargetMissing)).To(BeFalse())

	g.Expect(result).To(SatisfyAll(
		HaveField("Attempted", StageDeploy),
		HaveField("Completed", StageDeploy),
		HaveField("Owner", Not(BeNil())),
		HaveField("ApplyCompleted", BeTrue()),
		HaveField("WaitRequested", BeTrue()),
		HaveField("WaitCompleted", BeFalse()),
		HaveField("WaitTargets", Equal([]types.NamespacedName{{Namespace: "run-namespace", Name: "synthetic-controller"}})),
	))
	g.Expect(result.Owner.Name).To(Equal("synthetic"))
	g.Expect(string(result.Owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))

	// The shared budget was spent polling the one target until it
	// expired — never reset, never abandoned.
	g.Expect(len(fixture.gets)).To(BeNumerically(">=", 2))
	g.Expect(fixture.gets).To(ContainElement(waitTargetIdentity("run-namespace", "synthetic-controller")))
}

// TestWaitAPIErrorFails proves an API failure on observation fails
// the wait with its cause preserved and the deploy facts retained.
func TestWaitAPIErrorFails(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cause := errors.New("api failure")

	fixture := newPrepareCluster(t)
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{err: apierrors.NewInternalError(cause)},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)
	deployed(t, target)

	err := target.Wait(t.Context())
	g.Expect(err).To(HaveOccurred())
	// The API error's server-side identity is preserved through the
	// wait's wrapping.
	g.Expect(apierrors.IsInternalError(err)).To(BeTrue())
	g.Expect(errors.Is(err, moduleerrors.ErrRolloutWait)).To(BeTrue())
	g.Expect(errors.Is(err, moduleerrors.ErrOwnerPreparation)).To(BeFalse())

	g.Expect(target.applied).To(BeTrue())
	g.Expect(target.owner).NotTo(BeNil())
	g.Expect(target.owner.Name).To(Equal("synthetic"))
	g.Expect(string(target.owner.UID)).To(Equal(prepareServerUID(prepareDefinitionIdentity)))
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
	fixture.frames[waitTargetIdentity("run-namespace", "synthetic-controller")] = []waitFrame{
		{err: apierrors.NewInternalError(errors.New(marker))},
	}

	target := newWaitDeployer(t, waitOptions(), fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)
	deployed(t, target)

	err := target.Wait(t.Context())
	g.Expect(err).To(HaveOccurred())

	g.Expect(err.Error()).NotTo(ContainSubstring(marker))
	g.Expect(err.Error()).To(ContainSubstring("run-namespace/synthetic-controller"))

	var status *apierrors.StatusError
	g.Expect(errors.As(err, &status)).To(BeTrue())
	g.Expect(apierrors.IsInternalError(err)).To(BeTrue())
	g.Expect(status.Error()).To(ContainSubstring(marker))
	g.Expect(errors.Is(err, moduleerrors.ErrRolloutWait)).To(BeTrue())
}

// TestApplyRejectsNegativeWaitTimeout proves a negative wait budget on
// a requested wait is an options error before any stage or client
// call runs.
func TestApplyRejectsNegativeWaitTimeout(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	options := testOptions()
	options.Wait = true
	options.WaitTimeout = -time.Second

	fixture := newPrepareCluster(t)
	target := newWaitDeployer(t, options, fixture,
		literalControllerDefinitionYAML, composeTemplateYAML)

	result, err := target.Apply(t.Context())
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDeployOptions)).To(BeTrue())

	g.Expect(result).To(Equal(Result{}))
	g.Expect(fixture.applies).To(BeEmpty())
}
