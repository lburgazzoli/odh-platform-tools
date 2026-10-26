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
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// CustomizeOptions collects the ordered customization requests built
// by the options; Customize applies them in order.
type CustomizeOptions struct {
	// requests are the ordered mutations to apply.
	requests []customizationRequest
}

// CustomizeOption customizes a typed ConfigMap. Options apply in the
// order they are passed; a nil option is ignored.
type CustomizeOption = option.Option[CustomizeOptions]

// WithData sets one data key to the value, replacing an existing
// entry and materializing a missing data block first.
func WithData(key string, value string) CustomizeOption {
	return option.FunctionalOption[CustomizeOptions](func(target *CustomizeOptions) {
		target.requests = append(target.requests, setData(key, value))
	})
}

// Remove deletes the data keys; keys missing from the data block — or
// a missing data block itself — are a no-op.
func Remove(keys ...string) CustomizeOption {
	return option.FunctionalOption[CustomizeOptions](func(target *CustomizeOptions) {
		target.requests = append(target.requests, removeKeys(keys))
	})
}
