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

// Package deployment applies ordered mutations to typed Deployments.
package deployment

import (
	"errors"

	appsv1 "k8s.io/api/apps/v1"
)

// Customize mutates the Deployment in option order, stopping on the first error.
// Earlier mutations are retained. Nil input fails; nil options are ignored.
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
		if err := request(d); err != nil {
			return err
		}
	}

	return nil
}
