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

package generate

import (
	"errors"
)

// nonEmptyValue is a string flag value that rejects an explicitly
// empty value; later occurrences overwrite earlier ones, matching
// ordinary string flags otherwise. The service still treats an
// omitted flag as its own default.
type nonEmptyValue struct {
	value string
}

// String returns the current value.
func (v *nonEmptyValue) String() string { return v.value }

// Set rejects an empty value and stores the rest.
func (v *nonEmptyValue) Set(value string) error {
	if value == "" {
		return errors.New("must not be empty")
	}

	v.value = value

	return nil
}

// Type names the value shape for help output.
func (v *nonEmptyValue) Type() string { return "string" }

// singleValue is a string flag value that rejects a second occurrence
// and an empty value.
type singleValue struct {
	value string
	set   bool
}

// String returns the current value.
func (v *singleValue) String() string { return v.value }

// Set stores the first nonempty occurrence and rejects the rest.
func (v *singleValue) Set(value string) error {
	if v.set {
		return errors.New("may only be given once")
	}

	if value == "" {
		return errors.New("must not be empty")
	}

	v.value = value
	v.set = true

	return nil
}

// Type names the value shape for help output.
func (v *singleValue) Type() string { return "string" }
