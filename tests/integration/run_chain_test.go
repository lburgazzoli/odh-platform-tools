//go:build integration

package integration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/images"
)

// runDefinitionWrite is the persisted definition's write identity
// substring.
const runDefinitionWrite = "Kind=PlatformModuleDefinition synthetic"

// runControllerWrite is the controller Deployment's write identity
// substring.
const runControllerWrite = "run-namespace/synthetic-controller"

// runControllerObservation is the scripted wait observation identity
// of the controller Deployment.
const runControllerObservation = "apps/v1, Kind=Deployment run-namespace/synthetic-controller"

// TestRunChainHelmComposesStages runs the complete Helm chain —
// loader, Viper file images, the shared Helm engine with the library
// apply-order post-renderer ahead of both struct injectors, the
// runner-internal owner preparation, and the real framework deploy
// action — and proves the cross-service contract: the literal
// controller image is shared by main and init containers, the env
// injection lands on the main container alone, the platform ConfigMap
// participates in the framework's enriched sort, authored values reach
// templates, and the write order is the pre-migration apply order.
func TestRunChainHelmComposesStages(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunHelmModule(t, root)

	harness := newRunHarness(t)

	options := runOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

	result, err := harness.run(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	// The staged report carries the confirmed owner with its
	// simulated server identity.
	g.Expect(result.Completed).To(Equal(runner.StageDeploy))
	g.Expect(result.ApplyCompleted).To(BeTrue())
	g.Expect(result.Owner).NotTo(BeNil())
	g.Expect(result.Owner.Name).To(Equal("synthetic"))
	g.Expect(result.Owner.UID).NotTo(BeEmpty())
	g.Expect(result.WaitRequested).To(BeFalse())

	// The controller Deployment: the literal image is shared by the
	// main and init containers, and both carry the injected
	// environment.
	controller := harness.stored(t, runDeploymentGVK, "run-namespace", "synthetic-controller")
	g.Expect(controller.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "run-namespace"`),
		jq.Match(`.spec.template.spec.containers[0].image == "quay.io/example/controller:v1"`),
		jq.Match(`.spec.template.spec.initContainers[0].image == "quay.io/example/controller:v1"`),
		jq.Match(`.spec.template.spec.containers[0].env // [] | map(.name) == ["RELATED_IMAGE_ALPHA","LOG_LEVEL","APPLICATIONS_NAMESPACE","ODH_MODULE_OPERATOR_PLATFORM_TYPE"]`),
		jq.Match(`.spec.template.spec.containers[0].env[0].value == "quay.io/example/alpha:v3"`),
		jq.Match(`.spec.template.spec.containers[0].env[1].value == "debug"`),
		jq.Match(`.spec.template.spec.containers[0].env[2].value == "run-namespace"`),
		jq.Match(`.spec.template.spec.containers[0].env[3].value == "OpenDataHub"`),
		jq.Match(`.spec.template.spec.initContainers[0].env // [] | map(.name) == ["RELATED_IMAGE_ALPHA","LOG_LEVEL","APPLICATIONS_NAMESPACE","ODH_MODULE_OPERATOR_PLATFORM_TYPE"]`),
		jq.Match(`.metadata.ownerReferences[0].name == "synthetic"`),
	))

	// The unrelated Deployment is applied but never injected.
	unrelated := harness.stored(t, runDeploymentGVK, "run-namespace", "unrelated-controller")
	g.Expect(unrelated.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[0].image == "quay.io/chart/unrelated:v1"`),
		jq.Match(`.spec.template.spec.containers[0].env == null`),
	))

	// The injected platform ConfigMap participates in the framework's
	// enriched sort and is applied beside the payload.
	platform := harness.stored(t, runConfigMapGVK, "run-namespace", "odh-synthetic-config")
	g.Expect(platform.Object).To(SatisfyAll(
		jq.Match(`.data.platformVersion == "2.19.0"`),
		jq.Match(`.metadata.ownerReferences[0].name == "synthetic"`),
	))

	// Authored chart values survive composition.
	values := harness.stored(t, runConfigMapGVK, "run-namespace", "values-cm")
	g.Expect(values.Object).To(jq.Match(`.data.message == "authored-value"`))

	// The gate-labeled ConfigMap is an ordinary applied resource.
	gate := harness.stored(t, runConfigMapGVK, "run-namespace", "gate-cm")
	g.Expect(gate.Object).To(SatisfyAll(
		jq.Match(`.data.gate == "paused"`),
		jq.Match(`.metadata.labels["app.opendatahub.io/upgrade-gate"] == "true"`),
	))

	// Both same-named ConfigMaps land in their distinct namespaces.
	g.Expect(harness.stored(t, runConfigMapGVK, "alpha", "shared").Object).
		To(jq.Match(`.metadata.namespace == "alpha"`))
	g.Expect(harness.stored(t, runConfigMapGVK, "beta", "shared").Object).
		To(jq.Match(`.metadata.namespace == "beta"`))

	// Write order: the definition CRD apply precedes the definition
	// apply, which precedes every payload write; the payload itself
	// is applied in the engine's apply order — the pre-migration
	// winner ordering keeps alpha/shared ahead of beta/shared, and
	// every payload write before the first Deployment write.
	_, crdWrite := harness.writeIndex("Kind=CustomResourceDefinition")
	_, definitionWrite := harness.writeIndex(runDefinitionWrite)
	_, alphaWrite := harness.writeIndex("alpha/shared")
	_, betaWrite := harness.writeIndex("beta/shared")
	_, firstDeploymentWrite := harness.writeIndex("Kind=Deployment")

	g.Expect(crdWrite).To(BeNumerically(">=", 0))
	g.Expect(definitionWrite).To(BeNumerically(">", crdWrite))
	g.Expect(alphaWrite).To(BeNumerically(">", definitionWrite))
	g.Expect(betaWrite).To(BeNumerically(">", alphaWrite))
	g.Expect(firstDeploymentWrite).To(BeNumerically(">", betaWrite))

	// Every write ahead of the first Deployment is a non-Deployment
	// payload write: foundational kinds first, workloads last.
	for i, write := range harness.writes {
		if i < firstDeploymentWrite {
			g.Expect(write).NotTo(ContainSubstring("Kind=Deployment"))
		}
	}
}

// TestRunChainKustomizeComposesStages runs the complete Kustomize
// chain and proves the engine-shared parity: the namespace plugin
// places unset namespaces into the applications namespace, the same
// controller contract is injected, and the excluded core/v1
// Namespace applies without controller references.
func TestRunChainKustomizeComposesStages(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunKustomizeModule(t, root)

	harness := newRunHarness(t)

	options := runOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

	result, err := harness.run(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(result.Completed).To(Equal(runner.StageDeploy))
	g.Expect(result.ApplyCompleted).To(BeTrue())

	// The namespace plugin placed the controller package.
	controller := harness.stored(t, runDeploymentGVK, "run-namespace", "synthetic-controller")
	g.Expect(controller.Object).To(SatisfyAll(
		jq.Match(`.metadata.namespace == "run-namespace"`),
		jq.Match(`.spec.template.spec.containers[0].image == "quay.io/example/controller:v1"`),
		jq.Match(`.spec.template.spec.initContainers[0].image == "quay.io/example/controller:v1"`),
		jq.Match(`.spec.template.spec.containers[0].env // [] | map(.name) == ["RELATED_IMAGE_ALPHA","LOG_LEVEL","APPLICATIONS_NAMESPACE","ODH_MODULE_OPERATOR_PLATFORM_TYPE"]`),
	))

	// The rendered Namespace follows the framework's
	// never-own-namespaces policy: applied, ordinary, unowned.
	namespace := harness.stored(t, runNamespaceGVK, "", "aux-namespace")
	g.Expect(namespace.Object).To(SatisfyAll(
		jq.Match(`.metadata.ownerReferences == null`),
		jq.Match(`.metadata.name == "aux-namespace"`),
	))

	// The platform ConfigMap and the plain resources land with the
	// namespace plugin's placement.
	platform := harness.stored(t, runConfigMapGVK, "run-namespace", "odh-synthetic-config")
	g.Expect(platform.Object).To(jq.Match(`.data.platformVersion == "2.19.0"`))
	values := harness.stored(t, runConfigMapGVK, "run-namespace", "values-cm")
	g.Expect(values.Object).To(jq.Match(`.data.message == "authored-value"`))
	gate := harness.stored(t, runConfigMapGVK, "run-namespace", "gate-cm")
	g.Expect(gate.Object).To(jq.Match(`.metadata.labels["app.opendatahub.io/upgrade-gate"] == "true"`))
}

// TestRunChainImageSourceResolution proves the image source contract
// through the real chain: environment beats file, an explicitly empty
// environment variable masks the file value and the unresolved
// controller image fails the run, and the file alone resolves when no
// variable is set.
func TestRunChainImageSourceResolution(t *testing.T) {
	root := t.TempDir()
	writeRunHelmVariant(t, root, runHelmSourceImageDefinitionYAML, runDeploymentTemplateYAML)

	t.Run("environment wins over file", func(t *testing.T) {
		g := NewWithT(t)

		t.Setenv("RELATED_IMAGE_ALPHA", "quay.io/example/alpha:env")

		harness := newRunHarness(t)

		options := runOptions(root)
		options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

		_, err := harness.run(t.Context(), options)
		g.Expect(err).NotTo(HaveOccurred())

		controller := harness.stored(t, runDeploymentGVK, "run-namespace", "synthetic-controller")
		g.Expect(controller.Object).To(SatisfyAll(
			jq.Match(`.spec.template.spec.containers[0].image == "quay.io/example/alpha:env"`),
			jq.Match(`.spec.template.spec.containers[0].env[0].value == "quay.io/example/alpha:env"`),
		))
	})

	t.Run("empty environment masks the file", func(t *testing.T) {
		g := NewWithT(t)

		t.Setenv("RELATED_IMAGE_ALPHA", "")

		harness := newRunHarness(t)

		options := runOptions(root)
		options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

		// The masked controller image does not resolve, and the
		// strict policy fails the run instead of keeping the chart
		// default.
		_, err := harness.run(t.Context(), options)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`container "manager" image unresolved in Deployment synthetic-controller`))
	})

	t.Run("file alone resolves", func(t *testing.T) {
		g := NewWithT(t)

		harness := newRunHarness(t)

		options := runOptions(root)
		options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

		_, err := harness.run(t.Context(), options)
		g.Expect(err).NotTo(HaveOccurred())

		controller := harness.stored(t, runDeploymentGVK, "run-namespace", "synthetic-controller")
		g.Expect(controller.Object).To(jq.Match(
			`.spec.template.spec.containers[0].image == "quay.io/example/alpha:v3"`))
	})
}

// TestRunChainUnrequestedAmbientVariablesNotInjected proves ambient
// image sources the definition does not declare never reach the
// rendered controller.
func TestRunChainUnrequestedAmbientVariablesNotInjected(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("RELATED_IMAGE_BETA", "quay.io/example/beta:v1")

	root := t.TempDir()
	writeRunHelmModule(t, root)

	harness := newRunHarness(t)

	options := runOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

	// The declared source is unresolved (no environment, the file
	// does not carry it): only the platform env is injected.
	options.RelatedImagesFile = runImagesFile(t, root, "UNRELATED=nothing\n")

	_, err := harness.run(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	controller := harness.stored(t, runDeploymentGVK, "run-namespace", "synthetic-controller")
	g.Expect(controller.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[0].env // [] | map(.name) == ["LOG_LEVEL","APPLICATIONS_NAMESPACE","ODH_MODULE_OPERATOR_PLATFORM_TYPE"]`),
		jq.Match(`.spec.template.spec.containers[0].env // [] | map(.name) | index("RELATED_IMAGE_BETA") == null`),
	))
}

// TestRunChainMissingImagesFileFailsBeforeCluster proves an explicit
// but unreadable related-images file is an image-source input error
// that stops the chain before any client operation.
func TestRunChainMissingImagesFileFailsBeforeCluster(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunHelmModule(t, root)

	harness := newRunHarness(t)

	options := runOptions(root)
	options.RelatedImagesFile = filepath.Join(root, "nonexistent.env")

	result, err := harness.run(t.Context(), options)
	g.Expect(err).To(HaveOccurred())

	var imageErr *images.ImageSourceError
	g.Expect(errors.As(err, &imageErr)).To(BeTrue())

	g.Expect(result.Attempted).To(Equal(runner.StageLoad))
	g.Expect(result.Owner).To(BeNil())
	g.Expect(harness.connected).To(BeFalse())
	g.Expect(harness.writes).To(BeEmpty())
	g.Expect(harness.gets).To(BeEmpty())
}

// TestRunChainLocalFailuresStopBeforeClientOperations proves local
// preparation failures — an invalid definition, a render failure, and
// an expired invocation — stop the chain before any client operation
// or write.
func TestRunChainLocalFailuresStopBeforeClientOperations(t *testing.T) {
	t.Parallel()

	t.Run("invalid definition", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeModuleFile(t, root, "definition.yaml", invalidTypeDefinitionYAML)

		harness := newRunHarness(t)

		result, err := harness.run(t.Context(), runOptions(root))
		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())

		g.Expect(result.Owner).To(BeNil())
		g.Expect(harness.connected).To(BeFalse())
		g.Expect(harness.writes).To(BeEmpty())
	})

	t.Run("render failure", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeModuleFile(t, root, "definition.yaml", helmDefinitionYAML)
		writeModuleFile(t, root, "manifests/chart/Chart.yaml", plainChartYAML)
		writeModuleFile(t, root, "manifests/chart/values.yaml", chartValuesYAML)
		writeModuleFile(t, root, "manifests/chart/templates/broken.yaml", brokenTemplateYAML)

		harness := newRunHarness(t)

		result, err := harness.run(t.Context(), runOptions(root))
		g.Expect(err).To(HaveOccurred())

		g.Expect(result.Attempted).To(Equal(runner.StageRender))
		g.Expect(result.Completed).To(Equal(runner.StageLoad))
		g.Expect(result.Owner).To(BeNil())
		g.Expect(harness.connected).To(BeFalse())
		g.Expect(harness.writes).To(BeEmpty())
	})

	t.Run("expired invocation", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeRunHelmModule(t, root)

		harness := newRunHarness(t)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		result, err := harness.run(ctx, runOptions(root))
		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

		g.Expect(result.Owner).To(BeNil())
		g.Expect(harness.writes).To(BeEmpty())
	})
}

// TestRunChainPreparationFailures proves the preparation contract
// through the real runner chain: a failed definition apply stops the
// run with safe public text, the raw cause preserved under Unwrap, no
// owner, and no payload write; a definition CRD that never serves
// stops the run the same way; and a failed payload apply keeps the
// confirmed owner.
func TestRunChainPreparationFailures(t *testing.T) {
	t.Parallel()

	t.Run("definition apply failure is safe and cause-preserving", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeRunHelmModule(t, root)

		harness := newRunHarness(t)
		harness.failApply[runDefinitionWrite] = apierrors.NewForbidden(
			schema.GroupResource{Group: "config.opendatahub.io", Resource: "platformmoduledefinitions"},
			"synthetic", errors.New("CHAIN-MARKER-refused"))

		result, err := harness.run(t.Context(), runOptions(root))
		g.Expect(err).To(HaveOccurred())

		g.Expect(result.Attempted).To(Equal(runner.StageDeploy))
		g.Expect(result.Completed).To(Equal(runner.StageRender))
		g.Expect(result.Owner).To(BeNil())
		g.Expect(result.ApplyCompleted).To(BeFalse())

		// The safe public text names the failed operation without
		// echoing the server's own message; the cause identity stays
		// reachable through Unwrap.
		g.Expect(err.Error()).To(SatisfyAll(
			ContainSubstring("applying the module definition"),
			Not(ContainSubstring("CHAIN-MARKER-refused")),
		))
		g.Expect(apierrors.IsForbidden(err)).To(BeTrue())

		// The definition apply was attempted; no payload write was.
		_, definitionWrite := harness.writeIndex(runDefinitionWrite)
		g.Expect(definitionWrite).To(BeNumerically(">=", 0))
		deploymentWrites, _ := harness.writeIndex("Kind=Deployment")
		g.Expect(deploymentWrites).To(Equal(0))
	})

	t.Run("never-serving CRD stops before the definition", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeRunHelmModule(t, root)

		harness := newRunHarness(t)
		harness.crdNeverEstablished = true

		options := runOptions(root)
		options.Timeout = 600 * time.Millisecond

		result, err := harness.run(t.Context(), options)
		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())

		g.Expect(result.Owner).To(BeNil())
		g.Expect(result.ApplyCompleted).To(BeFalse())
		g.Expect(harness.writes).To(HaveLen(1))
		g.Expect(harness.writes[0]).To(ContainSubstring("Kind=CustomResourceDefinition"))
	})

	t.Run("failed payload apply keeps the confirmed owner", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeRunHelmModule(t, root)

		harness := newRunHarness(t)
		harness.failApply["Kind=Role run-namespace/synthetic-role"] =
			apierrors.NewForbidden(
				schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "roles"},
				"synthetic-role", errors.New("not allowed"))

		result, err := harness.run(t.Context(), runOptions(root))
		g.Expect(err).To(HaveOccurred())

		g.Expect(result.Attempted).To(Equal(runner.StageDeploy))
		g.Expect(result.Completed).To(Equal(runner.StageRender))
		g.Expect(result.Owner).NotTo(BeNil())
		g.Expect(result.Owner.Name).To(Equal("synthetic"))
		g.Expect(result.ApplyCompleted).To(BeFalse())
	})
}

// TestRunChainDefaultMakesNoWorkloadObservations proves the default
// run performs exactly the deploy action's own pre-apply lookup on
// the controller Deployment — no workload observation, no readiness
// claim — and its report makes no wait claim.
func TestRunChainDefaultMakesNoWorkloadObservations(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunHelmModule(t, root)

	harness := newRunHarness(t)

	options := runOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

	result, err := harness.run(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(harness.getCount(runControllerWrite)).To(Equal(1))
	g.Expect(result.WaitRequested).To(BeFalse())
	g.Expect(result.WaitCompleted).To(BeFalse())
	g.Expect(result.WaitTargets).To(BeEmpty())
}

// TestRunChainWaitCompletion proves the requested wait observes only
// the matched controller Deployment — never the unrelated one —
// through the real public status helper, completing once the scripted
// rollout reports done, with the override budget forwarded.
func TestRunChainWaitCompletion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunHelmModule(t, root)

	harness := newRunHarness(t)
	harness.frames[runControllerObservation] = []runFrame{
		{live: runDeploymentStatus(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		})},
		{live: runDeploymentStatus(nil)},
	}

	options := runOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)
	options.Wait = true
	options.WaitTimeout = 5 * time.Second

	result, err := harness.run(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(result.WaitRequested).To(BeTrue())
	g.Expect(result.WaitCompleted).To(BeTrue())
	g.Expect(result.WaitTargets).To(Equal([]runner.Target{
		{Namespace: "run-namespace", Name: "synthetic-controller"},
	}))

	// The controller was observed beyond the deploy action's single
	// lookup; the unrelated Deployment never was.
	g.Expect(harness.getCount(runControllerWrite)).To(BeNumerically(">=", 2))
	g.Expect(harness.getCount("run-namespace/unrelated-controller")).To(Equal(1))
}

// TestRunChainWaitShortBudgetTimesOut proves an expiring wait budget
// fails as a runtime deadline error while the report retains the
// confirmed owner and apply completion.
func TestRunChainWaitShortBudgetTimesOut(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunHelmModule(t, root)

	harness := newRunHarness(t)
	harness.frames[runControllerObservation] = []runFrame{
		{live: runDeploymentStatus(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		})},
	}

	options := runOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)
	options.Wait = true
	options.WaitTimeout = 50 * time.Millisecond

	result, err := harness.run(t.Context(), options)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())

	g.Expect(result.Attempted).To(Equal(runner.StageDeploy))
	g.Expect(result.Completed).To(Equal(runner.StageDeploy))
	g.Expect(result.Owner).NotTo(BeNil())
	g.Expect(result.ApplyCompleted).To(BeTrue())
	g.Expect(result.WaitRequested).To(BeTrue())
	g.Expect(result.WaitCompleted).To(BeFalse())
	g.Expect(result.WaitTargets).To(Equal([]runner.Target{
		{Namespace: "run-namespace", Name: "synthetic-controller"},
	}))
}

// TestRunChainWaitOverallDeadlineWins proves the wait budget is a
// child of the overall invocation deadline: a short overall timeout
// caps a much longer wait budget.
func TestRunChainWaitOverallDeadlineWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunHelmModule(t, root)

	harness := newRunHarness(t)
	harness.frames[runControllerObservation] = []runFrame{
		{live: runDeploymentStatus(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		})},
	}

	options := runOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)
	options.Wait = true
	options.WaitTimeout = 30 * time.Second
	options.Timeout = 2 * time.Second

	started := time.Now()

	result, err := harness.run(t.Context(), options)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())

	// The overall deadline, not the wait budget, ended the run.
	g.Expect(time.Since(started)).To(BeNumerically("<", 10*time.Second))
	g.Expect(result.ApplyCompleted).To(BeTrue())
	g.Expect(result.WaitCompleted).To(BeFalse())
}

// TestRunChainIdempotentRerun proves a second run over the same
// server reuses the persisted identity: the owner UID is stable, the
// definition CRD takes the same server-side-apply path on both runs,
// and every stage completes again.
func TestRunChainIdempotentRerun(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunHelmModule(t, root)

	harness := newRunHarness(t)

	options := runOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

	first, err := harness.run(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	second, err := harness.run(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(second.Owner.UID).To(Equal(first.Owner.UID))
	g.Expect(second.ApplyCompleted).To(BeTrue())

	// The definition CRD is applied on every run — create-or-update
	// is the same path — and the payload's own CRD reapplies too.
	crdWrites, _ := harness.writeIndex("Kind=CustomResourceDefinition platformmoduledefinitions.config.opendatahub.io")
	g.Expect(crdWrites).To(Equal(2))
}

// TestRunChainHelmFallbackDeploymentName proves the empty configured
// deployment name falls back to the module's metadata.name — the
// operator's deploymentNameFor release-name fallback — with the env
// injection landing on the fallback target.
func TestRunChainHelmFallbackDeploymentName(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunHelmVariant(t, root, runHelmFallbackDefinitionYAML, runFallbackDeploymentTemplateYAML)

	harness := newRunHarness(t)

	_, err := harness.run(t.Context(), runOptions(root))
	g.Expect(err).NotTo(HaveOccurred())

	controller := harness.stored(t, runDeploymentGVK, "run-namespace", "synthetic")
	g.Expect(controller.Object).To(SatisfyAll(
		jq.Match(`.spec.template.spec.containers[0].image == "quay.io/example/controller:v1"`),
		jq.Match(`.spec.template.spec.containers[0].env // [] | map(.name) == ["APPLICATIONS_NAMESPACE","ODH_MODULE_OPERATOR_PLATFORM_TYPE"]`),
	))
}

// TestRunChainHelmMalformedEnvFailsRender proves the typed-target
// boundary through the real chain: a chart-rendered controller
// Deployment with a malformed env field fails the render stage before
// any connection or write, instead of being repaired.
func TestRunChainHelmMalformedEnvFailsRender(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeRunHelmVariant(t, root, runHelmMalformedEnvDefinitionYAML, runMalformedEnvDeploymentTemplateYAML)

	harness := newRunHarness(t)

	result, err := harness.run(t.Context(), runOptions(root))
	g.Expect(err).To(HaveOccurred())

	g.Expect(result.Attempted).To(Equal(runner.StageRender))
	g.Expect(result.Completed).To(Equal(runner.StageLoad))
	g.Expect(result.Owner).To(BeNil())
	g.Expect(result.ApplyCompleted).To(BeFalse())
	g.Expect(harness.connected).To(BeFalse())
	g.Expect(harness.writes).To(BeEmpty())
}
