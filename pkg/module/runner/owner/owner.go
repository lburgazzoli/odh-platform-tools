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

// Package owner persists the definition owner: it bootstraps the
// embedded definition custom resource definition and publishes the
// normalized, unexpanded module definition behind a real server
// identity.
//
// Ensure is the whole surface. It runs inside a deploy stage, on a
// live cluster.Connection, and composes three steps: reject an
// unsupported or missing definition envelope before any write, create
// or compatibly reuse the definition CRD and wait for it to serve,
// then create, read, or conflict-retried-update the definition by
// authored metadata.name.
//
// The published spec is exactly what the loader produced: authored
// Helm values and placeholders stay literal, and placement defaults
// are already applied. File-supplied authority fields — UID,
// resourceVersion, namespace, owner references, finalizers — are
// never submitted; the published object carries the authored name,
// labels, and annotations only. Updates preserve the live UID,
// resourceVersion, status, and unrelated metadata, so re-runs reuse
// identities — including previously unbound definitions — without any
// namespace binding or lock.
//
// Publication is not transactional and failure retains state. The
// returned owner is the best-known server-backed identity even when
// an error accompanies it, and the typed error's outcome separates a
// refused publication (rejected) from an unreported one (unknown): a
// nil owner never proves that no write occurred. Ambiguous creates
// are never retried as new identities, and no UID is fabricated.
package owner

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
)

// maxPublishAttempts bounds create/update conflict retries: a
// competing writer wins the publication only by outlasting this many
// attempts.
const maxPublishAttempts = 3

// Definition publication refusal causes. They stay reachable through
// the typed error's Unwrap chain for callers that classify rejections
// beyond the publication outcome.
var (
	errUnsupportedEnvelope = errors.New("unsupported definition envelope")
	errDefinitionDeletion  = errors.New("definition owner is being deleted")
	errDefinitionForeign   = errors.New("definition owner carries foreign owner references")
	errDefinitionNoUID     = errors.New("definition owner has no UID")
	errConflictExhausted   = errors.New("definition owner update conflicts exhausted")
)

// Ensure bootstraps the definition CRD and publishes the normalized
// definition, returning the server-backed owner. The returned owner
// is non-nil whenever a server-backed identity was confirmed — on
// success and on error alike; a nil owner means the identity is
// unconfirmed, not that no write occurred.
//
// On error the cause chain preserves API error, conflict, and
// cancellation identities, and the typed *Error carries the
// publication outcome and any confirmed owner.
func Ensure(
	ctx context.Context,
	connection *cluster.Connection,
	definition v1alpha1.PlatformModuleDefinition,
) (*v1alpha1.PlatformModuleDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, newError(OutcomeNotAttempted, nil, "checking the invocation context", "", err)
	}

	if definition.APIVersion != v1alpha1.APIVersion || definition.Kind != v1alpha1.Kind {
		cause := fmt.Errorf("%w: apiVersion %q, kind %q", errUnsupportedEnvelope, definition.APIVersion, definition.Kind)
		return nil, newError(OutcomeNotAttempted, nil, "checking the definition envelope", "", cause)
	}

	if err := ensureCRD(ctx, connection); err != nil {
		return nil, newError(OutcomeNotAttempted, nil, "bootstrapping the definition custom resource definition", "", err)
	}

	if err := connection.WaitCRDs(ctx, v1alpha1.CRDName); err != nil {
		return nil, newError(OutcomeNotAttempted, nil, "waiting for the definition custom resource definition", "", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, newError(OutcomeNotAttempted, nil, "checking the invocation context", "", err)
	}

	return publish(ctx, connection, definition)
}

// publish creates, reads, or updates the definition owner by authored
// metadata.name. It classifies every failure: definitive server
// refusals are rejected, unreported request outcomes are unknown, and
// only a successful write or an already-matching read confirms the
// desired spec.
func publish(
	ctx context.Context,
	connection *cluster.Connection,
	definition v1alpha1.PlatformModuleDefinition,
) (*v1alpha1.PlatformModuleDefinition, error) {
	client := connection.Client()
	desired := publicationPayload(definition)

	for attempt := 1; attempt <= maxPublishAttempts; attempt++ {
		created := desired.DeepCopy()
		if err := client.Create(ctx, created); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return nil, publicationError(nil, "creating the definition owner", err)
			}
		} else {
			return confirmed(connection, created)
		}

		live := &v1alpha1.PlatformModuleDefinition{}
		if err := client.Get(ctx, crclient.ObjectKey{Name: desired.Name}, live); err != nil {
			if apierrors.IsNotFound(err) && attempt < maxPublishAttempts {
				// The owner existed at creation time and vanished
				// before the read: race a fresh creation.
				continue
			}

			return nil, publicationError(nil, "reading the definition owner", err)
		}

		if err := checkLive(live); err != nil {
			return live, newError(OutcomeRejected, normalize(connection, live), "checking the definition owner", "", err)
		}

		if alreadyMatching(desired, live) {
			return confirmed(connection, live)
		}

		updated := mergedUpdate(live, desired)
		if err := client.Update(ctx, updated); err != nil {
			if apierrors.IsConflict(err) && attempt < maxPublishAttempts {
				continue
			}

			owner := normalize(connection, live)
			if apierrors.IsConflict(err) {
				err = fmt.Errorf("%w: %w", errConflictExhausted, err)
			}

			return owner, publicationError(owner, "updating the definition owner", err)
		}

		if err := ensureGVK(connection, updated); err != nil {
			return updated, newError(OutcomeConfirmed, updated, "normalizing the owner group, version, and kind", "", err)
		}

		return updated, nil
	}

	return nil, newError(
		OutcomeRejected,
		nil,
		"updating the definition owner",
		"",
		fmt.Errorf("%w after %d attempts", errConflictExhausted, maxPublishAttempts),
	)
}

// publicationPayload rebuilds the creatable definition from the
// authored one: the normalized spec plus exactly the authored name,
// labels, and annotations. Forged authority fields — UID,
// resourceVersion, namespace, owner references, finalizers — never
// reach a create or update request.
func publicationPayload(definition v1alpha1.PlatformModuleDefinition) *v1alpha1.PlatformModuleDefinition {
	desired := definition.DeepCopy()
	desired.ObjectMeta = metav1.ObjectMeta{
		Name:        desired.Name,
		Labels:      desired.Labels,
		Annotations: desired.Annotations,
	}

	return desired
}

// checkLive rejects a live definition this run must not adopt or
// overwrite: one with no server identity, one being deleted, or one
// carrying foreign owner references.
func checkLive(live *v1alpha1.PlatformModuleDefinition) error {
	if live.UID == "" {
		return fmt.Errorf("%w: %s", errDefinitionNoUID, live.Name)
	}

	if live.GetDeletionTimestamp() != nil {
		return fmt.Errorf("%w: %s", errDefinitionDeletion, live.Name)
	}

	if len(live.OwnerReferences) > 0 {
		return fmt.Errorf("%w: %s", errDefinitionForeign, live.Name)
	}

	return nil
}

// alreadyMatching reports whether the live definition already
// publishes the desired spec: an equal spec plus every authored label
// and annotation present with the authored value. Unrelated live
// metadata does not block reuse.
func alreadyMatching(desired, live *v1alpha1.PlatformModuleDefinition) bool {
	if !specsEqual(desired.Spec, live.Spec) {
		return false
	}

	return metadataMatching(desired.Labels, live.Labels) &&
		metadataMatching(desired.Annotations, live.Annotations)
}

// specsEqual compares two specs semantically. The one normalization
// is the null representation: an authored literal null Helm value
// carries raw null bytes, while the same value read back from the
// server has empty raw bytes — the apiextensions JSON type decodes
// null as unset. Both spellings mean null and compare equal.
func specsEqual(desired, live v1alpha1.PlatformModuleDefinitionSpec) bool {
	desiredCopy := desired.DeepCopy()
	liveCopy := live.DeepCopy()

	normalizeNullValues(desiredCopy)
	normalizeNullValues(liveCopy)

	return reflect.DeepEqual(*desiredCopy, *liveCopy)
}

// normalizeNullValues rewrites empty raw Helm value bytes to the null
// spelling in place.
func normalizeNullValues(spec *v1alpha1.PlatformModuleDefinitionSpec) {
	if spec.Manifests.Helm == nil {
		return
	}

	for key, value := range spec.Manifests.Helm.Values {
		if len(value.Raw) == 0 {
			value.Raw = []byte("null")
			spec.Manifests.Helm.Values[key] = value
		}
	}
}

// metadataMatching reports whether every desired entry is present
// with the same value; the live map may carry unrelated entries.
func metadataMatching(desired, live map[string]string) bool {
	for key, value := range desired {
		if live[key] != value {
			return false
		}
	}

	return true
}

// mergedUpdate builds the update payload from the live definition:
// the live identity, status, and unrelated metadata with the desired
// spec overlaid and the authored labels and annotations applied over
// the live ones.
func mergedUpdate(live, desired *v1alpha1.PlatformModuleDefinition) *v1alpha1.PlatformModuleDefinition {
	updated := live.DeepCopy()
	updated.Spec = desired.Spec
	updated.TypeMeta = metav1.TypeMeta{}

	if len(desired.Labels) > 0 {
		if updated.Labels == nil {
			updated.Labels = map[string]string{}
		}
		maps.Copy(updated.Labels, desired.Labels)
	}

	if len(desired.Annotations) > 0 {
		if updated.Annotations == nil {
			updated.Annotations = map[string]string{}
		}
		maps.Copy(updated.Annotations, desired.Annotations)
	}

	return updated
}

// confirmed normalizes the runtime GVK of an already-matching live
// definition owner and returns it. The publication is confirmed by
// the matching read, so a normalization failure — unreachable for a
// scheme-registered type — still returns the owner with a
// confirmed-outcome typed error rather than discarding it.
func confirmed(
	connection *cluster.Connection,
	owner *v1alpha1.PlatformModuleDefinition,
) (*v1alpha1.PlatformModuleDefinition, error) {
	if err := ensureGVK(connection, owner); err != nil {
		return owner, newError(OutcomeConfirmed, owner, "normalizing the owner group, version, and kind", "", err)
	}

	return owner, nil
}

// ensureGVK initializes the owner's runtime GVK through the framework
// helper, so scheme registration alone never leaves a typed object
// without its group, version, and kind. If resolution fails, the
// owner keeps the statically known envelope.
func ensureGVK(connection *cluster.Connection, owner *v1alpha1.PlatformModuleDefinition) error {
	if err := fwresources.EnsureGroupVersionKind(connection.Client().Scheme(), owner); err != nil {
		owner.TypeMeta = metav1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.Kind}

		return err
	}

	return nil
}

// normalize is the best-effort GVK initialization for owners carried
// on error paths: the publication error dominates, and the owner
// still leaves with a usable runtime GVK.
func normalize(connection *cluster.Connection, owner *v1alpha1.PlatformModuleDefinition) *v1alpha1.PlatformModuleDefinition {
	_ = ensureGVK(connection, owner)

	return owner
}

// publicationError classifies a failed publication request. A request
// the server answered is definitive — rejected; a request whose
// outcome went unreported — a lost response or a cancellation — is
// unknown, never an inference that no write occurred.
func publicationError(
	owner *v1alpha1.PlatformModuleDefinition,
	operation string,
	cause error,
) *Error {
	if isLostResponse(cause) {
		return newError(OutcomeUnknown, owner, operation, "the request outcome was not reported", cause)
	}

	return newError(OutcomeRejected, owner, operation, "", cause)
}

// isLostResponse reports whether a request error carries no server
// answer: a transport failure, a cancellation, or a deadline. Server
// answers — including every Kubernetes status error — are definitive.
func isLostResponse(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var status *apierrors.StatusError
	return !errors.As(err, &status)
}
