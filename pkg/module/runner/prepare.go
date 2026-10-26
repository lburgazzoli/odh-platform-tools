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

package runner

import (
	"context"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	configcrd "github.com/lburgazzoli/odh-platform-tools/config/crd"
	k8scrd "github.com/lburgazzoli/odh-platform-tools/pkg/k8s/crd"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
)

// fieldOwner is the server-side-apply field manager the preparation
// writes under — the embedded definition CRD and the definition itself
// alike. One manager owns the authored spec and the whole authored
// labels and annotations maps, so a re-run replaces keys it no longer
// authors; live status and fields owned by other field managers are
// never claimed.
const fieldOwner = "platform.opendatahub.io/module-run"

// prepareOwner is the single preparation step ahead of the payload
// apply: apply the embedded definition CRD, wait until the server
// serves it, then apply the definition itself and return the
// server-backed object. Both writes are plain create-or-update
// server-side applies through the framework helper — the same path
// whether the server holds the object or not, with no pre-write read
// and no local compatibility or live-state policy: the API validates
// the result and refuses what it must refuse.
func prepareOwner(
	ctx context.Context,
	connection *cluster.Connection,
	definition v1alpha1.PlatformModuleDefinition,
) (*v1alpha1.PlatformModuleDefinition, error) {
	client := connection.Client()

	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(configcrd.PlatformModuleDefinitionYAML, crd); err != nil {
		return nil, prepareFail("decoding the embedded definition custom resource definition", err)
	}

	if err := fwresources.Apply(ctx, client, crd,
		crclient.FieldOwner(fieldOwner), crclient.ForceOwnership); err != nil {
		return nil, prepareFail("applying the definition custom resource definition", err)
	}

	if err := k8scrd.WaitForCRDToBeEstablished(ctx, client, v1alpha1.CRDName); err != nil {
		return nil, prepareFail("waiting for the definition custom resource definition", err)
	}

	// Forged authority fields — UID, resourceVersion, namespace, owner
	// references, finalizers — never reach the apply request: the
	// preparation claims only the authored name, labels, annotations,
	// and spec. The server-side merge preserves the live identity and
	// status, so re-runs reuse identities without any lock.
	definition.ObjectMeta = metav1.ObjectMeta{
		Name:        definition.Name,
		Labels:      definition.Labels,
		Annotations: definition.Annotations,
	}
	desired := &definition

	if err := fwresources.Apply(ctx, client, desired,
		crclient.FieldOwner(fieldOwner), crclient.ForceOwnership); err != nil {
		return nil, prepareFail("applying the module definition", err)
	}

	return desired, nil
}

// prepareError is the safe error the preparation returns: its text
// names the failed operation only, never echoing server-returned or
// authored values; the raw cause stays reachable through Unwrap for
// errors.Is and errors.As.
type prepareError struct {
	message string
	cause   error
}

// Error returns the safe message.
func (e *prepareError) Error() string {
	return e.message
}

// Unwrap returns the raw cause, preserving error identities for
// errors.Is and errors.As.
func (e *prepareError) Unwrap() error {
	return e.cause
}

// prepareFail wraps one preparation failure in the safe error.
func prepareFail(operation string, cause error) error {
	return &prepareError{
		message: fmt.Sprintf(
			"%s failed; the raw cause remains reachable through errors.Is and errors.As",
			operation),
		cause: cause,
	}
}
