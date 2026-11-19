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

package validator

import (
	"time"

	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// DefaultPollInterval is the default observation interval, mirroring
// deployer.DefaultWaitPollInterval.
const DefaultPollInterval = time.Second

// ServiceOptions configures one validator service.
type ServiceOptions struct {
	// PollInterval defaults to DefaultPollInterval; it must be positive.
	PollInterval time.Duration
}

// ApplyTo replaces the configuration with this preset.
func (o ServiceOptions) ApplyTo(target *ServiceOptions) {
	*target = o
}

// ServiceOption customizes construction; options apply in order.
type ServiceOption = option.Option[ServiceOptions]

// WithPollInterval sets the observation interval.
func WithPollInterval(interval time.Duration) ServiceOption {
	return option.FunctionalOption[ServiceOptions](func(target *ServiceOptions) {
		target.PollInterval = interval
	})
}

// preset applies defaults to unset fields.
func (o ServiceOptions) preset() ServiceOptions {
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultPollInterval
	}

	return o
}
