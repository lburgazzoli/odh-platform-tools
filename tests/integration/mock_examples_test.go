//go:build integration

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

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	toolapi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/adapt"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/images"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

const mockExampleAPIGroup = "components.platform.opendatahub.io"
const mockExampleAPIVersion = mockExampleAPIGroup + "/v1alpha1"
const mockExampleNamespace = "run-namespace"
const mockExampleImageKey = "RELATED_IMAGE_MOCK_CONTROLLER"
const mockExampleImage = "quay.io/lburgazzoli/odh-platform-tools:dev"

func TestMockExamplePackagesUseScopedRBACAndSeparateInstances(t *testing.T) {
	for _, example := range []struct {
		backend  string
		source   string
		kind     string
		resource string
	}{
		{backend: "helm", source: "chart", kind: "HelmModule", resource: "helmmodules"},
		{backend: "kustomize", kind: "KustomizeModule", resource: "kustomizemodules"},
	} {
		t.Run(example.backend, func(t *testing.T) {
			g := NewWithT(t)
			ctx := t.Context()
			root := filepath.Join("..", "..", "examples")
			definitionFile := filepath.Join(root, example.backend, example.source, "module.yaml")
			instanceName := "default-" + example.backend + "-module"
			configMapName := "odh-" + example.backend + "-module-config"

			definition, err := loader.LoadFromPath(ctx, definitionFile)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(definition.Name).To(Equal(example.backend + "-module"))
			g.Expect(definition.Spec.Instance.Name).To(Equal(instanceName))
			g.Expect(definition.Spec.Instance.Type).To(Equal(toolapi.ModuleType{
				Group: mockExampleAPIGroup, Version: "v1alpha1", Kind: example.kind,
			}))

			lintOutput, err := exec.CommandContext(ctx, cliBinary, "module", "lint", "--config", definitionFile).CombinedOutput()
			g.Expect(err).NotTo(HaveOccurred(), "%s", lintOutput)
			g.Expect(string(lintOutput)).To(ContainSubstring(": pass\n"))

			resources, err := renderer.Render(ctx, definition, definitionFile, renderer.WithNamespace(mockExampleNamespace))
			g.Expect(err).NotTo(HaveOccurred())
			byKind := make(map[string]unstructured.Unstructured, len(resources))
			for _, resource := range resources {
				g.Expect(byKind).NotTo(HaveKey(resource.GetKind()), "expected one %s", resource.GetKind())
				byKind[resource.GetKind()] = resource
			}
			g.Expect(byKind).To(HaveLen(7))
			g.Expect(byKind).NotTo(HaveKey(example.kind))

			instanceYAML, err := os.ReadFile(filepath.Join(root, example.backend, "instance.yaml"))
			g.Expect(err).NotTo(HaveOccurred())
			instances, err := k8s.DecodeResources(instanceYAML)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(instances).To(HaveLen(1))
			g.Expect(instances[0].GroupVersionKind()).To(Equal(definition.Spec.Instance.Type.GroupVersionKind()))
			g.Expect(instances[0].GetName()).To(Equal(instanceName))
			g.Expect(instances[0].GetNamespace()).To(BeEmpty())

			crd := mockExampleObject[apiextensionsv1.CustomResourceDefinition](t, byKind["CustomResourceDefinition"])
			g.Expect(crd.Name).To(Equal(example.resource + "." + mockExampleAPIGroup))
			g.Expect(crd.Namespace).To(BeEmpty())
			g.Expect(crd.Spec.Group).To(Equal(mockExampleAPIGroup))
			g.Expect(crd.Spec.Scope).To(Equal(apiextensionsv1.ClusterScoped))
			g.Expect(crd.Spec.Names.Kind).To(Equal(example.kind))
			g.Expect(crd.Spec.Names.Plural).To(Equal(example.resource))
			g.Expect(crd.Spec.Names.ListKind).To(Equal(example.kind + "List"))
			g.Expect(crd.Spec.Versions).To(HaveLen(1))
			version := crd.Spec.Versions[0]
			g.Expect(version).To(SatisfyAll(
				HaveField("Name", "v1alpha1"),
				HaveField("Served", true),
				HaveField("Storage", true),
				HaveField("Subresources.Status", Not(BeNil())),
				HaveField("Schema.OpenAPIV3Schema.XValidations", ContainElement(HaveField("Rule", "self.metadata.name == '"+instanceName+"'"))),
			))
			g.Expect(byKind["CustomResourceDefinition"].Object).To(SatisfyAll(
				jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.status.properties | .phase.type == "string"`),
				jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.status.properties | .observedGeneration.format == "int64"`),
				jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.status.properties | .conditions.items.properties | (["type", "status", "reason", "message", "severity", "observedGeneration", "lastTransitionTime"] - keys) | length == 0`),
				jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.status.properties | .conditions.items.properties.lastTransitionTime.format == "date-time"`),
				jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.status.properties | .releases.items.properties | .name.type == "string"`),
				jq.Match(`.spec.versions[0].schema.openAPIV3Schema.properties.status.properties | .releases.items.properties | .version.type == "string"`),
			))

			deployment := mockExampleObject[appsv1.Deployment](t, byKind["Deployment"])
			g.Expect(deployment.Name).To(Equal(definition.ControllerDeploymentName()))
			g.Expect(deployment.Name).To(Equal("mock-controller"))
			g.Expect(deployment.Namespace).To(Equal(mockExampleNamespace))
			g.Expect(deployment.Spec.Replicas).To(gstruct.PointTo(Equal(int32(1))))
			g.Expect(deployment.Spec.Strategy.Type).To(Equal(appsv1.RecreateDeploymentStrategyType))
			g.Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(1))
			manager := deployment.Spec.Template.Spec.Containers[0]
			g.Expect(manager.Name).To(Equal(definition.Spec.Controller.Deployment.Container.Name))
			g.Expect(manager.Image).To(Equal(mockExampleImage))
			g.Expect(manager.Command).To(BeEmpty())
			g.Expect(manager.Args).To(Equal([]string{
				"module", "mock", "--api-version=" + mockExampleAPIVersion,
				"--kind=" + example.kind, "--platform-configmap=" + configMapName,
			}))
			g.Expect(byKind["Deployment"].Object).To(SatisfyAll(
				jq.Match(`.spec.template.spec.securityContext.runAsNonRoot`),
				jq.Match(`.spec.template.spec.containers[0].securityContext.allowPrivilegeEscalation == false`),
				jq.Match(`.spec.template.spec.containers[0].ports[0].containerPort == 8081`),
				jq.Match(`.spec.template.spec.containers[0].livenessProbe.httpGet == {"path": "/healthz", "port": "probes"}`),
				jq.Match(`.spec.template.spec.containers[0].readinessProbe.httpGet == {"path": "/readyz", "port": "probes"}`),
				jq.Match(`.spec.template.spec.containers[0].env | any(.name == "POD_NAMESPACE" and .valueFrom.fieldRef.fieldPath == "metadata.namespace")`),
			))

			serviceAccount := byKind["ServiceAccount"]
			g.Expect(deployment.Spec.Template.Spec.ServiceAccountName).To(Equal(serviceAccount.GetName()))
			g.Expect(serviceAccount.GetNamespace()).To(Equal(mockExampleNamespace))
			subject := rbacv1.Subject{Kind: "ServiceAccount", Name: serviceAccount.GetName(), Namespace: mockExampleNamespace}

			role := mockExampleObject[rbacv1.Role](t, byKind["Role"])
			g.Expect(role.Namespace).To(Equal(mockExampleNamespace))
			g.Expect(role.Rules).To(ConsistOf(rbacv1.PolicyRule{
				APIGroups: []string{""}, Resources: []string{"configmaps"},
				ResourceNames: []string{configMapName}, Verbs: []string{"get", "list", "watch"},
			}))
			binding := mockExampleObject[rbacv1.RoleBinding](t, byKind["RoleBinding"])
			g.Expect(binding.Namespace).To(Equal(mockExampleNamespace))
			g.Expect(binding.RoleRef).To(Equal(rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}))
			g.Expect(binding.Subjects).To(ConsistOf(subject))

			clusterRole := mockExampleObject[rbacv1.ClusterRole](t, byKind["ClusterRole"])
			g.Expect(clusterRole.Namespace).To(BeEmpty())
			g.Expect(clusterRole.Rules).To(ConsistOf(
				rbacv1.PolicyRule{APIGroups: []string{mockExampleAPIGroup}, Resources: []string{example.resource}, Verbs: []string{"get", "list", "watch"}},
				rbacv1.PolicyRule{APIGroups: []string{mockExampleAPIGroup}, Resources: []string{example.resource + "/status"}, Verbs: []string{"get", "patch", "update"}},
				rbacv1.PolicyRule{APIGroups: []string{"events.k8s.io"}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
			))
			clusterBinding := mockExampleObject[rbacv1.ClusterRoleBinding](t, byKind["ClusterRoleBinding"])
			g.Expect(clusterBinding.Namespace).To(BeEmpty())
			g.Expect(clusterBinding.RoleRef).To(Equal(rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole.Name}))
			g.Expect(clusterBinding.Subjects).To(ConsistOf(subject))

			t.Run("deployment image source", func(t *testing.T) {
				g := NewWithT(t)
				t.Setenv(mockExampleImageKey, "")
				g.Expect(os.Unsetenv(mockExampleImageKey)).To(Succeed())
				imageFile := filepath.Join(root, "related-images.yaml")

				assertImage := func(want string) {
					t.Helper()
					resolved, err := images.Snapshot(imageFile, definition)
					g.Expect(err).NotTo(HaveOccurred())
					injector := adapt.EnvInjector{Definition: &definition, Images: resolved}
					adapted, err := injector.Run(t.Context(), []unstructured.Unstructured{byKind["Deployment"]})
					g.Expect(err).NotTo(HaveOccurred())
					deployment := mockExampleObject[appsv1.Deployment](t, adapted[0])
					g.Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal(want))
				}

				assertImage(mockExampleImage)
				const envImage = "quay.io/lburgazzoli/odh-platform-tools:local"
				t.Setenv(mockExampleImageKey, envImage)
				assertImage(envImage)
			})
		})
	}
}

func mockExampleObject[T any](t *testing.T, object unstructured.Unstructured) T {
	t.Helper()
	g := NewWithT(t)
	var typed T
	g.Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &typed)).To(Succeed())
	return typed
}
