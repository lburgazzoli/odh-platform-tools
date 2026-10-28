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

package adapt

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	fwgvk "github.com/opendatahub-io/odh-platform-utilities/framework/cluster/gvk"
	odhTypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
)

// ControllerBridge supplies the framework deploy action's ownership policy.
// Ordinary resources are dynamically owned; Namespaces and CRDs are excluded.
// It supports deployment only, not reconciliation or garbage collection.
type ControllerBridge struct {
	client       crclient.Client
	dynamicOwned map[schema.GroupVersionKind]struct{}
}

var _ odhTypes.Controller = (*ControllerBridge)(nil)

// NewControllerBridge uses the supplied client unchanged.
func NewControllerBridge(client crclient.Client) *ControllerBridge {
	return &ControllerBridge{
		client:       client,
		dynamicOwned: map[schema.GroupVersionKind]struct{}{},
	}
}

// Owns reports whether the action registered a GVK for dynamic ownership.
func (b *ControllerBridge) Owns(gvk schema.GroupVersionKind) bool {
	_, owned := b.dynamicOwned[gvk]

	return owned
}

// AddDynamicOwnedType records a type the action decided to own.
func (b *ControllerBridge) AddDynamicOwnedType(gvk schema.GroupVersionKind) {
	b.dynamicOwned[gvk] = struct{}{}
}

// GetClient returns the invocation client.
func (b *ControllerBridge) GetClient() crclient.Client {
	return b.client
}

// GetDiscoveryClient returns nil; the deploy action does not use discovery here.
func (b *ControllerBridge) GetDiscoveryClient() discovery.DiscoveryInterface {
	return nil
}

// GetDynamicClient returns nil; the deploy action does not use a dynamic client.
func (b *ControllerBridge) GetDynamicClient() dynamic.Interface {
	return nil
}

// IsDynamicOwnershipEnabled enables owner references on ordinary resources.
func (b *ControllerBridge) IsDynamicOwnershipEnabled() bool {
	return true
}

// IsExcludedFromDynamicOwnership excludes Namespaces and CRDs.
func (b *ControllerBridge) IsExcludedFromDynamicOwnership(gvk schema.GroupVersionKind) bool {
	return gvk == fwgvk.Namespace || gvk == fwgvk.CustomResourceDefinition
}
