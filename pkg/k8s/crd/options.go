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

package crd

import (
	"time"

	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// DefaultPollInterval is the delay between CRD establishment polls;
// the first poll is always immediate.
const DefaultPollInterval = 500 * time.Millisecond

// WaitOptions holds the optional wait configuration.
type WaitOptions struct {
	// PollInterval is the delay between establishment polls.
	PollInterval time.Duration
}

// ApplyTo implements WaitOption, so a whole WaitOptions can
// be passed as an option itself — a bundled preset. Non-positive
// fields are ignored, keeping the target's values.
func (opts WaitOptions) ApplyTo(target *WaitOptions) {
	if opts.PollInterval > 0 {
		target.PollInterval = opts.PollInterval
	}
}

// WaitOption customizes one wait. Options apply in order over the
// recorded defaults; a nil option is ignored.
type WaitOption = option.Option[WaitOptions]

// newOptions applies the options over the recorded defaults.
func newOptions(opts ...WaitOption) WaitOptions {
	defaults := WaitOptions{
		PollInterval: DefaultPollInterval,
	}

	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&defaults)
		}
	}

	return defaults
}

// WithPollInterval sets the delay between establishment polls; the
// first poll stays immediate. A non-positive interval is ignored,
// keeping the default.
func WithPollInterval(interval time.Duration) WaitOption {
	return option.FunctionalOption[WaitOptions](func(target *WaitOptions) {
		if interval > 0 {
			target.PollInterval = interval
		}
	})
}
