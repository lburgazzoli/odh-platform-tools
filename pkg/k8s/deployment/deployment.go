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

// Package deployment customizes typed Deployment workloads: named
// container image and env mutations, collected as ordered options and
// applied in place by Customize. The helpers own the mechanics only —
// target-selection policy, image resolution, and skip rules stay with
// callers.
package deployment

import (
	"errors"

	appsv1 "k8s.io/api/apps/v1"
)

// Customize applies the collected customization options to the typed
// Deployment in place. Requests apply in the order their options were
// passed; the first error stops the pass and earlier mutations are
// kept — there is no rollback. A nil Deployment returns an error, nil
// options are ignored, and a non-nil Deployment without options
// succeeds unchanged. There is no change count: a successful pass may
// have changed nothing.
func Customize(d *appsv1.Deployment, opts ...CustomizeOption) error {
	if d == nil {
		return errors.New("cannot customize a nil Deployment")
	}

	options := CustomizeOptions{}

	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&options)
		}
	}

	for _, request := range options.requests {
		if err := request.apply(d); err != nil {
			return err
		}
	}

	return nil
}
