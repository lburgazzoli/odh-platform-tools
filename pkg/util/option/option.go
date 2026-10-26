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

// Package option carries the shared generic functional-option types:
// components expose optional configuration as an Option alias over
// their options struct and WithXxx constructors over this adapter,
// so options compose uniformly across the tool.
package option

// Option is one generic option targeting a configuration struct.
type Option[T any] interface {
	ApplyTo(target *T)
}

// FunctionalOption adapts a plain function to Option. A nil function
// or a nil target is ignored.
type FunctionalOption[T any] func(target *T)

// ApplyTo implements Option.
func (f FunctionalOption[T]) ApplyTo(target *T) {
	if f == nil || target == nil {
		return
	}

	f(target)
}
