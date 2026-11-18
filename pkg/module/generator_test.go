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

package module

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/generator"

	. "github.com/onsi/gomega"

	"sigs.k8s.io/yaml"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
)

// exampleSource resolves one of the checked-in example module
// sources.
func exampleSource(t *testing.T, name string) string {
	t.Helper()

	source, err := filepath.Abs(filepath.Join("..", "..", "examples", name))
	if err != nil {
		t.Fatalf("resolve example %s: %v", name, err)
	}

	return source
}

// TestNewGeneratorInfersHelmExample proves the composed service
// infers a complete definition from the checked-in Helm chart: the
// lowercase CRD kind names the module, the storage version supplies
// the GVK, and the controller fields come from the rendered
// Deployment.
func TestNewGeneratorInfersHelmExample(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result, err := NewGenerator().Run(context.Background(), generator.Options{
		SourceDir: exampleSource(t, filepath.Join("helm", "chart")),
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Output).To(Equal("stdout"))

	definition := loadGenerated(t, g, result.YAML)

	g.Expect(definition.Name).To(Equal("helmmodule"))
	g.Expect(definition.Spec.Instance.Type).To(Equal(toolApi.ModuleType{
		Group:   "components.platform.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "HelmModule",
	}))
	g.Expect(definition.Spec.Instance.Name).To(Equal("helmmodule"))
	g.Expect(definition.Spec.Manifests.Format).To(Equal(toolApi.FormatHelm))
	g.Expect(definition.Spec.Manifests.Helm.Path).To(Equal(filepath.Join("..", "..", "examples", "helm", "chart")))
	g.Expect(definition.Spec.Controller.Deployment.Name).To(Equal("mock-controller"))
	g.Expect(definition.Spec.Controller.Deployment.Container.Name).
		To(Equal(toolApi.DefaultMainContainerName))
	g.Expect(definition.Spec.Controller.Deployment.Container.Image).
		To(Equal("quay.io/lburgazzoli/odh-platform-tools:dev"))
}

// TestNewGeneratorInfersKustomizeExample proves the composed service
// infers a complete definition from the checked-in Kustomize tree.
func TestNewGeneratorInfersKustomizeExample(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	result, err := NewGenerator().Run(context.Background(), generator.Options{
		SourceDir: exampleSource(t, "kustomize"),
	})

	g.Expect(err).NotTo(HaveOccurred())

	definition := loadGenerated(t, g, result.YAML)

	g.Expect(definition.Name).To(Equal("kustomizemodule"))
	g.Expect(definition.Spec.Instance.Type).To(Equal(toolApi.ModuleType{
		Group:   "components.platform.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "KustomizeModule",
	}))
	g.Expect(definition.Spec.Manifests.Format).To(Equal(toolApi.FormatKustomize))
	g.Expect(definition.Spec.Controller.Deployment.Name).To(Equal("mock-controller"))
	g.Expect(definition.Spec.Controller.Deployment.Container.Image).
		To(Equal("quay.io/lburgazzoli/odh-platform-tools:dev"))
}

// TestNewGeneratorIsDeterministic proves repeated runs over the same
// source produce identical bytes.
func TestNewGeneratorIsDeterministic(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	action := NewGenerator()
	options := generator.Options{SourceDir: exampleSource(t, "kustomize")}

	first, err := action.Run(context.Background(), options)
	g.Expect(err).NotTo(HaveOccurred())

	second, err := action.Run(context.Background(), options)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(second.YAML).To(Equal(first.YAML))
}

// loadGenerated decodes a generated document with the existing
// loader, proving the emitted YAML is loadable as authored.
func loadGenerated(
	t *testing.T,
	g *WithT,
	document []byte,
) toolApi.PlatformModuleDefinition {
	t.Helper()

	var generic map[string]any
	g.Expect(yaml.Unmarshal(document, &generic)).To(Succeed())

	path := filepath.Join(t.TempDir(), "module.yaml")
	g.Expect(os.WriteFile(path, document, 0o600)).To(Succeed())

	definition, err := loader.LoadFromPath(context.Background(), path)
	g.Expect(err).NotTo(HaveOccurred())

	return definition
}
