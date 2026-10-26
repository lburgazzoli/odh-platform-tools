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

package source

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestResolveEntry(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	const root = "/modules/synthetic"

	// An authored relative path resolves against the definition
	// file's directory; an empty path — an omitted block or an
	// omitted path — resolves to the directory itself, the
	// co-located layout; an absolute path is honored as is.
	g.Expect(ResolveEntry(root, "./manifests/chart")).To(Equal("/modules/synthetic/manifests/chart"))
	g.Expect(ResolveEntry(root, "manifests/chart")).To(Equal("/modules/synthetic/manifests/chart"))
	g.Expect(ResolveEntry(root, "")).To(Equal("/modules/synthetic"))
	g.Expect(ResolveEntry(root, "/elsewhere/chart")).To(Equal("/elsewhere/chart"))
	g.Expect(ResolveEntry(root, "/elsewhere/chart/")).To(Equal("/elsewhere/chart"))
}
