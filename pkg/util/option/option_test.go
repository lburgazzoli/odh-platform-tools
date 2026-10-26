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

package option

import (
	"testing"

	. "github.com/onsi/gomega"
)

// probe is the configuration struct the tests target.
type probe struct {
	value int
}

// TestFunctionalOptionApplies pins that the adapter applies the
// wrapped function to the target.
func TestFunctionalOptionApplies(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var applied Option[probe] = FunctionalOption[probe](func(target *probe) {
		target.value = 7
	})

	target := probe{}
	applied.ApplyTo(&target)

	g.Expect(target.value).To(Equal(7))
}

// TestFunctionalOptionIgnoresNil pins that a nil function and a nil
// target are ignored rather than panicking.
func TestFunctionalOptionIgnoresNil(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	var nilFn FunctionalOption[probe]
	nilFn.ApplyTo(&probe{})

	target := probe{}
	FunctionalOption[probe](func(*probe) {}).ApplyTo(nil)

	g.Expect(target.value).To(Equal(0))
}
