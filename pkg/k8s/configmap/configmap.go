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

// Package configmap customizes typed ConfigMap resources: data key
// mutations, collected as ordered options and applied in place by
// Customize. The helpers own the mechanics only — which keys to set
// or remove stays with callers. The package deliberately mirrors
// pkg/k8s/deployment's option and request pattern.
package configmap

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
)

// Customize applies the collected customization options to the typed
// ConfigMap in place. Requests apply in the order their options were
// passed; the first error stops the pass and earlier mutations are
// kept — there is no rollback. A nil ConfigMap returns an error, nil
// options are ignored, and a non-nil ConfigMap without options
// succeeds unchanged. There is no change count: a successful pass may
// have changed nothing.
func Customize(cm *corev1.ConfigMap, opts ...CustomizeOption) error {
	if cm == nil {
		return errors.New("cannot customize a nil ConfigMap")
	}

	options := CustomizeOptions{}

	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&options)
		}
	}

	for _, request := range options.requests {
		if err := request(cm); err != nil {
			return err
		}
	}

	return nil
}
