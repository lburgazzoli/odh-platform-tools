//go:build integration

package integration

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
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

// TestDeployChainFieldManagerCoversPreparationAndPayload proves the
// production wiring — the real loader, renderers, deployer, owner
// preparation, and framework payload action — sends the invocation's
// field manager on both preparation writes and every ordinary payload
// apply, while the bundled payload CRD keeps the framework's fixed
// platform manager; the empty Go option resolves to the platform
// identity, and separate invocations with different managers stay
// isolated.
func TestDeployChainFieldManagerCoversPreparationAndPayload(t *testing.T) {
	t.Run("custom manager reaches preparation and ordinary payload", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeDeployHelmModule(t, root)

		harness := newDeployHarness(t)

		options := deployOptions(root)
		options.FieldManager = "team.example.deployer"

		_, err := harness.deploy(t.Context(), options)
		g.Expect(err).NotTo(HaveOccurred())

		// Both owner-preparation writes carry the custom manager.
		g.Expect(harness.managersOf("platformmoduledefinitions")).
			To(Equal([]string{"team.example.deployer"}))
		g.Expect(harness.managersOf(runDefinitionWrite)).
			To(Equal([]string{"team.example.deployer"}))

		// The bundled payload CRD keeps the framework's fixed
		// platform manager; every other payload apply — the ordinary
		// writes and the rendered Namespace — carries the custom one.
		g.Expect(harness.managersOf("syntheticmodules")).
			To(Equal([]string{"platform.opendatahub.io"}))
		for _, captured := range harness.applyManagers {
			if strings.Contains(captured.id, "syntheticmodules") {
				continue
			}
			if strings.Contains(captured.id, "platformmoduledefinitions") ||
				strings.Contains(captured.id, runDefinitionWrite) {
				continue
			}
			g.Expect(captured.manager).To(Equal("team.example.deployer"),
				"payload write %s", captured.id)
		}
	})

	t.Run("empty option means the platform default", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeDeployHelmModule(t, root)

		harness := newDeployHarness(t)

		_, err := harness.deploy(t.Context(), deployOptions(root))
		g.Expect(err).NotTo(HaveOccurred())

		for _, captured := range harness.applyManagers {
			g.Expect(captured.manager).To(Equal("platform.opendatahub.io"),
				"write %s", captured.id)
		}
	})

	t.Run("repeated custom use keeps invocations isolated", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeDeployHelmModule(t, root)

		first := newDeployHarness(t)
		firstOptions := deployOptions(root)
		firstOptions.FieldManager = "first.example"
		_, err := first.deploy(t.Context(), firstOptions)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(first.managersOf(runDefinitionWrite)).To(Equal([]string{"first.example"}))

		second := newDeployHarness(t)
		secondOptions := deployOptions(root)
		secondOptions.FieldManager = "second.example"
		_, err = second.deploy(t.Context(), secondOptions)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(second.managersOf(runDefinitionWrite)).To(Equal([]string{"second.example"}))
		g.Expect(first.managersOf(runDefinitionWrite)).To(Equal([]string{"first.example"}))
	})

	t.Run("invalid manager fails before any cluster access", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeDeployHelmModule(t, root)

		harness := newDeployHarness(t)

		options := deployOptions(root)
		options.FieldManager = "team/manager"

		result, err := harness.deploy(t.Context(), options)
		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.Is(err, moduleerrors.ErrInvalidDeployOptions)).To(BeTrue())
		g.Expect(result).To(Equal(deployer.Result{}))
		g.Expect(harness.connected).To(BeFalse())
		g.Expect(harness.writes).To(BeEmpty())
	})
}

// loader, Viper file images, the shared Helm engine with the library
// apply-order post-renderer ahead of both struct injectors, the
// deployer-internal owner preparation, and the real framework deploy
// action — and proves the cross-service contract: the literal
// controller image is shared by main and init containers, the env
// injection lands on the main container alone, the platform ConfigMap
// participates in the framework's enriched sort, authored values reach
// templates, and the write order is the pre-migration apply order.
func TestDeployChainHelmComposesStages(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeDeployHelmModule(t, root)

	harness := newDeployHarness(t)

	options := deployOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

	result, err := harness.deploy(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	// The staged report carries the confirmed owner with its
	// simulated server identity.
	g.Expect(result.Completed).To(Equal(deployer.StageDeploy))
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
func TestDeployChainKustomizeComposesStages(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeDeployKustomizeModule(t, root)

	harness := newDeployHarness(t)

	options := deployOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

	result, err := harness.deploy(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(result.Completed).To(Equal(deployer.StageDeploy))
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
// controller image fails the deploy, and the file alone resolves when no
// variable is set.
func TestDeployChainImageSourceResolution(t *testing.T) {
	root := t.TempDir()
	writeDeployHelmVariant(t, root, runHelmSourceImageDefinitionYAML, runDeploymentTemplateYAML)

	t.Run("environment wins over file", func(t *testing.T) {
		g := NewWithT(t)

		t.Setenv("RELATED_IMAGE_ALPHA", "quay.io/example/alpha:env")

		harness := newDeployHarness(t)

		options := deployOptions(root)
		options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

		_, err := harness.deploy(t.Context(), options)
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

		harness := newDeployHarness(t)

		options := deployOptions(root)
		options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

		// The masked controller image does not resolve, and the
		// strict policy fails the deploy instead of keeping the chart
		// default.
		_, err := harness.deploy(t.Context(), options)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring(`container "manager" image unresolved in Deployment synthetic-controller`))
	})

	t.Run("file alone resolves", func(t *testing.T) {
		g := NewWithT(t)

		harness := newDeployHarness(t)

		options := deployOptions(root)
		options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

		_, err := harness.deploy(t.Context(), options)
		g.Expect(err).NotTo(HaveOccurred())

		controller := harness.stored(t, runDeploymentGVK, "run-namespace", "synthetic-controller")
		g.Expect(controller.Object).To(jq.Match(
			`.spec.template.spec.containers[0].image == "quay.io/example/alpha:v3"`))
	})
}

// TestRunChainUnrequestedAmbientVariablesNotInjected proves ambient
// image sources the definition does not declare never reach the
// rendered controller.
func TestDeployChainUnrequestedAmbientVariablesNotInjected(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("RELATED_IMAGE_BETA", "quay.io/example/beta:v1")

	root := t.TempDir()
	writeDeployHelmModule(t, root)

	harness := newDeployHarness(t)

	options := deployOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

	// The declared source is unresolved (no environment, the file
	// does not carry it): only the platform env is injected.
	options.RelatedImagesFile = runImagesFile(t, root, "UNRELATED=nothing\n")

	_, err := harness.deploy(t.Context(), options)
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
func TestDeployChainMissingImagesFileFailsBeforeCluster(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeDeployHelmModule(t, root)

	harness := newDeployHarness(t)

	options := deployOptions(root)
	options.RelatedImagesFile = filepath.Join(root, "nonexistent.env")

	result, err := harness.deploy(t.Context(), options)
	g.Expect(err).To(HaveOccurred())

	g.Expect(errors.Is(err, moduleerrors.ErrImageSource)).To(BeTrue())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeFalse())

	var imageErr *moduleerrors.ModuleError
	g.Expect(errors.As(err, &imageErr)).To(BeTrue())
	g.Expect(errors.Is(err, fs.ErrNotExist)).To(BeTrue())

	g.Expect(result.Attempted).To(Equal(deployer.StageLoad))
	g.Expect(result.Owner).To(BeNil())
	g.Expect(harness.connected).To(BeFalse())
	g.Expect(harness.writes).To(BeEmpty())
	g.Expect(harness.gets).To(BeEmpty())
}

// TestRunChainLocalFailuresStopBeforeClientOperations proves local
// preparation failures — an invalid definition, a render failure, and
// an expired invocation — stop the chain before any client operation
// or write.
func TestDeployChainLocalFailuresStopBeforeClientOperations(t *testing.T) {
	t.Parallel()

	t.Run("invalid definition", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeModuleFile(t, root, "definition.yaml", invalidTypeDefinitionYAML)

		harness := newDeployHarness(t)

		result, err := harness.deploy(t.Context(), deployOptions(root))
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

		harness := newDeployHarness(t)

		result, err := harness.deploy(t.Context(), deployOptions(root))
		g.Expect(err).To(HaveOccurred())

		g.Expect(result.Attempted).To(Equal(deployer.StageRender))
		g.Expect(result.Completed).To(Equal(deployer.StageLoad))
		g.Expect(result.Owner).To(BeNil())
		g.Expect(harness.connected).To(BeFalse())
		g.Expect(harness.writes).To(BeEmpty())
	})

	t.Run("expired invocation", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeDeployHelmModule(t, root)

		harness := newDeployHarness(t)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		result, err := harness.deploy(ctx, deployOptions(root))
		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())

		g.Expect(result.Owner).To(BeNil())
		g.Expect(harness.writes).To(BeEmpty())
	})
}

// TestRunChainPreparationFailures proves the preparation contract
// through the real deployer chain: a failed definition apply stops the
// run with safe public text, the raw cause preserved under Unwrap, no
// owner, and no payload write; a definition CRD that never serves
// stops the deploy the same way; and a failed payload apply keeps the
// confirmed owner.
func TestDeployChainPreparationFailures(t *testing.T) {
	t.Parallel()

	t.Run("definition apply failure is safe and cause-preserving", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		root := t.TempDir()
		writeDeployHelmModule(t, root)

		harness := newDeployHarness(t)
		harness.failApply[runDefinitionWrite] = apierrors.NewForbidden(
			schema.GroupResource{Group: "config.opendatahub.io", Resource: "platformmoduledefinitions"},
			"synthetic", errors.New("CHAIN-MARKER-refused"))

		result, err := harness.deploy(t.Context(), deployOptions(root))
		g.Expect(err).To(HaveOccurred())

		g.Expect(result.Attempted).To(Equal(deployer.StageDeploy))
		g.Expect(result.Completed).To(Equal(deployer.StageRender))
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
		g.Expect(errors.Is(err, moduleerrors.ErrOwnerPreparation)).To(BeTrue())
		g.Expect(errors.Is(err, moduleerrors.ErrDeployApply)).To(BeFalse())

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
		writeDeployHelmModule(t, root)

		harness := newDeployHarness(t)
		harness.crdNeverEstablished = true

		options := deployOptions(root)
		options.Timeout = 600 * time.Millisecond

		result, err := harness.deploy(t.Context(), options)
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
		writeDeployHelmModule(t, root)

		harness := newDeployHarness(t)
		harness.failApply["Kind=Role run-namespace/synthetic-role"] =
			apierrors.NewForbidden(
				schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "roles"},
				"synthetic-role", errors.New("not allowed"))

		result, err := harness.deploy(t.Context(), deployOptions(root))
		g.Expect(err).To(HaveOccurred())

		g.Expect(result.Attempted).To(Equal(deployer.StageDeploy))
		g.Expect(result.Completed).To(Equal(deployer.StageRender))
		g.Expect(result.Owner).NotTo(BeNil())
		g.Expect(result.Owner.Name).To(Equal("synthetic"))
		g.Expect(result.ApplyCompleted).To(BeFalse())
	})
}

// TestRunChainDefaultMakesNoWorkloadObservations proves the default
// run performs exactly the deploy action's own pre-apply lookup on
// the controller Deployment — no workload observation, no readiness
// claim — and its report makes no wait claim.
func TestDeployChainDefaultMakesNoWorkloadObservations(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeDeployHelmModule(t, root)

	harness := newDeployHarness(t)

	options := deployOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

	result, err := harness.deploy(t.Context(), options)
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
func TestDeployChainWaitCompletion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeDeployHelmModule(t, root)

	harness := newDeployHarness(t)
	harness.frames[runControllerObservation] = []runFrame{
		{live: runDeploymentStatus(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		})},
		{live: runDeploymentStatus(nil)},
	}

	options := deployOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)
	options.Wait = true
	options.WaitTimeout = 5 * time.Second

	result, err := harness.deploy(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(result.WaitRequested).To(BeTrue())
	g.Expect(result.WaitCompleted).To(BeTrue())
	g.Expect(result.WaitTargets).To(Equal([]types.NamespacedName{
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
func TestDeployChainWaitShortBudgetTimesOut(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeDeployHelmModule(t, root)

	harness := newDeployHarness(t)
	harness.frames[runControllerObservation] = []runFrame{
		{live: runDeploymentStatus(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		})},
	}

	options := deployOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)
	options.Wait = true
	options.WaitTimeout = 50 * time.Millisecond

	result, err := harness.deploy(t.Context(), options)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())

	g.Expect(result.Attempted).To(Equal(deployer.StageDeploy))
	g.Expect(result.Completed).To(Equal(deployer.StageDeploy))
	g.Expect(result.Owner).NotTo(BeNil())
	g.Expect(result.ApplyCompleted).To(BeTrue())
	g.Expect(result.WaitRequested).To(BeTrue())
	g.Expect(result.WaitCompleted).To(BeFalse())
	g.Expect(result.WaitTargets).To(Equal([]types.NamespacedName{
		{Namespace: "run-namespace", Name: "synthetic-controller"},
	}))
}

// TestRunChainWaitOverallDeadlineWins proves the wait budget is a
// child of the overall invocation deadline: a short overall timeout
// caps a much longer wait budget.
func TestDeployChainWaitOverallDeadlineWins(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeDeployHelmModule(t, root)

	harness := newDeployHarness(t)
	harness.frames[runControllerObservation] = []runFrame{
		{live: runDeploymentStatus(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		})},
	}

	options := deployOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)
	options.Wait = true
	options.WaitTimeout = 30 * time.Second
	options.Timeout = 2 * time.Second

	started := time.Now()

	result, err := harness.deploy(t.Context(), options)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())

	// The overall deadline, not the wait budget, ended the deploy.
	g.Expect(time.Since(started)).To(BeNumerically("<", 10*time.Second))
	g.Expect(result.ApplyCompleted).To(BeTrue())
	g.Expect(result.WaitCompleted).To(BeFalse())
}

// TestRunChainIdempotentRerun proves a second run over the same
// server reuses the persisted identity: the owner UID is stable, the
// definition CRD takes the same server-side-apply path on both runs,
// and every stage completes again.
func TestDeployChainIdempotentRerun(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeDeployHelmModule(t, root)

	harness := newDeployHarness(t)

	options := deployOptions(root)
	options.RelatedImagesFile = runImagesFile(t, root, runImagesEnv)

	first, err := harness.deploy(t.Context(), options)
	g.Expect(err).NotTo(HaveOccurred())

	second, err := harness.deploy(t.Context(), options)
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
func TestDeployChainHelmFallbackDeploymentName(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeDeployHelmVariant(t, root, runHelmFallbackDefinitionYAML, runFallbackDeploymentTemplateYAML)

	harness := newDeployHarness(t)

	_, err := harness.deploy(t.Context(), deployOptions(root))
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
func TestDeployChainHelmMalformedEnvFailsRender(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := t.TempDir()
	writeDeployHelmVariant(t, root, runHelmMalformedEnvDefinitionYAML, runMalformedEnvDeploymentTemplateYAML)

	harness := newDeployHarness(t)

	result, err := harness.deploy(t.Context(), deployOptions(root))
	g.Expect(err).To(HaveOccurred())

	g.Expect(result.Attempted).To(Equal(deployer.StageRender))
	g.Expect(result.Completed).To(Equal(deployer.StageLoad))
	g.Expect(result.Owner).To(BeNil())
	g.Expect(result.ApplyCompleted).To(BeFalse())
	g.Expect(harness.connected).To(BeFalse())
	g.Expect(harness.writes).To(BeEmpty())
}
