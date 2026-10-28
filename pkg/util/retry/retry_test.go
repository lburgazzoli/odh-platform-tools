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
	"math"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

func everyErrorIsRetriable(error) bool { return true }

func attempts(failures int) (func(context.Context) error, *int) {
	count := 0

	return func(context.Context) error {
		count++
		if count <= failures {
			return fmt.Errorf("attempt %d failed", count)
		}
		return nil
	}, &count
}

func TestOnErrorDefaultsToOneAttempt(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	fn, count := attempts(5)

	err := OnError(t.Context(), fn)
	g.Expect(err).To(MatchError("attempt 1 failed"))
	g.Expect(*count).To(Equal(1))
}

func TestOnErrorRetriesToSuccess(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	fn, count := attempts(2)

	err := OnError(
		t.Context(),
		fn,
		WithMaxAttempts(3),
		WithBackoff(Backoff{InitialDelay: time.Millisecond, Multiplier: 2, MaxDelay: 2 * time.Millisecond}),
		WithRetriable(everyErrorIsRetriable),
	)
	g.Expect(err).To(Succeed())
	g.Expect(*count).To(Equal(3))
}

func TestOnErrorReturnsLastErrorWhenExhausted(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	fn, count := attempts(5)

	err := OnError(
		t.Context(),
		fn,
		WithMaxAttempts(2),
		WithBackoff(Backoff{InitialDelay: time.Millisecond, Multiplier: 1, MaxDelay: time.Millisecond}),
		WithRetriable(everyErrorIsRetriable),
	)
	g.Expect(err).To(MatchError("attempt 2 failed"))
	g.Expect(*count).To(Equal(2))
}

func TestOnErrorStopsOnTerminalError(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	terminal := errors.New("terminal")
	count := 0

	err := OnError(
		t.Context(),
		func(context.Context) error {
			count++
			return terminal
		},
		WithMaxAttempts(4),
		WithBackoff(Backoff{InitialDelay: time.Hour, Multiplier: 2, MaxDelay: 8 * time.Hour}),
		WithRetriable(func(err error) bool { return !errors.Is(err, terminal) }),
	)
	g.Expect(err).To(MatchError(terminal))
	g.Expect(count).To(Equal(1))
}

func TestOnErrorOptionsFollowRepositoryOptionPattern(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	first := Options{
		MaxAttempts: 2,
		Backoff:     Backoff{InitialDelay: time.Millisecond, Multiplier: 1, MaxDelay: time.Millisecond},
		Retriable:   everyErrorIsRetriable,
	}
	second := Options{
		MaxAttempts: 1,
	}
	fn, count := attempts(1)

	var nilOption Option
	err := OnError(t.Context(), fn, first, nilOption, second)
	g.Expect(err).To(MatchError("attempt 1 failed"))
	g.Expect(*count).To(Equal(1))

	var nilFunctionalOption Option = option.FunctionalOption[Options](nil)
	g.Expect(OnError(t.Context(), func(context.Context) error { return nil }, nilFunctionalOption)).To(Succeed())
}

func TestBackoffCapsExponentialDelay(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	backoff := Backoff{InitialDelay: time.Second, Multiplier: 2, MaxDelay: 8 * time.Second}

	g.Expect([]time.Duration{
		backoff.delay(1),
		backoff.delay(2),
		backoff.delay(3),
		backoff.delay(4),
		backoff.delay(5),
	}).To(Equal([]time.Duration{
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		8 * time.Second,
	}))
}

func TestOnErrorRejectsInvalidPolicies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		options []Option
	}{
		{name: "non-positive attempts", options: []Option{WithMaxAttempts(0)}},
		{name: "missing classifier", options: []Option{
			WithMaxAttempts(2),
			WithBackoff(Backoff{InitialDelay: time.Second, Multiplier: 2, MaxDelay: 8 * time.Second}),
		}},
		{name: "missing backoff", options: []Option{
			WithMaxAttempts(2),
			WithRetriable(everyErrorIsRetriable),
		}},
		{name: "invalid multiplier", options: []Option{
			WithMaxAttempts(2),
			WithBackoff(Backoff{InitialDelay: time.Second, Multiplier: math.NaN(), MaxDelay: time.Second}),
			WithRetriable(everyErrorIsRetriable),
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			called := false
			err := OnError(t.Context(), func(context.Context) error {
				called = true
				return nil
			}, tc.options...)

			g := NewWithT(t)
			g.Expect(errors.Is(err, ErrInvalidOptions)).To(BeTrue())
			g.Expect(called).To(BeFalse())
		})
	}
}

func TestOnErrorCancellationDuringPause(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	fn, count := attempts(5)
	err := OnError(
		ctx,
		fn,
		WithMaxAttempts(3),
		WithBackoff(Backoff{InitialDelay: time.Hour, Multiplier: 2, MaxDelay: 2 * time.Hour}),
		WithRetriable(everyErrorIsRetriable),
	)
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	g.Expect(*count).To(Equal(1))
}

func TestOnErrorCancellationAfterAttempt(t *testing.T) {
	t.Parallel()

	t.Run("failure returns context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		err := OnError(ctx, func(context.Context) error {
			cancel()
			return errors.New("action failure")
		}, WithMaxAttempts(1))
		g := NewWithT(t)
		g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})

	t.Run("success wins", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		err := OnError(ctx, func(context.Context) error {
			cancel()
			return nil
		})
		g := NewWithT(t)
		g.Expect(err).To(Succeed())
	})
}

func TestOnErrorRefusesCancelledContext(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	called := false
	err := OnError(ctx, func(context.Context) error {
		called = true
		return nil
	})
	g.Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	g.Expect(called).To(BeFalse())
}
