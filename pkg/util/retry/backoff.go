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

package retry

import (
	"errors"
	"math"
	"time"
)

// Backoff configures exponential delays between failed attempts.
// InitialDelay must be positive, Multiplier finite and at least one,
// and MaxDelay at least InitialDelay.
type Backoff struct {
	InitialDelay time.Duration
	Multiplier   float64
	MaxDelay     time.Duration
}

// delay returns the pause before retry number retry, where the first
// retry is numbered one. Callers validate the policy before using it.
func (b Backoff) delay(retry int) time.Duration {
	delay := b.InitialDelay

	for range retry - 1 {
		if delay >= b.MaxDelay {
			return b.MaxDelay
		}

		next := float64(delay) * b.Multiplier
		if next >= float64(b.MaxDelay) {
			return b.MaxDelay
		}

		delay = time.Duration(next)
	}

	return min(delay, b.MaxDelay)
}

func (b Backoff) validate() error {
	switch {
	case b.InitialDelay <= 0:
		return errors.New("Backoff.InitialDelay must be positive")
	case math.IsNaN(b.Multiplier), math.IsInf(b.Multiplier, 0), b.Multiplier < 1:
		return errors.New("Backoff.Multiplier must be finite and at least one")
	case b.MaxDelay < b.InitialDelay:
		return errors.New("Backoff.MaxDelay must be at least InitialDelay")
	default:
		return nil
	}
}
