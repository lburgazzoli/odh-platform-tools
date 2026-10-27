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
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"
	fwdeploy "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/deploy"
	odhTypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/adapt"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/cluster"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// errNilArgument marks a nil payload-apply argument; it carries identity
// through the shared error's cause chain for errors.Is and its text
// never echoes API-returned values.
var errNilArgument = errors.New("nil deploy argument")

// Fixed safe operation summaries, one per phase. They intentionally
// omit server-returned detail, which stays reachable through the
// cause chain: failures before the action runs are classified by
// moduleerrors.ErrDeployPrepare, action failures by
// moduleerrors.ErrDeployApply.
const (
	prepareFailureMessage = "preparing the module deployment failed; " +
		"the raw causes remain reachable through errors.Is and errors.As"
	applyFailureMessage = "applying the module resources failed; " +
		"the raw causes remain reachable through errors.Is and errors.As"
)

// DeployPayload is the concrete ApplyPayload seam: it installs the
// rendered module resources through the actual framework deploy
// action, with the persisted definition as the live owner, under the
// given field manager. It never publishes an owner itself and never
// prunes.
//
// The flow is minimal: normalize the persisted owner's runtime type,
// then invoke the action once with the whole payload. The action's
// default SortByApplyOrder — the same engine apply-order post-renderer
// the shared render pipeline uses — places Namespaces and CRDs first
// and routes CRDs through its unowned CRD path, so no payload
// partition or CRD establishment wait exists here: a bundled custom
// resource whose CRD was just created may fail on the first run and
// succeed on a re-run, exactly as the operator behaves. CRD
// establishment waiting stays only in the deployer's definition-CRD
// preparation. Rendered Namespaces follow normal framework create/update
// behavior and stay unowned; a payload referencing an API the server
// does not serve fails through the action's own error after earlier
// writes, reported as partial state, and there is no rollback.
//
// fieldManager is the invocation's effective server-side-apply field
// manager, already resolved and validated by the caller: the
// production caller passes Options.FieldManager defaulted to
// DefaultFieldManager, and this service adds no defaulting of its
// own. It reaches the framework through WithFieldOwner, so it covers
// the ordinary payload apply — including the manager-derived fallback
// part-of label on objects that author none and the framework's
// deploy metric label — while bundled payload CRDs keep the
// framework's own fixed manager and fallback label. An empty argument
// is rejected here as a safe preparation failure before any client
// access, never silently reverting to the framework's owner-kind
// default.
//
// The framework's continue-on-error first report is the stage's error
// contract, exactly as in the operator, and its force-SSA adoption
// semantics govern foreign-owned objects. The returned error is a
// safe shared module error: its category names the phase —
// moduleerrors.ErrDeployPrepare before the action runs,
// moduleerrors.ErrDeployApply for the action itself — and the
// framework's raw report stays reachable through Unwrap for
// errors.Is and errors.As.
func DeployPayload(
	ctx context.Context,
	connection *cluster.Connection,
	owner *toolApi.PlatformModuleDefinition,
	release api.Release,
	resources []unstructured.Unstructured,
	fieldManager string,
) error {
	if err := ctx.Err(); err != nil {
		return moduleerrors.New(moduleerrors.ErrDeployPrepare,
			prepareFailureMessage, moduleerrors.WithCause(err))
	}

	if connection == nil || owner == nil {
		return moduleerrors.New(moduleerrors.ErrDeployPrepare,
			prepareFailureMessage,
			moduleerrors.WithCause(fmt.Errorf("%w: connection and owner must not be nil", errNilArgument)))
	}

	// The service receives its configuration prevalidated; an empty
	// manager is a caller defect, never a request for the framework's
	// own default. It fails before any client access.
	if fieldManager == "" {
		return moduleerrors.New(moduleerrors.ErrDeployPrepare,
			prepareFailureMessage,
			moduleerrors.WithCause(fmt.Errorf("%w: field manager must not be empty", errNilArgument)))
	}

	client := connection.Client()

	// The persisted owner's runtime GVK is normalized from the
	// connection scheme before the action reads the instance: scheme
	// registration alone does not populate TypeMeta.
	if err := fwresources.EnsureGroupVersionKind(client.Scheme(), owner); err != nil {
		return moduleerrors.New(moduleerrors.ErrDeployPrepare,
			prepareFailureMessage, moduleerrors.WithCause(err))
	}

	// The plain connection client goes to the action: the framework's
	// own ownership, unmanaged, and adoption semantics apply
	// unchanged, matching the operator.
	bridge := adapt.NewControllerBridge(client)

	// One invocation-local action carries the whole payload. Defaults
	// keep the framework field ownership, metadata, customizers, SSA
	// mode, the default apply-order sort (Namespaces and CRDs first),
	// and the CRD field owner with ForceOwnership; WithFieldOwner
	// names this invocation's manager for the ordinary payload apply
	// and its fallback part-of label, leaving the bundled-CRD path on
	// the framework's fixed manager.
	action := fwdeploy.NewAction(
		fwdeploy.WithCache(),
		fwdeploy.WithContinueOnError(),
		fwdeploy.WithFieldOwner(fieldManager),
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
		return moduleerrors.New(moduleerrors.ErrDeployApply,
			applyFailureMessage, moduleerrors.WithCause(err))
	}

	return nil
}
