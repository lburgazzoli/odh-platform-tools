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

package configmap

import (
	corev1 "k8s.io/api/core/v1"
)

// Requests defer fallible work until Customize; options only collect them.
type customizationRequest func(cm *corev1.ConfigMap) error

func setData(key string, value string) customizationRequest {
	return func(cm *corev1.ConfigMap) error {
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}

		cm.Data[key] = value

		return nil
	}
}

func removeKeys(keys []string) customizationRequest {
	return func(cm *corev1.ConfigMap) error {
		for _, key := range keys {
			delete(cm.Data, key)
		}

		return nil
	}
}
