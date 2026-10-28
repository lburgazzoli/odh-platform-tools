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

package loader

import (
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// LoadOptions reserves configuration for future loader options.
type LoadOptions struct{}

// ApplyTo replaces the load options.
func (opts LoadOptions) ApplyTo(target *LoadOptions) {
	*target = opts
}

// LoadOption customizes a load; nil options are ignored.
type LoadOption = option.Option[LoadOptions]
