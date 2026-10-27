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

// ControllerBridge is the minimal concrete Controller the framework
// deploy action reads, shaped after the operator's modules controller:
// dynamic ownership with no statically owned types. It owns no
// reconciler and no manager. The action populates the dynamic-owned
// set as it decides to own rendered types; Owns answers from that
// set. core/v1 Namespaces and CRDs stay excluded from ownership — the
// never-own-namespaces policy the action's shouldOwn reads — so the
// action never sets controller references on them (CRDs additionally
// bypass shouldOwn entirely through the action's unowned deployCRD
// path). Its client is the plain connection client; the discovery and
// dynamic getters report nil, because only out-of-scope
// garbage-collection and dynamic-ownership actions read them.
type ControllerBridge struct {
	client       crclient.Client
	dynamicOwned map[schema.GroupVersionKind]struct{}
}

// compile assertion: the bridge satisfies the framework Controller
// contract.
var _ odhTypes.Controller = (*ControllerBridge)(nil)

// NewControllerBridge wires the bridge over the plain connection
// client the deploy action deploys through.
func NewControllerBridge(client crclient.Client) *ControllerBridge {
	return &ControllerBridge{
		client:       client,
		dynamicOwned: map[schema.GroupVersionKind]struct{}{},
	}
}

// Owns reports whether the type is in the dynamic-owned set the
// action populated: a module deploy declares no statically owned types,
// exactly like the operator's modules controller.
func (b *ControllerBridge) Owns(gvk schema.GroupVersionKind) bool {
	_, owned := b.dynamicOwned[gvk]

	return owned
}

// AddDynamicOwnedType records a type the action decided to own.
func (b *ControllerBridge) AddDynamicOwnedType(gvk schema.GroupVersionKind) {
	b.dynamicOwned[gvk] = struct{}{}
}

// GetClient returns the plain connection client the action deploys
// through.
func (b *ControllerBridge) GetClient() crclient.Client {
	return b.client
}

// GetDiscoveryClient reports none: the deploy action reads no
// discovery client.
func (b *ControllerBridge) GetDiscoveryClient() discovery.DiscoveryInterface {
	return nil
}

// GetDynamicClient reports none: the deploy action reads no dynamic
// client.
func (b *ControllerBridge) GetDynamicClient() dynamic.Interface {
	return nil
}

// IsDynamicOwnershipEnabled keeps dynamic ownership on: rendered
// ordinary resources receive controller references to the persisted
// definition.
func (b *ControllerBridge) IsDynamicOwnershipEnabled() bool {
	return true
}

// IsExcludedFromDynamicOwnership excludes core/v1 Namespaces and
// CRDs, matching the upstream controller's exclusions.
func (b *ControllerBridge) IsExcludedFromDynamicOwnership(gvk schema.GroupVersionKind) bool {
	return gvk == fwgvk.Namespace || gvk == fwgvk.CustomResourceDefinition
}
