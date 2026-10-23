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

package owner

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	configcrd "github.com/lburgazzoli/odh-platform-tools/config/crd"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
)

// CRD bootstrap refusal causes. They stay reachable through the typed
// error's Unwrap chain for callers that classify rejections beyond
// the publication outcome.
var (
	errCRDTerminating     = errors.New("definition custom resource definition is terminating")
	errCRDOwnerReferenced = errors.New("definition custom resource definition is owner-referenced")
	errCRDIncompatible    = errors.New("definition custom resource definition is incompatible")
)

// ensureCRD bootstraps the persisted definition API: it decodes the
// embedded generated CRD and creates it only when the server has
// none. A CRD that already exists is never rewritten — not even to
// refresh documentation — and is reused only when its live spec is
// semantically compatible with the generated one. A creation raced by
// another writer falls back to reading and checking the winner.
func ensureCRD(ctx context.Context, connection *cluster.Connection) error {
	desired, err := decodeEmbeddedCRD()
	if err != nil {
		return err
	}

	cli := connection.Client()

	live := &apiextensionsv1.CustomResourceDefinition{}
	if err := cli.Get(ctx, crclient.ObjectKey{Name: v1alpha1.CRDName}, live); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		if err := cli.Create(ctx, desired); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return err
			}

			// Another writer created it first: check theirs.
			if err := cli.Get(ctx, crclient.ObjectKey{Name: v1alpha1.CRDName}, live); err != nil {
				return err
			}
		} else {
			return nil
		}
	}

	return checkCRD(live, desired)
}

// decodeEmbeddedCRD decodes the generated definition CRD embedded in
// the binary, so bootstrapping needs no source checkout.
func decodeEmbeddedCRD() (*apiextensionsv1.CustomResourceDefinition, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(configcrd.PlatformModuleDefinitionYAML, crd); err != nil {
		return nil, fmt.Errorf("decoding the embedded definition custom resource definition: %w", err)
	}

	return crd, nil
}

// checkCRD rejects a live CRD this run must not build on: one being
// deleted, one owner-referenced, or one whose spec is semantically
// incompatible with the generated CRD. Status and metadata are
// ignored; compatible CRDs are reused untouched.
func checkCRD(live, desired *apiextensionsv1.CustomResourceDefinition) error {
	if live.GetDeletionTimestamp() != nil {
		return fmt.Errorf("%w: %s", errCRDTerminating, live.Name)
	}

	if len(live.OwnerReferences) > 0 {
		return fmt.Errorf("%w: %s", errCRDOwnerReferenced, live.Name)
	}

	liveSpec := normalizeCRDSpec(live.Spec)
	desiredSpec := normalizeCRDSpec(desired.Spec)

	if !reflect.DeepEqual(liveSpec, desiredSpec) {
		return fmt.Errorf("%w: %s", errCRDIncompatible, live.Name)
	}

	return nil
}

// normalizeCRDSpec applies the Kubernetes-default normalization the
// semantic comparison needs: an absent, empty, or None conversion
// strategy is the same as no conversion, and schema Description prose
// — regenerated on every controller-gen run — carries no API meaning.
// Validation, structural, and serialization fields are normalized by
// nothing and stay compared.
func normalizeCRDSpec(spec apiextensionsv1.CustomResourceDefinitionSpec) apiextensionsv1.CustomResourceDefinitionSpec {
	normalized := *spec.DeepCopy()

	if normalized.Conversion == nil ||
		normalized.Conversion.Strategy == "" ||
		normalized.Conversion.Strategy == apiextensionsv1.NoneConverter {
		normalized.Conversion = nil
	}

	for i := range normalized.Versions {
		if schema := normalized.Versions[i].Schema; schema != nil {
			stripSchemaDescriptions(schema.OpenAPIV3Schema)
		}
	}

	return normalized
}

// stripSchemaDescriptions clears Description prose from a schema
// subtree in place, recursing through every nested schema location.
func stripSchemaDescriptions(schema *apiextensionsv1.JSONSchemaProps) {
	if schema == nil {
		return
	}

	schema.Description = ""

	for name, property := range schema.Properties {
		stripSchemaDescriptions(&property)
		schema.Properties[name] = property
	}

	if schema.AdditionalProperties != nil {
		stripSchemaDescriptions(schema.AdditionalProperties.Schema)
	}

	if schema.Items != nil {
		stripSchemaDescriptions(schema.Items.Schema)
		for i, item := range schema.Items.JSONSchemas {
			stripSchemaDescriptions(&item)
			schema.Items.JSONSchemas[i] = item
		}
	}

	stripSchemaDescriptions(schema.Not)
	for i, sub := range schema.AllOf {
		stripSchemaDescriptions(&sub)
		schema.AllOf[i] = sub
	}
	for i, sub := range schema.AnyOf {
		stripSchemaDescriptions(&sub)
		schema.AnyOf[i] = sub
	}
	for i, sub := range schema.OneOf {
		stripSchemaDescriptions(&sub)
		schema.OneOf[i] = sub
	}
}
