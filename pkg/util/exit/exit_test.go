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

package exit

import (
	"errors"
	"testing"

	. "github.com/onsi/gomega"
)

// TestExitErrorDelegatesToCause proves the message and identity come
// from the wrapped execution error when one is present.
func TestExitErrorDelegatesToCause(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cause := errors.New("render failed")
	err := &Error{Code: 1, Err: cause}

	g.Expect(err.Error()).To(Equal("render failed"))
	g.Expect(err.Unwrap()).To(BeIdenticalTo(cause))
	g.Expect(errors.Is(err, cause)).To(BeTrue())
	g.Expect(err.ExitCode()).To(Equal(1))
}

// TestExitErrorFallsBackToStatus proves a code-only failure — the
// findings-only shape, where no execution error exists — renders a
// status line instead of panicking on a nil cause.
func TestExitErrorFallsBackToStatus(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := &Error{Code: 2}

	g.Expect(err.Error()).To(Equal("exit status 2"))
	g.Expect(err.Unwrap()).To(BeNil())
	g.Expect(err.ExitCode()).To(Equal(2))
}
