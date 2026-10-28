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

// publishDefinition applies the embedded CRD, waits for establishment, and
// applies a sanitized definition copy. The returned object carries the live
// identity used to own payload resources.
func (r *Deployer) publishDefinition(ctx context.Context) (*toolApi.PlatformModuleDefinition, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(configcrd.PlatformModuleDefinitionYAML, crd); err != nil {
		return nil, moduleerrors.New(
			moduleerrors.ErrOwnerPreparation,
			"decoding the embedded definition custom resource definition failed",
			moduleerrors.WithCause(err),
		)
	}

	if err := fwresources.Apply(ctx, r.client, crd,
		crclient.FieldOwner(r.options.FieldManager), crclient.ForceOwnership); err != nil {
		return nil, moduleerrors.New(
			moduleerrors.ErrOwnerPreparation,
			safeFailureMessage("applying the definition custom resource definition failed", err),
			moduleerrors.WithCause(err),
		)
	}

	if err := k8scrd.WaitForCRDToBeEstablished(ctx, r.client, toolApi.CRDName); err != nil {
		return nil, moduleerrors.New(
			moduleerrors.ErrOwnerPreparation,
			safeFailureMessage("waiting for the definition custom resource definition failed", err),
			moduleerrors.WithCause(err),
		)
	}

	// Keep authored name, labels, annotations, and spec; discard client-supplied
	// identity and lifecycle metadata.
	desired := r.definition.DeepCopy()
	desired.ObjectMeta = metav1.ObjectMeta{
		Name:        desired.Name,
		Labels:      desired.Labels,
		Annotations: desired.Annotations,
	}

	if err := fwresources.Apply(ctx, r.client, desired,
		crclient.FieldOwner(r.options.FieldManager), crclient.ForceOwnership); err != nil {
		return nil, moduleerrors.New(
			moduleerrors.ErrOwnerPreparation,
			safeFailureMessage("applying the module definition failed", err),
			moduleerrors.WithCause(err),
		)
	}

	return desired, nil
}
