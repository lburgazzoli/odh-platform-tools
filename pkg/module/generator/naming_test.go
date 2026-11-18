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

package generator

import (
	"errors"
	"testing"

	. "github.com/onsi/gomega"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// TestInferredNameLowercasesKind proves the name derivation is
// lowercasing alone: no pluralization, splitting, or sanitization.
func TestInferredNameLowercasesKind(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	g.Expect(inferredName("FooBar")).To(Equal("foobar"))
	g.Expect(inferredName("KustomizeModule")).To(Equal("kustomizemodule"))
	g.Expect(inferredName("odh")).To(Equal("odh"))
}

// TestValidateExplicitNameEnforcesKubernetesNaming proves an explicit
// name is rejected as an options failure before any render when it is
// not a valid Kubernetes name, on either backend.
func TestValidateExplicitNameEnforcesKubernetesNaming(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "Foo", "a_b", "-lead", "trail-", "sp ace"} {
		t.Run("name "+name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			err := validateExplicitName(name, toolApi.FormatKustomize)

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue())
		})
	}
}

// TestValidateExplicitNameEnforcesReleaseNaming proves the stricter
// Helm release-name constraints apply only to Helm sources: a long
// lowercase name is usable for Kustomize but not as a release name.
func TestValidateExplicitNameEnforcesReleaseNaming(t *testing.T) {
	t.Parallel()

	long := "abcdefghijklmnopqrstuvwxyz012345abcdefghijklmnopqrstuvwxyz012345"

	g := NewWithT(t)

	g.Expect(validateExplicitName("demo-module", toolApi.FormatHelm)).To(Succeed())
	g.Expect(validateExplicitName("demo-module", toolApi.FormatKustomize)).To(Succeed())
	g.Expect(validateExplicitName(long, toolApi.FormatKustomize)).To(Succeed())

	err := validateExplicitName(long, toolApi.FormatHelm)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue())

	err = validateExplicitName("Demo", toolApi.FormatHelm)
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidGenerateOptions)).To(BeTrue())
}

// TestValidateInferredNameRequestsExplicitName proves an inferred
// name that cannot be used is a definition failure that asks for an
// explicit name, without bypassing the CRD validation that produced
// it.
func TestValidateInferredNameRequestsExplicitName(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	err := validateInferredName("FooBar", toolApi.FormatKustomize)

	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("pass an explicit name"))
}
