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
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

// everyErrorIsRetriable accepts any failure, so the tests exercise
// the schedule and cancellation paths rather than classification.
func everyErrorIsRetriable(error) bool { return true }

// attempts counts how many times fn ran and fails the first count
// failures, then succeeds.
func attempts(failures int) (func(context.Context) error, *int) {
	count := 0

	return func(context.Context) error {
		count++

		if count <= failures {
			return errors.New("attempt failure")
		}

		return nil
	}, &count
}

// TestOnErrorSucceedsOnFirstAttempt proves a successful fn runs once
// and returns nil without consulting the schedule.
func TestOnErrorSucceedsOnFirstAttempt(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fn, count := attempts(0)

	g.Expect(OnError(
		t.Context(),
		[]time.Duration{time.Hour},
		everyErrorIsRetriable,
		fn,
	)).To(Succeed())
	g.Expect(*count).To(Equal(1))
}

// TestOnErrorEmptyScheduleRunsOnce proves an empty schedule pins
// single-attempt semantics: the failure is returned as-is.
func TestOnErrorEmptyScheduleRunsOnce(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fn, count := attempts(10)

	err := OnError(t.Context(), nil, everyErrorIsRetriable, fn)
	g.Expect(err).To(MatchError("attempt failure"))
	g.Expect(*count).To(Equal(1))
}

// TestOnErrorRetriesUntilSuccess proves one attempt runs per
// schedule entry plus the first: two failures need two pauses to
// reach the successful third attempt.
func TestOnErrorRetriesUntilSuccess(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fn, count := attempts(2)

	g.Expect(OnError(
		t.Context(),
		[]time.Duration{time.Millisecond, time.Millisecond},
		everyErrorIsRetriable,
		fn,
	)).To(Succeed())
	g.Expect(*count).To(Equal(3))
}

// TestOnErrorExhaustionKeepsLastError proves exhaustion after
// len(pauses)+1 attempts returns the last failure.
func TestOnErrorExhaustionKeepsLastError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	fn, count := attempts(10)

	err := OnError(
		t.Context(),
		[]time.Duration{time.Millisecond},
		everyErrorIsRetriable,
		fn,
	)
	g.Expect(err).To(MatchError("attempt failure"))
	g.Expect(*count).To(Equal(2))
}

// TestOnErrorStopsOnTerminalError proves a rejected error returns
// immediately, ahead of any remaining schedule.
func TestOnErrorStopsOnTerminalError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	terminal := errors.New("terminal failure")
	count := 0

	err := OnError(
		t.Context(),
		[]time.Duration{time.Hour, time.Hour},
		func(err error) bool { return !errors.Is(err, terminal) },
		func(context.Context) error {
			count++
			return terminal
		},
	)
	g.Expect(err).To(MatchError(terminal))
	g.Expect(count).To(Equal(1))
}

// TestOnErrorRefusesCancelledContext proves a done context fails
// before the first attempt and fn never runs.
func TestOnErrorRefusesCancelledContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	count := 0

	err := OnError(ctx, nil, everyErrorIsRetriable, func(context.Context) error {
		count++
		return nil
	})
	g.Expect(err).To(MatchError(context.Canceled))
	g.Expect(count).To(Equal(0))
}

// TestOnErrorCancellationDuringPause proves a deadline landing inside
// a pause aborts the wait, returns the context error, and runs no
// further attempt. The pause is far longer than the deadline and the
// budget generous, so the timing holds under -race load.
func TestOnErrorCancellationDuringPause(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	fn, count := attempts(10)

	started := time.Now()

	err := OnError(
		ctx,
		[]time.Duration{time.Hour},
		everyErrorIsRetriable,
		fn,
	)
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(time.Since(started)).To(BeNumerically("<", 30*time.Second))
	g.Expect(*count).To(Equal(1))
}
