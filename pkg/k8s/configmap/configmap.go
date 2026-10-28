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

// Package configmap applies ordered mutations to typed ConfigMaps.
package configmap

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
)

// Customize mutates the ConfigMap in option order, stopping on the first error.
// Earlier mutations are retained. Nil input fails; nil options are ignored.
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
