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
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	fwdeploy "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/deploy"
	odhTypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/adapt"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/retry"
)

var errNilArgument = errors.New("nil deploy argument")

const (
	prepareFailureMessage = "preparing the module deployment failed"
	applyFailureMessage   = "applying the module resources failed"
)

func defaultPayloadRetryOptions() retry.Options {
	return retry.Options{
		MaxAttempts: 5,
		Backoff: retry.Backoff{
			InitialDelay: time.Second,
			Multiplier:   2,
			MaxDelay:     8 * time.Second,
		},
		Retriable: isRetriablePayloadError,
	}
}

// The framework exposes only its first failure as a cause. Later permanent
// failures cannot veto retries selected by an earlier transient error.
func isRetriablePayloadError(err error) bool {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case meta.IsNoMatchError(err),
		apierrors.IsTimeout(err),
		apierrors.IsServerTimeout(err),
		apierrors.IsTooManyRequests(err),
		apierrors.IsServiceUnavailable(err),
		apierrors.IsInternalError(err):
		return true
	default:
		return false
	}
}

// applyResources retries the whole framework action without a deploy cache.
// Namespaces and CRDs remain unowned; bundled CRDs keep the framework's field
// manager. Successful writes survive later failures and are reapplied on retry.
func (r *Deployer) applyResources(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return moduleerrors.New(
			moduleerrors.ErrDeployPrepare,
			prepareFailureMessage,
			moduleerrors.WithCause(err),
		)
	}

	if r.client == nil || r.owner == nil {
		return moduleerrors.New(
			moduleerrors.ErrDeployPrepare,
			prepareFailureMessage,
			moduleerrors.WithCause(fmt.Errorf("%w: client and owner must not be nil", errNilArgument)),
		)
	}

	if r.options.FieldManager == "" {
		return moduleerrors.New(
			moduleerrors.ErrDeployPrepare,
			prepareFailureMessage,
			moduleerrors.WithCause(fmt.Errorf("%w: field manager must not be empty", errNilArgument)),
		)
	}

	// Scheme registration alone does not populate TypeMeta.
	if err := fwresources.EnsureGroupVersionKind(r.client.Scheme(), r.owner); err != nil {
		return moduleerrors.New(
			moduleerrors.ErrDeployPrepare,
			prepareFailureMessage,
			moduleerrors.WithCause(err),
		)
	}

	bridge := adapt.NewControllerBridge(r.client)

	err := retry.OnError(
		ctx,
		func(attemptCtx context.Context) error {
			action := fwdeploy.NewAction(
				fwdeploy.WithContinueOnError(),
				fwdeploy.WithFieldOwner(r.options.FieldManager),
			)

			// Suppress framework logs that may contain raw server error text.
			quietCtx := logf.IntoContext(attemptCtx, logr.Discard())

			return action(quietCtx, &odhTypes.ReconciliationRequest{
				Client:     r.client,
				Controller: bridge,
				Instance:   r.owner,
				Release:    r.release,
				Resources:  r.resources,
			})
		},
		r.payloadRetryOptions,
	)
	if err != nil {
		return moduleerrors.New(
			moduleerrors.ErrDeployApply,
			safeFailureMessage(applyFailureMessage, err),
			moduleerrors.WithCause(err),
		)
	}

	return nil
}
