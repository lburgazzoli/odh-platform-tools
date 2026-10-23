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

package owner

import (
	"fmt"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// Outcome classifies the desired-spec publication of the definition
// owner: whether the normalized unexpanded spec authored in the file
// is known to be the spec stored server-side.
//
// An outcome never describes writes in general. Only a successful
// create, a successful update, or an already-matching read confirms
// the desired spec, so a nil owner with OutcomeUnknown asserts an
// ambiguous write, not the absence of one.
type Outcome string

// The publication outcomes.
const (
	// OutcomeNotAttempted marks failures before any definition write
	// was attempted: envelope rejection, definition CRD bootstrap
	// failures, and establishment or discovery failures.
	OutcomeNotAttempted Outcome = "not_attempted"

	// OutcomeConfirmed marks a publication whose desired spec is
	// known to be stored server-side: a successful create, a
	// successful update, or a live definition that already matches.
	OutcomeConfirmed Outcome = "confirmed"

	// OutcomeRejected marks a definitive server-side refusal of the
	// desired publication: incompatible live state (terminating,
	// owner-referenced, missing UID) or a definitive API error. A
	// rejected publication can still carry a confirmed owner
	// identity from a prior read.
	OutcomeRejected Outcome = "rejected"

	// OutcomeUnknown marks an ambiguous publication: a request whose
	// outcome the server never reported — a lost create or update
	// response, or a cancellation. The write may or may not have
	// been applied.
	OutcomeUnknown Outcome = "unknown"
)

// Error is the safe typed error Ensure returns. Its text names the
// failed operation and the publication outcome, but never echoes the
// cause's message or any authored or API-sourced values; the raw
// cause — including cancellation and API error identities — stays
// reachable only through errors.Is and errors.As on Unwrap.
//
// Owner carries the best-known server-backed definition state: a
// confirmed identity from a successful read, creation, or update. A
// nil Owner means the identity is unconfirmed, never that no write
// occurred.
type Error struct {
	// Outcome classifies the desired-spec publication.
	Outcome Outcome

	// Owner is the best-known server-backed definition state, or nil
	// when the identity is unconfirmed.
	Owner *v1alpha1.PlatformModuleDefinition

	message string
	cause   error
}

// Error returns the safe message: operation, reason, and outcome
// only.
func (e *Error) Error() string {
	return e.message
}

// Unwrap returns the raw cause, preserving error identities for
// errors.Is and errors.As.
func (e *Error) Unwrap() error {
	return e.cause
}

// newError builds a typed error whose safe message names the
// operation and reason and classifies the publication outcome.
func newError(
	outcome Outcome,
	owner *v1alpha1.PlatformModuleDefinition,
	operation string,
	reason string,
	cause error,
) *Error {
	message := fmt.Sprintf("ensuring the module definition owner: %s", operation)
	if reason != "" {
		message = fmt.Sprintf("%s: %s", message, reason)
	}

	return &Error{
		Outcome: outcome,
		Owner:   owner,
		message: fmt.Sprintf("%s: publication %s", message, outcome),
		cause:   cause,
	}
}
