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

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	configcrd "github.com/lburgazzoli/odh-platform-tools/config/crd"
	k8scrd "github.com/lburgazzoli/odh-platform-tools/pkg/k8s/crd"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// prepareOwner is the single preparation step ahead of the payload
// apply, over the invocation's client: apply the embedded definition
// CRD, wait until the server serves it, then apply the definition
// itself and return the server-backed object. Both writes are plain
// create-or-update server-side applies through the framework helper —
// the same path whether the server holds the object or not, with no
// pre-write read and no local compatibility or live-state policy: the
// API validates the result and refuses what it must refuse. Both
// writes claim their fields under the invocation's effective field
// manager: one manager owns the authored spec and the whole authored
// labels and annotations maps, so a re-run replaces keys it no longer
// authors; live status and fields owned by other field managers are
// never claimed.
//
// The authored definition the receiver holds is never mutated: a deep
// copy is sanitized and submitted, and the copy — carrying the
// identity the apply response returned — becomes the confirmed owner.
func (r *Deployer) prepareOwner(ctx context.Context) (*toolApi.PlatformModuleDefinition, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(configcrd.PlatformModuleDefinitionYAML, crd); err != nil {
		return nil, prepareFail("decoding the embedded definition custom resource definition", err)
	}

	if err := fwresources.Apply(ctx, r.client, crd,
		crclient.FieldOwner(r.fieldManager), crclient.ForceOwnership); err != nil {
		return nil, prepareFail("applying the definition custom resource definition", err)
	}

	if err := k8scrd.WaitForCRDToBeEstablished(ctx, r.client, toolApi.CRDName); err != nil {
		return nil, prepareFail("waiting for the definition custom resource definition", err)
	}

	// Forged authority fields — UID, resourceVersion, namespace, owner
	// references, finalizers — never reach the apply request: the
	// preparation claims only the authored name, labels, annotations,
	// and spec, over a copy of the authored definition. The
	// server-side merge preserves the live identity and status, so
	// re-runs reuse identities without any lock.
	desired := r.definition.DeepCopy()
	desired.ObjectMeta = metav1.ObjectMeta{
		Name:        desired.Name,
		Labels:      desired.Labels,
		Annotations: desired.Annotations,
	}

	if err := fwresources.Apply(ctx, r.client, desired,
		crclient.FieldOwner(r.fieldManager), crclient.ForceOwnership); err != nil {
		return nil, prepareFail("applying the module definition", err)
	}

	return desired, nil
}

// prepareFail wraps one preparation failure in the safe shared
// moduleerrors.ErrOwnerPreparation error: its text names the failed
// operation only, never echoing server-returned or authored values;
// the raw cause stays reachable through Unwrap for errors.Is and
// errors.As.
func prepareFail(operation string, cause error) error {
	return moduleerrors.New(
		moduleerrors.ErrOwnerPreparation,
		operation+" failed",
		moduleerrors.WithCause(cause),
	)
}
