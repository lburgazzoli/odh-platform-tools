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

// Package retry provides a bounded, context-aware retry loop with an
// explicit attempt limit, backoff policy, and per-error retry predicate.
package retry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// ErrInvalidOptions identifies an invalid retry configuration.
var ErrInvalidOptions = errors.New("invalid retry options")

// Options configure one OnError invocation. MaxAttempts includes
// the initial call. A single-attempt policy needs no backoff or retry
// predicate; policies with retries require both.
type Options struct {
	MaxAttempts int
	Backoff     Backoff
	Retriable   func(error) bool
}

// ApplyTo implements Option, so a complete retry policy can be
// passed as a preset. Options apply in order and later values replace
// earlier ones.
func (opts Options) ApplyTo(target *Options) {
	*target = opts
}

// Option customizes one retry invocation.
type Option = option.Option[Options]

// WithMaxAttempts sets the maximum total calls to fn, including the
// initial call.
func WithMaxAttempts(attempts int) Option {
	return option.FunctionalOption[Options](func(target *Options) {
		target.MaxAttempts = attempts
	})
}

// WithBackoff sets the exponential delay policy between attempts.
func WithBackoff(backoff Backoff) Option {
	return option.FunctionalOption[Options](func(target *Options) {
		target.Backoff = backoff
	})
}

// WithRetriable sets the predicate that selects errors eligible for
// another attempt.
func WithRetriable(retriable func(error) bool) Option {
	return option.FunctionalOption[Options](func(target *Options) {
		target.Retriable = retriable
	})
}

// OnError runs fn until it succeeds, returns a terminal error, reaches
// MaxAttempts, or its context is done. The default policy runs fn once.
// A retry policy requires a positive backoff and a non-nil predicate.
// Nil options are ignored.
//
// Context cancellation observed during a pause prevents the next call.
// After an attempt, a successful result wins a cancellation race; when
// an attempt fails and ctx is done, ctx.Err is returned.
func OnError(
	ctx context.Context,
	fn func(context.Context) error,
	opts ...Option,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if fn == nil {
		return fmt.Errorf("%w: attempt function is required", ErrInvalidOptions)
	}

	resolved := Options{MaxAttempts: 1}
	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&resolved)
		}
	}

	if err := resolved.validate(); err != nil {
		return err
	}

	for attempt := 1; attempt <= resolved.MaxAttempts; attempt++ {
		if attempt > 1 {
			if err := pause(ctx, resolved.Backoff.delay(attempt-1)); err != nil {
				return err
			}
		}

		err := fn(ctx)
		if err == nil {
			return nil
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		if resolved.Retriable == nil || !resolved.Retriable(err) || attempt == resolved.MaxAttempts {
			return err
		}
	}

	return nil
}

func (opts Options) validate() error {
	if opts.MaxAttempts < 1 {
		return fmt.Errorf("%w: MaxAttempts must be positive", ErrInvalidOptions)
	}

	if opts.MaxAttempts == 1 {
		return nil
	}

	if opts.Retriable == nil {
		return fmt.Errorf("%w: Retriable is required when MaxAttempts exceeds one", ErrInvalidOptions)
	}

	if err := opts.Backoff.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOptions, err)
	}

	return nil
}

func pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
