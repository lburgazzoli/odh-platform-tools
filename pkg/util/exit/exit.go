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

// Package exit defines the process exit contract shared by the
// module commands.
package exit

import (
	"fmt"
)

// Error carries a process exit code and the execution error.
// Commands report their outcome themselves before returning it; the
// message is a backstop for contexts that print the error anyway, so
// it never duplicates the command's report.
type Error struct {
	Code int
	Err  error
}

// Error renders the wrapped execution error, or a generic status
// fallback when the code alone carries the outcome — error-severity
// lint findings, for example, fail without an execution error.
func (e *Error) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}

	return fmt.Sprintf("exit status %d", e.Code)
}

// Unwrap preserves the underlying execution error identity.
func (e *Error) Unwrap() error { return e.Err }

// ExitCode reports the classified process exit status; cmd/main.go
// dispatches on it through its consumer-side exitCoder interface.
func (e *Error) ExitCode() int { return e.Code }
