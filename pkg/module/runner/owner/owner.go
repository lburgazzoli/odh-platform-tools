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
// then publish the definition by authored metadata.name through one
// server-side apply behind an explicit field owner.
//
// The published spec is exactly what the loader produced: authored
// Helm values and placeholders stay literal, and placement defaults
// are already applied. File-supplied authority fields — UID,
// resourceVersion, namespace, owner references, finalizers — are
// never submitted, and no status is sent: the apply claims only the
// authored name, labels, annotations, and spec. The server-side
// merge preserves the live identity and status, so re-runs reuse
// identities — including previously unbound definitions — without
// any namespace binding or lock. Authored labels and annotations are
// the whole desired state under server-side-apply atomic maps: keys
// a re-run no longer authors are replaced.
//
// Publication is not transactional and failure retains state. The
// returned owner is the best-known server-backed identity even when
// an error accompanies it, and the typed error's outcome separates a
// refused publication (rejected) from an unreported one (unknown): a
// nil owner never proves that no write occurred. Ambiguous applies
// are never retried as new identities, and no UID is fabricated.
package owner

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	jsonv2 "encoding/json/v2"

	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
)

// fieldOwner is the server-side-apply field manager the owner
// publication writes under. One manager owns the authored spec and
// the whole authored labels and annotations maps, so a re-run
// replaces keys it no longer authors; live status and fields owned
// by other field managers are never claimed.
const fieldOwner = "platform.opendatahub.io/module-run"

// Definition publication refusal causes. They stay reachable through
// the typed error's Unwrap chain for callers that classify rejections
// beyond the publication outcome.
var (
	errUnsupportedEnvelope = errors.New("unsupported definition envelope")
	errDefinitionDeletion  = errors.New("definition owner is being deleted")
	errDefinitionForeign   = errors.New("definition owner carries foreign owner references")
	errDefinitionNoUID     = errors.New("definition owner has no UID")
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

// publish applies the definition owner through one server-side apply
// patch. A pre-write read confirms the live identity — when the owner
// already exists — and rejects one this run must not overwrite; a
// missing owner is the create case the apply itself covers. Every
// failure keeps its classification: definitive server refusals are
// rejected, unreported request outcomes are unknown, and only a
// successful apply confirms the desired spec.
func publish(
	ctx context.Context,
	connection *cluster.Connection,
	definition v1alpha1.PlatformModuleDefinition,
) (*v1alpha1.PlatformModuleDefinition, error) {
	client := connection.Client()
	desired := publicationPayload(definition)

	live := &v1alpha1.PlatformModuleDefinition{}
	err := client.Get(ctx, crclient.ObjectKey{Name: desired.Name}, live)
	switch {
	case apierrors.IsNotFound(err):
		// The owner is absent: the apply itself is the create.
		live = nil
	case err != nil:
		return nil, publicationError(nil, "reading the definition owner", err)
	default:
		if err := checkLive(live); err != nil {
			return live, newError(OutcomeRejected, normalize(connection, live), "checking the definition owner", "", err)
		}
	}

	payload, err := applyPayload(desired)
	if err != nil {
		return nil, newError(OutcomeNotAttempted, nil, "building the definition owner apply request", "", err)
	}

	if err := client.Apply(
		ctx,
		crclient.ApplyConfigurationFromUnstructured(payload),
		crclient.FieldOwner(fieldOwner),
		crclient.ForceOwnership,
	); err != nil {
		owner := live
		if owner != nil {
			owner = normalize(connection, owner)
		}

		return owner, publicationError(owner, "applying the definition owner", err)
	}

	applied, err := decodeApplied(payload)
	if err != nil {
		return nil, newError(OutcomeUnknown, nil, "decoding the applied definition owner", "", err)
	}

	return confirmed(connection, applied)
}

// publicationPayload rebuilds the publishable definition from the
// authored one: the normalized spec plus exactly the authored name,
// labels, and annotations. Forged authority fields — UID,
// resourceVersion, namespace, owner references, finalizers — never
// reach an apply request.
func publicationPayload(definition v1alpha1.PlatformModuleDefinition) *v1alpha1.PlatformModuleDefinition {
	desired := definition.DeepCopy()
	desired.ObjectMeta = metav1.ObjectMeta{
		Name:        desired.Name,
		Labels:      desired.Labels,
		Annotations: desired.Annotations,
	}

	return desired
}

// applyPayload builds the server-side-apply request body: exactly the
// definition envelope, the authored name, labels, and annotations,
// and the normalized unexpanded spec. No UID, resourceVersion,
// finalizers, namespace, or status is sent, so the apply claims only
// the authored fields. The spec crosses into the unstructured body
// through a JSON round trip, preserving literal Helm values and null
// spellings exactly as authored.
func applyPayload(desired *v1alpha1.PlatformModuleDefinition) (*unstructured.Unstructured, error) {
	encoded, err := jsonv2.Marshal(desired.Spec)
	if err != nil {
		return nil, fmt.Errorf("encoding the definition spec: %w", err)
	}

	spec := map[string]any{}
	if err := jsonv2.Unmarshal(encoded, &spec); err != nil {
		return nil, fmt.Errorf("decoding the definition spec: %w", err)
	}

	metadata := map[string]any{"name": desired.Name}
	if len(desired.Labels) > 0 {
		labels := make(map[string]any, len(desired.Labels))
		for key, value := range desired.Labels {
			labels[key] = value
		}
		metadata["labels"] = labels
	}
	if len(desired.Annotations) > 0 {
		annotations := make(map[string]any, len(desired.Annotations))
		for key, value := range desired.Annotations {
			annotations[key] = value
		}
		metadata["annotations"] = annotations
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": v1alpha1.APIVersion,
		"kind":       v1alpha1.Kind,
		"metadata":   metadata,
		"spec":       spec,
	}}, nil
}

// decodeApplied decodes the server's apply response — the full live
// object, decoded into the unstructured payload — into the typed
// owner. The JSON round trip preserves literal Helm values and null
// spellings a struct converter cannot express.
func decodeApplied(payload *unstructured.Unstructured) (*v1alpha1.PlatformModuleDefinition, error) {
	encoded, err := jsonv2.Marshal(payload.Object)
	if err != nil {
		return nil, fmt.Errorf("encoding the applied definition owner: %w", err)
	}

	applied := &v1alpha1.PlatformModuleDefinition{}
	if err := jsonv2.Unmarshal(encoded, applied); err != nil {
		return nil, fmt.Errorf("decoding the applied definition owner: %w", err)
	}

	return applied, nil
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

// confirmed normalizes the runtime GVK of an applied definition
// owner and returns it. The publication is confirmed by the apply
// response, so a normalization failure — unreachable for a
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
