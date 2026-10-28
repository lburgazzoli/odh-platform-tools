package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	rest "k8s.io/client-go/rest"

	deployer "github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/images"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// confirmedOwner is the server-backed owner identity of the golden
// reports.
func confirmedOwner() *toolApi.PlatformModuleDefinition {
	return &toolApi.PlatformModuleDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: toolApi.APIVersion, Kind: toolApi.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic", UID: "definition-uid"},
	}
}

// imageSourceError returns one real image-source failure from the
// images service: an explicit but unreadable related-images file.
func imageSourceError() error {
	_, err := images.Load("/nonexistent/images.env", nil)

	return err
}

// recordingDeploy is a fake action boundary recording every invocation.
type recordingDeploy struct {
	calls    int
	contexts []context.Context
	getters  []genericclioptions.RESTClientGetter
	options  []deployer.Options
	result   deployer.Result
	err      error
}

// deploy records one invocation and returns the configured outcome.
func (r *recordingDeploy) deploy(
	ctx context.Context,
	getter genericclioptions.RESTClientGetter,
	options deployer.Options,
) (deployer.Result, error) {
	r.calls++
	r.contexts = append(r.contexts, ctx)
	r.getters = append(r.getters, getter)
	r.options = append(r.options, options)

	return r.result, r.err
}

// failingWriter fails every write.
type failingWriter struct{}

// Write fails unconditionally.
func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failure")
}

// exitCode maps one deploy command error to its process exit status:
// classified outcomes keep their own code, and flag or usage
// failures are usage failures.
func exitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}

	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}

	return ExitUsage
}

// executeDeploy runs the deploy command with the given flag arguments.
func executeDeploy(
	ctx context.Context,
	t *testing.T,
	action Deploy,
	flags ...string,
) (string, string, error) {
	t.Helper()

	command := NewDeployCommand(action, testGetter(t))

	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(flags)

	err := command.ExecuteContext(ctx)

	return stdout.String(), stderr.String(), err
}

// deployTestKubeconfigYAML is a standalone kubeconfig whose context
// carries no namespace, pointing at an unreachable server: namespace
// resolution reads it locally and never contacts it.
const deployTestKubeconfigYAML = `apiVersion: v1
kind: Config
clusters:
- name: test-cluster
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
current-context: test-context
users:
- name: test-user
  user: {}
`

// testGetter returns standard ConfigFlags bound to a standalone
// no-namespace kubeconfig, so command tests never read the ambient
// kubeconfig: the resolved namespace is deterministically the
// standard default.
func testGetter(t *testing.T) *genericclioptions.ConfigFlags {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(deployTestKubeconfigYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path

	return flags
}

// deployFlags are the minimal required flag arguments.
var deployFlags = []string{
	"--config", "definition.yaml",
	"--platform-version", "2.19.0",
}

func TestDeployCommandForwardsOptionsContextAndGetter(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingDeploy{}

	getter := testGetter(t)
	// The deploy command owns no namespace flag: the effective
	// namespace comes from the standard getter, here an explicit
	// standard-flag override.
	explicitNamespace := "applications"
	getter.Namespace = &explicitNamespace
	command := NewDeployCommand(recorder.deploy, getter)

	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--config", "/work/modules/synthetic/definition.yaml",
		"--platform-version", "v2.19.0+dev",
		"--related-images-file", "/work/images.env",
		"--monitoring-namespace", "monitoring",
		"--platform-type", "OpenDataHub",
		"--field-manager", "team.example.deployer",
		"--timeout", "90s",
		"--wait",
		"--wait-timeout", "45s",
		"--output", "json",
	})

	ctx := t.Context()
	g.Expect(command.ExecuteContext(ctx)).NotTo(HaveOccurred())

	// Exactly one action call, with every flag value in a plain
	// options struct — the namespace resolved from the getter — the
	// caller's context, and the root's standard getter forwarded
	// untouched.
	g.Expect(recorder.calls).To(Equal(1))
	g.Expect(recorder.options).To(Equal([]deployer.Options{{
		DefinitionFile:      "/work/modules/synthetic/definition.yaml",
		RelatedImagesFile:   "/work/images.env",
		Namespace:           "applications",
		MonitoringNamespace: "monitoring",
		PlatformType:        "OpenDataHub",
		PlatformVersion:     "v2.19.0+dev",
		FieldManager:        "team.example.deployer",
		Timeout:             90 * time.Second,
		Wait:                true,
		WaitTimeout:         45 * time.Second,
	}}))
	g.Expect(recorder.contexts).To(HaveLen(1))
	g.Expect(recorder.contexts[0]).To(Equal(ctx))
	g.Expect(recorder.getters).To(HaveLen(1))
	g.Expect(recorder.getters[0]).To(BeIdenticalTo(getter))
}

func TestDeployCommandDefaults(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingDeploy{}

	_, _, err := executeDeploy(t.Context(), t, recorder.deploy, deployFlags...)
	g.Expect(err).NotTo(HaveOccurred())

	// No wait by default, the standard budgets, the canonical field
	// manager, text output, and the standard default namespace from
	// the standalone no-namespace kubeconfig.
	g.Expect(recorder.options).To(Equal([]deployer.Options{{
		DefinitionFile:  "definition.yaml",
		PlatformVersion: "2.19.0",
		Namespace:       "default",
		FieldManager:    deployer.DefaultFieldManager,
		Timeout:         DefaultTimeout,
		Wait:            false,
		WaitTimeout:     DefaultWaitTimeout,
	}}))
}

// TestDeployCommandRejectsEmptyFieldManager proves an explicitly
// empty --field-manager never reaches the injected action: it fails
// as a usage-classified options error with a zero-state report and
// the safe static message.
func TestDeployCommandRejectsEmptyFieldManager(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingDeploy{}

	stdout, _, err := executeDeploy(t.Context(), t, recorder.deploy,
		append(slicesClone(deployFlags), "--field-manager", "")...)

	g.Expect(recorder.calls).To(Equal(0))

	g.Expect(exitCode(err)).To(Equal(ExitUsage))
	var exitErr *ExitError
	g.Expect(errors.As(err, &exitErr)).To(BeTrue())
	g.Expect(errors.Is(exitErr.Err, moduleerrors.ErrInvalidDeployOptions)).To(BeTrue())
	g.Expect(exitErr.Err.Error()).To(Equal("invalid deploy options: FieldManager must not be empty"))

	// The report is the zero-state options failure: no attempted
	// stage, no owner, no apply claim.
	g.Expect(stdout).To(Equal(`module deploy definition.yaml: failed
invalid-options: invalid deploy options: FieldManager must not be empty
`))
}

// TestDeployCommandRejectsInvalidFieldManager proves an invalid
// nonempty --field-manager fails as a usage-classified options error
// through the real production action wiring — the deployer validates
// before any cluster access — and the rejected value appears in
// neither the report nor the failure summary.
func TestDeployCommandRejectsInvalidFieldManager(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	stdout, stderr, err := executeDeploy(t.Context(), t, Action,
		append(slicesClone(deployFlags), "--field-manager", "team/manager")...)

	g.Expect(exitCode(err)).To(Equal(ExitUsage))
	var exitErr *ExitError
	g.Expect(errors.As(err, &exitErr)).To(BeTrue())
	g.Expect(errors.Is(exitErr.Err, moduleerrors.ErrInvalidDeployOptions)).To(BeTrue())
	g.Expect(exitErr.Err.Error()).To(Equal(
		"invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters"))

	g.Expect(stdout).NotTo(ContainSubstring("team/manager"))
	g.Expect(stderr).NotTo(ContainSubstring("team/manager"))
	g.Expect(stdout).To(ContainSubstring(FailureInvalidOptions))
}

func TestDeployCommandClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		err      error
		category string
		code     int
	}{
		{"success", nil, "", ExitSuccess},
		{
			"cancellation",
			fmt.Errorf("run: %w", context.Canceled),
			FailureCancelled, ExitFailure,
		},
		{
			"deadline",
			fmt.Errorf("waiting: %w", context.DeadlineExceeded),
			FailureCancelled, ExitFailure,
		},
		{
			"invalid options",
			fmt.Errorf("load: %w", moduleerrors.ErrInvalidDeployOptions),
			FailureInvalidOptions, ExitUsage,
		},
		{
			"run options wrapper",
			moduleerrors.New(moduleerrors.ErrInvalidDeployOptions,
				"invalid deploy options: Timeout must be positive"),
			FailureInvalidOptions, ExitUsage,
		},
		{
			"lint options wrapper is not a deploy usage failure",
			moduleerrors.New(moduleerrors.ErrInvalidLintOptions,
				"invalid lint options: DefinitionFile is required"),
			FailureRuntime, ExitFailure,
		},
		{
			"owner preparation wrapper",
			moduleerrors.New(moduleerrors.ErrOwnerPreparation,
				"applying the module definition failed",
				moduleerrors.WithCause(errors.New("refused"))),
			FailureRuntime, ExitFailure,
		},
		{
			"deploy prepare wrapper",
			moduleerrors.New(moduleerrors.ErrDeployPrepare,
				"preparing the module deployment failed"),
			FailureRuntime, ExitFailure,
		},
		{
			"deploy apply wrapper",
			moduleerrors.New(moduleerrors.ErrDeployApply,
				"applying the module resources failed"),
			FailureRuntime, ExitFailure,
		},
		{
			"rollout wait wrapper",
			moduleerrors.New(moduleerrors.ErrRolloutWait,
				"waiting for module synthetic controller rollout failed: the wait was cancelled or its budget expired",
				moduleerrors.WithCause(context.DeadlineExceeded)),
			FailureCancelled, ExitFailure,
		},
		{
			"rollout wait wrapper carrying cancellation wins over runtime",
			moduleerrors.New(moduleerrors.ErrRolloutWait,
				"waiting for module synthetic controller rollout failed: the wait was cancelled or its budget expired",
				moduleerrors.WithCause(context.Canceled)),
			FailureCancelled, ExitFailure,
		},
		{
			"rollout wait wrapper without cancellation",
			moduleerrors.New(moduleerrors.ErrRolloutWait,
				"waiting for deployment run-namespace/synthetic-controller failed: refused"),
			FailureRuntime, ExitFailure,
		},
		{
			"missing wait target wrapper",
			moduleerrors.New(moduleerrors.ErrWaitTargetMissing,
				`no rendered controller deployment matched the wait target: module synthetic controller deployment "synthetic-controller"`),
			FailureRuntime, ExitFailure,
		},
		{
			"definition",
			fmt.Errorf("load: %w", moduleerrors.ErrInvalidDefinition),
			FailureDefinition, ExitUsage,
		},
		{
			"image file",
			fmt.Errorf("load: %w", imageSourceError()),
			FailureImageFile, ExitUsage,
		},
		{
			"runtime",
			errors.New("apply failure"),
			FailureRuntime, ExitFailure,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			category, code := classify(testCase.err)
			g.Expect(category).To(Equal(testCase.category))
			g.Expect(code).To(Equal(testCase.code))

			// The command wires the classification into its exit
			// outcome.
			recorder := &recordingDeploy{err: testCase.err}
			_, _, err := executeDeploy(t.Context(), t, recorder.deploy, deployFlags...)
			g.Expect(exitCode(err)).To(Equal(testCase.code))
		})
	}
}

func TestDeployCommandTextReportGolden(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingDeploy{result: deployer.Result{
			Attempted:      deployer.StageDeploy,
			Completed:      deployer.StageDeploy,
			Owner:          confirmedOwner(),
			ApplyCompleted: true,
		}}

		stdout, stderr, err := executeDeploy(t.Context(), t, recorder.deploy, deployFlags...)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(Equal(`module deploy definition.yaml: applied
owner: synthetic (uid definition-uid)
stages: deploy completed
apply: completed
`))
		g.Expect(stderr).To(BeEmpty())
	})

	t.Run("success with wait", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingDeploy{result: deployer.Result{
			Attempted:      deployer.StageDeploy,
			Completed:      deployer.StageDeploy,
			Owner:          confirmedOwner(),
			ApplyCompleted: true,
			WaitRequested:  true,
			WaitCompleted:  true,
			WaitTargets: []types.NamespacedName{
				{Namespace: "run-namespace", Name: "synthetic-controller"},
			},
		}}

		stdout, _, err := executeDeploy(t.Context(), t, recorder.deploy,
			append(slicesClone(deployFlags), "--wait")...)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(Equal(`module deploy definition.yaml: applied
owner: synthetic (uid definition-uid)
stages: deploy completed
apply: completed
wait: completed (run-namespace/synthetic-controller)
`))
	})

	t.Run("wait failure retains successful apply", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingDeploy{
			result: deployer.Result{
				Attempted:      deployer.StageDeploy,
				Completed:      deployer.StageDeploy,
				Owner:          confirmedOwner(),
				ApplyCompleted: true,
				WaitRequested:  true,
				WaitCompleted:  false,
				WaitTargets: []types.NamespacedName{
					{Namespace: "run-namespace", Name: "synthetic-controller"},
				},
			},
			err: fmt.Errorf("waiting for module synthetic controller rollout: %w", context.DeadlineExceeded),
		}

		stdout, stderr, err := executeDeploy(t.Context(), t, recorder.deploy,
			append(slicesClone(deployFlags), "--wait")...)
		g.Expect(exitCode(err)).To(Equal(ExitFailure))
		g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())

		g.Expect(stdout).To(Equal(`module deploy definition.yaml: failed
owner: synthetic (uid definition-uid)
stages: deploy completed
apply: completed
wait: incomplete
cancelled: waiting for module synthetic controller rollout: context deadline exceeded
`))
		g.Expect(stderr).To(ContainSubstring("module deploy definition.yaml: cancelled:"))
	})

	t.Run("failure without confirmed owner claims no write", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingDeploy{
			result: deployer.Result{
				Attempted: deployer.StageDeploy,
				Completed: deployer.StageRender,
			},
			err: errors.New("owner preparation failure"),
		}

		stdout, _, err := executeDeploy(t.Context(), t, recorder.deploy, deployFlags...)
		g.Expect(exitCode(err)).To(Equal(ExitFailure))

		// No owner line at all: the identity is unconfirmed, which is
		// not a claim that no write occurred.
		g.Expect(stdout).To(Equal(`module deploy definition.yaml: failed
stages: render completed
runtime: owner preparation failure
`))
	})
}

func TestDeployCommandJSONReportGolden(t *testing.T) {
	t.Parallel()

	t.Run("success without wait omits wait state", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingDeploy{result: deployer.Result{
			Attempted:      deployer.StageDeploy,
			Completed:      deployer.StageDeploy,
			Owner:          confirmedOwner(),
			ApplyCompleted: true,
		}}

		stdout, _, err := executeDeploy(t.Context(), t, recorder.deploy,
			append(slicesClone(deployFlags), "--output", "json")...)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(MatchJSON(`{
			"definition": "definition.yaml",
			"status": "applied",
			"owner": {
				"name": "synthetic",
				"uid": "definition-uid",
				"apiVersion": "config.opendatahub.io/v1alpha1",
				"kind": "PlatformModuleDefinition"
			},
			"attemptedStage": "deploy",
			"completedStage": "deploy",
			"applyCompleted": true
		}`))
	})

	t.Run("success with wait", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingDeploy{result: deployer.Result{
			Attempted:      deployer.StageDeploy,
			Completed:      deployer.StageDeploy,
			Owner:          confirmedOwner(),
			ApplyCompleted: true,
			WaitRequested:  true,
			WaitCompleted:  true,
			WaitTargets: []types.NamespacedName{
				{Namespace: "run-namespace", Name: "synthetic-controller"},
			},
		}}

		stdout, _, err := executeDeploy(t.Context(), t, recorder.deploy,
			append(slicesClone(deployFlags), "--wait", "--output", "json")...)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(stdout).To(MatchJSON(`{
			"definition": "definition.yaml",
			"status": "applied",
			"owner": {
				"name": "synthetic",
				"uid": "definition-uid",
				"apiVersion": "config.opendatahub.io/v1alpha1",
				"kind": "PlatformModuleDefinition"
			},
			"attemptedStage": "deploy",
			"completedStage": "deploy",
			"applyCompleted": true,
			"wait": {
				"requested": true,
				"completed": true,
				"targets": [
					{"namespace": "run-namespace", "name": "synthetic-controller"}
				]
			}
		}`))
	})

	t.Run("failure", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingDeploy{
			result: deployer.Result{
				Attempted:      deployer.StageDeploy,
				Completed:      deployer.StageDeploy,
				Owner:          confirmedOwner(),
				ApplyCompleted: true,
				WaitRequested:  true,
				WaitCompleted:  false,
			},
			err: fmt.Errorf("waiting: %w", context.DeadlineExceeded),
		}

		stdout, _, err := executeDeploy(t.Context(), t, recorder.deploy,
			append(slicesClone(deployFlags), "--wait", "--output", "json")...)
		g.Expect(exitCode(err)).To(Equal(ExitFailure))

		g.Expect(stdout).To(MatchJSON(`{
			"definition": "definition.yaml",
			"status": "failed",
			"owner": {
				"name": "synthetic",
				"uid": "definition-uid",
				"apiVersion": "config.opendatahub.io/v1alpha1",
				"kind": "PlatformModuleDefinition"
			},
			"attemptedStage": "deploy",
			"completedStage": "deploy",
			"applyCompleted": true,
			"wait": {
				"requested": true,
				"completed": false
			},
			"failure": {
				"category": "cancelled",
				"message": "waiting: context deadline exceeded"
			}
		}`))
	})
}

func TestDeployCommandFlagFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		flags []string
	}{
		{"missing config", []string{"--platform-version", "2.19.0"}},
		{"missing platform version", []string{"--config", "definition.yaml"}},
		{
			"invalid output format",
			append(slicesClone(deployFlags), "--output", "yaml"),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			recorder := &recordingDeploy{}

			stdout, _, err := executeDeploy(t.Context(), t, recorder.deploy, testCase.flags...)
			g.Expect(err).To(HaveOccurred())
			g.Expect(exitCode(err)).To(Equal(ExitUsage))
			g.Expect(recorder.calls).To(Equal(0))
			g.Expect(stdout).To(BeEmpty())
		})
	}
}

func TestDeployCommandForwardsInvalidWaitTimeout(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingDeploy{}

	// A negative enabled wait budget is forwarded unchanged: the
	// deployer rejects it as an options error, never an API error.
	_, _, err := executeDeploy(t.Context(), t, recorder.deploy,
		append(slicesClone(deployFlags), "--wait", "--wait-timeout", "-5s")...)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(recorder.options[0].Wait).To(BeTrue())
	g.Expect(recorder.options[0].WaitTimeout).To(Equal(-5 * time.Second))
}

func TestDeployCommandStatelessAcrossExecutions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	recorder := &recordingDeploy{}

	command := NewDeployCommand(recorder.deploy, genericclioptions.NewConfigFlags(true))

	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&bytes.Buffer{})

	command.SetArgs(slicesClone(deployFlags))
	g.Expect(command.ExecuteContext(t.Context())).NotTo(HaveOccurred())

	command.SetArgs(append(slicesClone(deployFlags), "--output", FormatJSON))
	g.Expect(command.ExecuteContext(t.Context())).NotTo(HaveOccurred())

	// The second execution sees only its own flag values: no state
	// leaks from the first run into options or output format.
	g.Expect(recorder.calls).To(Equal(2))
	g.Expect(recorder.options[0].Wait).To(BeFalse())
	g.Expect(recorder.options[1].PlatformVersion).To(Equal("2.19.0"))
	g.Expect(stdout.String()).To(ContainSubstring("module deploy definition.yaml: applied"))
	g.Expect(stdout.String()).To(ContainSubstring(`"definition":"definition.yaml"`))
}

func TestDeployCommandWriteFailuresExitAsExecutionFailures(t *testing.T) {
	t.Parallel()

	t.Run("report write failure preserves cancellation identity", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingDeploy{
			err: fmt.Errorf("apply: %w", context.Canceled),
		}

		command := NewDeployCommand(recorder.deploy, genericclioptions.NewConfigFlags(true))
		command.SetOut(failingWriter{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(slicesClone(deployFlags))

		err := command.ExecuteContext(t.Context())

		g.Expect(exitCode(err)).To(Equal(ExitFailure))
		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		g.Expect(err.Error()).To(ContainSubstring("write report"))
	})

	t.Run("summary write failure on a failed action", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		recorder := &recordingDeploy{err: errors.New("apply failure")}

		command := NewDeployCommand(recorder.deploy, genericclioptions.NewConfigFlags(true))
		command.SetOut(&bytes.Buffer{})
		command.SetErr(failingWriter{})
		command.SetArgs(slicesClone(deployFlags))

		err := command.ExecuteContext(t.Context())

		g.Expect(exitCode(err)).To(Equal(ExitFailure))
		g.Expect(err.Error()).To(ContainSubstring("write failure summary"))
	})
}

// slicesClone returns one copy of the flag arguments, so executions
// never share or mutate the shared minimal flag slice.
func slicesClone(flags []string) []string {
	return append([]string(nil), flags...)
}

// recordingGetter counts configuration resolutions: the standard
// ConfigFlags delegate unchanged, with ToRESTConfig observed, so a
// test can prove no client construction happened.
type recordingGetter struct {
	*genericclioptions.ConfigFlags
	resolutions int
}

// ToRESTConfig records one resolution and delegates to the standard
// flags.
func (g *recordingGetter) ToRESTConfig() (*rest.Config, error) {
	g.resolutions++

	return g.ConfigFlags.ToRESTConfig()
}

// actionOptions returns settings valid for the concrete action: the
// definition file need not exist until a stage reads it, and the
// client arrives through the action's own connection.
func actionOptions() deployer.Options {
	return deployer.Options{
		DefinitionFile:  "/work/modules/synthetic/definition.yaml",
		PlatformVersion: "2.19.0",
		Namespace:       "run-namespace",
		Timeout:         5 * time.Minute,
	}
}

// TestActionRejectsBadInvocationsBeforeClientConstruction proves the
// concrete action rejects an already-cancelled caller and invalid
// settings before any client construction: the configuration is never
// resolved, no stage runs, and the result is zero.
func TestActionRejectsBadInvocationsBeforeClientConstruction(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		options func() deployer.Options
		ctx     func() context.Context
		check   func(g Gomega, err error)
	}{
		{
			name:    "cancelled caller",
			options: actionOptions,
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()

				return ctx
			},
			check: func(g Gomega, err error) {
				g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
			},
		},
		{
			name: "invalid settings",
			options: func() deployer.Options {
				options := actionOptions()
				options.FieldManager = "team/manager"

				return options
			},
			ctx: func() context.Context { return t.Context() },
			check: func(g Gomega, err error) {
				g.Expect(errors.Is(err, moduleerrors.ErrInvalidDeployOptions)).To(BeTrue())
				g.Expect(err.Error()).To(Equal(
					"invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters"))
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			getter := &recordingGetter{ConfigFlags: genericclioptions.NewConfigFlags(true)}

			result, err := Action(testCase.ctx(), getter, testCase.options())

			testCase.check(g, err)
			g.Expect(getter.resolutions).To(Equal(0),
				"no client may be constructed before the invocation checks")
			g.Expect(result).To(Equal(deployer.Result{}))
		})
	}
}

// TestActionConnectFailureIsTerminal proves a connection failure stops
// the action with the runtime-classified configuration error and a
// zero result: no stage ever runs.
func TestActionConnectFailureIsTerminal(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	kubeconfig := filepath.Join(t.TempDir(), "nonexistent-kubeconfig")
	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &kubeconfig

	getter := &recordingGetter{ConfigFlags: flags}

	result, err := Action(t.Context(), getter, actionOptions())

	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("resolving Kubernetes configuration"))
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDeployOptions)).To(BeFalse())

	g.Expect(getter.resolutions).To(Equal(1))
	g.Expect(result).To(Equal(deployer.Result{}))
}

// actionDefinitionYAML is a Helm-format module definition whose
// controller image is a literal: no image environment is needed, so
// the local stages of the deadline test run offline.
const actionDefinitionYAML = `apiVersion: config.opendatahub.io/v1alpha1
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
      name: synthetic-controller
      container:
        image: quay.io/example/controller:v1
`

// actionChartYAML is the chart descriptor of the action fixture.
const actionChartYAML = `apiVersion: v2
name: synthetic
version: 0.1.0
`

// actionTemplateYAML renders the controller Deployment the deadline
// test's apply would write.
const actionTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: synthetic-controller
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
        image: quay.io/example/controller:v1
`

// actionRoot writes the minimal literal-image Helm module the action
// can load and render offline, and returns its root directory.
func actionRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	files := map[string]string{
		"definition.yaml":                           actionDefinitionYAML,
		"manifests/chart/Chart.yaml":                actionChartYAML,
		"manifests/chart/templates/deployment.yaml": actionTemplateYAML,
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

// hangingGetter returns a getter whose configuration points at a
// server that accepts connections and never answers: every request
// hangs until its own deadline.
func hangingGetter(t *testing.T) *recordingGetter {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path
	serverURL := server.URL
	flags.APIServer = &serverURL

	return &recordingGetter{ConfigFlags: flags}
}

// TestActionInvocationDeadlineBoundsTheDeploy proves the options'
// overall timeout bounds the whole concrete invocation: against a
// server that never answers, the deploy fails with the deadline
// identity well inside the budget, after exactly one configuration
// resolution.
func TestActionInvocationDeadlineBoundsTheDeploy(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	root := actionRoot(t)

	options := actionOptions()
	options.DefinitionFile = filepath.Join(root, "definition.yaml")
	// The budget is generous for the real local stages yet far under
	// any default, so under load the deadline still lands in the
	// hanging apply, not in Load or Render.
	options.Timeout = 2 * time.Second

	getter := hangingGetter(t)

	start := time.Now()
	result, err := Action(t.Context(), getter, options)
	elapsed := time.Since(start)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(elapsed).To(BeNumerically("<", 30*time.Second))

	g.Expect(getter.resolutions).To(Equal(1))
	g.Expect(result).To(Equal(deployer.Result{
		Attempted: deployer.StageDeploy,
		Completed: deployer.StageRender,
	}))
}
