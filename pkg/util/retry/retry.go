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

// Package retry carries the shared bounded retry loop: one function
// over an explicit schedule of pauses, closing the gap between the
// standard library — whose retry helpers either generate their own
// exponential schedule, sleep through cancellation, or discard the
// last error — and the needs of a deadline-bound invocation.
package retry

import (
	"context"
	"time"
)

// OnError runs fn until it succeeds, its error is rejected as
// retriable, the schedule is exhausted, or ctx is done. fn runs at
// most len(pauses)+1 times: the first attempt plus one after each
// pause, waiting pauses[i-1] ahead of attempt i. An empty schedule
// means a single attempt.
//
// The outcome is exactly one of: nil on success; the error itself
// when retriable rejects it; the last error when the attempts are
// exhausted; ctx.Err() when the context is done — checked before the
// first attempt, and during every pause, where cancellation wins the
// race with the timer firing and no further attempt runs.
//
// retriable classifies each failed attempt: true means the error is
// worth another attempt, false that it is terminal and returns
// immediately. Callers mark their own terminal conditions — most
// commonly a context error surfacing through the operation — by
// returning false for them.
//
// fn receives the same context: a per-attempt timeout or other
// derived context belongs to the caller inside fn.
func OnError(
	ctx context.Context,
	pauses []time.Duration,
	retriable func(error) bool,
	fn func(context.Context) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	var lastErr error

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(pauses[attempt-1])

			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}

			// The timer and the cancellation can both be ready when
			// the wait ends exactly at the deadline; the cancellation
			// wins, and no further attempt runs.
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		err := fn(ctx)
		if err == nil {
			return nil
		}

		if !retriable(err) {
			return err
		}

		lastErr = err

		if attempt >= len(pauses) {
			return lastErr
		}
	}
}
