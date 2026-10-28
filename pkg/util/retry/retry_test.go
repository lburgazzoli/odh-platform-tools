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
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

// everyErrorIsRetriable accepts any failure, so the tests exercise
// the schedule and cancellation paths rather than classification.
func everyErrorIsRetriable(error) bool { return true }

// TestIsRetriable proves the default classification: wrapped and bare
// context errors are terminal, everything else retries.
func TestIsRetriable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		err      error
		expected bool
	}{
		{"cancellation", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"wrapped cancellation", fmt.Errorf("action failed: %w", context.Canceled), false},
		{"wrapped deadline", fmt.Errorf("action failed: %w", context.DeadlineExceeded), false},
		{"ordinary failure", errors.New("apply failure"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			g.Expect(IsRetriable(tc.err)).To(Equal(tc.expected))
		})
	}
}

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
// len(pauses)+1 attempts returns the last failure, not an earlier
// one: each attempt fails with its own sentinel.
func TestOnErrorExhaustionKeepsLastError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	first := errors.New("first attempt failure")
	second := errors.New("second attempt failure")

	count := 0

	err := OnError(
		t.Context(),
		[]time.Duration{time.Millisecond},
		everyErrorIsRetriable,
		func(context.Context) error {
			count++
			if count == 1 {
				return first
			}
			return second
		},
	)
	g.Expect(err).To(MatchError(second))
	g.Expect(err).NotTo(MatchError(first))
	g.Expect(count).To(Equal(2))
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

// TestOnErrorCancellationWinsTimerRace proves the convergence rule
// when the pause timer and the cancellation are both ready when the
// wait ends: whichever arm the select picks, the cancellation wins,
// no further attempt runs, and the context error is returned. The
// zero pause makes the timer ready immediately, so every iteration
// enters the race; repeating them makes both arms near-certain to be
// exercised.
func TestOnErrorCancellationWinsTimerRace(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	for range 10 {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		count := 0

		err := OnError(
			ctx,
			[]time.Duration{0},
			everyErrorIsRetriable,
			func(context.Context) error {
				count++
				cancel()
				return errors.New("attempt failure")
			},
		)
		g.Expect(err).To(MatchError(context.Canceled))
		g.Expect(count).To(Equal(1))
	}
}
