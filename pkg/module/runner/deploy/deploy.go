package deploy

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"
	fwdeploy "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/deploy"
	odhTypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
)

// Apply installs the rendered module resources through the actual
// framework deploy action, with the persisted definition as the live
// owner. It never publishes an owner itself and never prunes.
//
// The flow is minimal: normalize the persisted owner's runtime type,
// then invoke the action once with the whole payload. The action's
// default SortByApplyOrder — the same engine apply-order post-renderer
// the shared render pipeline uses — places Namespaces and CRDs first
// and routes CRDs through its unowned CRD path, so no payload
// partition or CRD establishment wait exists here: a bundled custom
// resource whose CRD was just created may fail on the first run and
// succeed on a re-run, exactly as the operator behaves. CRD
// establishment waiting stays only in owner.Ensure's definition-CRD
// bootstrap. Rendered Namespaces follow normal framework create/update
// behavior and stay unowned; a payload referencing an API the server
// does not serve fails through the action's own error after earlier
// writes, reported as partial state, and there is no rollback.
//
// The framework's continue-on-error first report is the stage's error
// contract, exactly as in the operator, and its force-SSA adoption
// semantics govern foreign-owned objects. The returned error is a
// safe typed *Error: its text names the stage, and the framework's
// raw report stays reachable through Unwrap for errors.Is and
// errors.As.
func Apply(
	ctx context.Context,
	connection *cluster.Connection,
	owner *v1alpha1.PlatformModuleDefinition,
	release api.Release,
	resources []unstructured.Unstructured,
) error {
	if err := ctx.Err(); err != nil {
		return &Error{Stage: StagePrepare, Message: stageMessages[StagePrepare], cause: err}
	}

	if connection == nil || owner == nil {
		return &Error{
			Stage:   StagePrepare,
			Message: stageMessages[StagePrepare],
			cause:   fmt.Errorf("%w: connection and owner must not be nil", errNilArgument),
		}
	}

	client := connection.Client()

	// The persisted owner's runtime GVK is normalized from the
	// connection scheme before the action reads the instance: scheme
	// registration alone does not populate TypeMeta.
	if err := fwresources.EnsureGroupVersionKind(client.Scheme(), owner); err != nil {
		return &Error{Stage: StagePrepare, Message: stageMessages[StagePrepare], cause: err}
	}

	// The plain connection client goes to the action: the framework's
	// own ownership, unmanaged, and adoption semantics apply
	// unchanged, matching the operator.
	bridge := &controllerBridge{
		client:       client,
		dynamicOwned: map[schema.GroupVersionKind]struct{}{},
	}

	// One invocation-local action carries the whole payload. Defaults
	// keep the framework field ownership, metadata, customizers, SSA
	// mode, the default apply-order sort (Namespaces and CRDs first),
	// and the CRD field owner with ForceOwnership.
	action := fwdeploy.NewAction(
		fwdeploy.WithCache(),
		fwdeploy.WithContinueOnError(),
	)

	// The action's own logs can echo server-returned error text, so
	// it runs under a discarded logger.
	quietCtx := logf.IntoContext(ctx, logr.Discard())

	// The single action invocation: the plain connection client, the
	// controller bridge, the persisted definition as the live
	// instance, the runtime release, and the whole rendered payload.
	// Conditions stay unset and SkipDeploy stays false.
	if err := action(quietCtx, &odhTypes.ReconciliationRequest{
		Client:     client,
		Controller: bridge,
		Instance:   owner,
		Release:    release,
		Resources:  resources,
	}); err != nil {
		return &Error{Stage: StageApply, Message: stageMessages[StageApply], cause: err}
	}

	return nil
}
