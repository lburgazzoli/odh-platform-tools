// Package kustomize constructs native Kustomize renderers.
package kustomize

import (
	"fmt"
	"path/filepath"

	"sigs.k8s.io/kustomize/api/builtins" //nolint:staticcheck // The builtin namespace transformer is the configured one the framework uses.
	"sigs.k8s.io/kustomize/api/filters/namespace"
	"sigs.k8s.io/kustomize/api/resmap"
	kustomizetypes "sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/resid"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"
	kustomize "github.com/k8s-manifest-kit/renderer-kustomize/pkg"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/source"
)

// New resolves the source relative to definitionFile using RootOnly load
// restrictions. A nonempty namespace rewrites object and reference namespaces.
func New(
	definition toolApi.PlatformModuleDefinition,
	definitionFile string,
	namespace string,
) (enginetypes.Renderer, error) {
	var path string
	if manifests := definition.Spec.Manifests.Kustomize; manifests != nil {
		path = manifests.Path
	}

	entry := kustomize.Source{
		Path: source.ResolveEntry(filepath.Dir(definitionFile), path),
	}

	nativeOptions := []kustomize.RendererOption{
		// Keep renderer-added metadata disabled for operator parity.
		kustomize.WithContentHash(false),
		kustomize.WithSourceAnnotations(false),
	}

	if namespace != "" {
		nativeOptions = append(nativeOptions, kustomize.WithPlugin(namespacePlugin(namespace)))
	}

	native, err := kustomize.New([]kustomize.Source{entry}, nativeOptions...)
	if err != nil {
		return nil, fmt.Errorf("kustomize render for module %s: %w", definition.Name, err)
	}

	return native, nil
}

// Match the operator's unexported namespaceApplierPlugin configuration.
// Create object/RBAC namespace fields, but only rewrite existing webhook references.
func namespacePlugin(targetNamespace string) resmap.Transformer {
	return &builtins.NamespaceTransformerPlugin{
		ObjectMeta: kustomizetypes.ObjectMeta{
			Name:      "odh-namespace-plugin",
			Namespace: targetNamespace,
		},
		FieldSpecs: []kustomizetypes.FieldSpec{
			{
				Gvk:                resid.Gvk{},
				Path:               "metadata/namespace",
				CreateIfNotPresent: true,
			},
			{
				Gvk: resid.Gvk{
					Group: "rbac.authorization.k8s.io",
					Kind:  "ClusterRoleBinding",
				},
				Path:               "subjects/namespace",
				CreateIfNotPresent: true,
			},
			{
				Gvk: resid.Gvk{
					Group: "rbac.authorization.k8s.io",
					Kind:  "RoleBinding",
				},
				Path:               "subjects/namespace",
				CreateIfNotPresent: true,
			},
			{
				Gvk: resid.Gvk{
					Group: "admissionregistration.k8s.io",
					Kind:  "ValidatingWebhookConfiguration",
				},
				Path:               "webhooks/clientConfig/service/namespace",
				CreateIfNotPresent: false,
			},
			{
				Gvk: resid.Gvk{
					Group: "admissionregistration.k8s.io",
					Kind:  "MutatingWebhookConfiguration",
				},
				Path:               "webhooks/clientConfig/service/namespace",
				CreateIfNotPresent: false,
			},
			{
				Gvk: resid.Gvk{
					Group: "apiextensions.k8s.io",
					Kind:  "CustomResourceDefinition",
				},
				Path:               "spec/conversion/webhook/clientConfig/service/namespace",
				CreateIfNotPresent: false,
			},
		},
		UnsetOnly:              false,
		SetRoleBindingSubjects: namespace.AllServiceAccountSubjects,
	}
}
