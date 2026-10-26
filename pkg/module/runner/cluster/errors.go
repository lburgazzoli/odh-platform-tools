package cluster

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// IsWriteOutcomeUnknown reports whether a failed write request carries
// no usable server answer: a transport failure, a cancellation, a
// deadline, or a timeout status — the one server answer whose write
// outcome stays ambiguous, which is why client-go retries these. Every
// other Kubernetes status error is a definitive answer.
//
// The predicate classifies the run's write policy only; it never
// infers that no write occurred.
func IsWriteOutcomeUnknown(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var status *apierrors.StatusError
	if errors.As(err, &status) {
		// The write may have committed while the answer was late, so
		// a timeout answer is as ambiguous as a lost one.
		return apierrors.IsTimeout(status) || apierrors.IsServerTimeout(status)
	}

	return true
}
