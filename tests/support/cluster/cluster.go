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

// Package cluster provides the test-only managed cluster engines of
// the live end-to-end suite: a supervised Kind engine that owns the
// exact clusters it creates, and an external engine that uses one
// explicitly confirmed configuration and never deletes anything.
//
// The engines adapt the db-operator's test cluster separation
// (opendatahub-module-operator@db-service, commit
// 1574e15f887a7f33e5dfcae879932699aa0f2aa4, modules/opendatahub-db-operator/test/support/cluster)
// without its prefix purge, ambient kubeconfig mutation, or automatic
// cert-manager and operator installation: no engine here lists,
// purges, or writes any cluster or configuration it did not itself
// create under a private path.
//
// This package is test support only. Its unit tests run offline with
// injected providers and never create a cluster.
package cluster

import (
	"context"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Engine types selectable through New and the e2e environment.
const (
	// TypeKind manages an isolated, engine-owned Kind cluster.
	TypeKind = "kind"

	// TypeExternal uses one explicitly confirmed external cluster.
	TypeExternal = "external"
)

// Engine is the lifecycle of one live test cluster: a private
// configuration and connected client after Setup, and a Stop that
// disposes exactly what the engine owns. The KubeconfigPath and
// ContextName pair is the confirmed identity subprocesses use — the
// run CLI receives them as explicit flags, never an ambient
// current-context fallback.
type Engine interface {
	// Name is the exact cluster identity: the created cluster name
	// for managed engines, the confirmed context for external ones.
	Name() string

	// KubeconfigPath is the private kubeconfig file path.
	KubeconfigPath() string

	// ContextName is the kubeconfig context to select explicitly.
	ContextName() string

	// Config is the connected REST configuration.
	Config() *rest.Config

	// Client is the connected controller-runtime client.
	Client() client.Client

	// Setup connects the client and proves the API answers.
	Setup(ctx context.Context) error

	// Stop disposes exactly the cluster the engine owns; an external
	// engine never deletes its cluster. A failed Stop reports the
	// exact retained cluster and its private artifacts.
	Stop(ctx context.Context) error
}

// New returns the engine selected by the options: Kind by default,
// external when explicitly selected and confirmed.
func New(ctx context.Context, opts ...Option) (Engine, error) {
	options := newOptions(opts...)

	switch options.Type {
	case "", TypeKind:
		return newKind(ctx, options)
	case TypeExternal:
		return newExternal(ctx, options)
	default:
		return nil, &InvalidOptionsError{Reason: "unsupported engine type " + options.Type}
	}
}
