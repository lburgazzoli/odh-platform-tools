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

// Package crd waits for custom resource definitions to serve.
package crd

import (
	"context"

	apihelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// WaitForCRDToBeEstablished polls the named custom resource
// definition through the given client until it reports both
// Established and NamesAccepted — the upstream apihelpers condition
// checks, not a local reimplementation. The first poll is immediate;
// later polls follow the configured interval (WithPollInterval,
// DefaultPollInterval). No discovery refresh follows: a lazy REST
// mapper reloads missing groups itself, so groups served by a CRD
// established here become mappable on the next lookup. Errors are
// returned with their identity preserved — an apiextensions NotFound,
// a context cancellation, or any request error reaches the caller
// unwrapped in kind, and a failure means no successful establishment
// is claimed.
func WaitForCRDToBeEstablished(
	ctx context.Context,
	cli crclient.Client,
	name string,
	opts ...WaitOption,
) error {
	options := WaitOptions{
		PollInterval: DefaultPollInterval,
	}

	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&options)
		}
	}

	return wait.PollUntilContextCancel(ctx, options.PollInterval, true, func(ctx context.Context) (bool, error) {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := cli.Get(ctx, crclient.ObjectKey{Name: name}, &crd); err != nil {
			return false, err
		}

		if !apihelpers.IsCRDConditionTrue(&crd, apiextensionsv1.Established) {
			return false, nil
		}

		if !apihelpers.IsCRDConditionTrue(&crd, apiextensionsv1.NamesAccepted) {
			return false, nil
		}

		return true, nil
	})
}
