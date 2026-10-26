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

package k8s

import (
	"context"
	"time"

	apihelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// crdPollInterval is the delay between CRD establishment polls; the
// first poll is immediate.
const crdPollInterval = 500 * time.Millisecond

// WaitForCRDToBeEstablished polls the named custom resource
// definitions through the given client until every one reports both
// Established and NamesAccepted — the upstream apihelpers condition
// checks, not a local reimplementation. No discovery refresh follows:
// a lazy REST mapper reloads missing groups itself, so groups served
// by CRDs established here become mappable on the next lookup. Errors
// are returned with their identity preserved — an apiextensions
// NotFound, a context cancellation, or any request error reaches the
// caller unwrapped in kind, and a failure means no successful
// establishment is claimed. No names means nothing to wait for.
func WaitForCRDToBeEstablished(
	ctx context.Context,
	cli crclient.Client,
	names ...string,
) error {
	if len(names) == 0 {
		return ctx.Err()
	}

	return wait.PollUntilContextCancel(ctx, crdPollInterval, true, func(ctx context.Context) (bool, error) {
		for _, name := range names {
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
		}

		return true, nil
	})
}
