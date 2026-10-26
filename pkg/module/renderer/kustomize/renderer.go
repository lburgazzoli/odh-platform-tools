// Package kustomize builds the native manifest-kit Kustomize renderer
// for Kustomize-based module definitions. Construction is its whole
// job: the shared rendering service in the parent renderer package
// drives the returned renderer through the shared engine, ordering,
// and guard.
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

// New builds the native Kustomize renderer for the module described
// by definition, whose file was loaded from definitionFile: the
// declared path maps directly to the source path — resolved against
// the definition file's directory, with an empty path denoting the
// co-located layout — with no fallback to a default/ overlay, and the
// entry kustomization is always the one rendered — the renderer's own
// loader selects the kustomization file name. Load restrictions stay
// at the renderer's RootOnly default, and no renderer-added content
// hash, source annotations, or managed-by label is applied,
// preserving the old framework engine's effective metadata.
//
// A nonempty namespace registers the renderer's real builtin
// namespace transformer plugin — configured exactly as the operator's
// Kustomize render action configures it — applying the namespace to
// namespaced objects, RoleBinding and ClusterRoleBinding subjects,
// and cross-object references; cluster-scoped objects keep their
// scope. An empty namespace registers no plugin at all, so the
// offline lint path renders exactly like a plain build.
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
		// The explicit flags preserve the old framework engine's
		// effective metadata: no renderer-added content hash or
		// source annotations, where the native renderer would
		// default the content hash to true.
		kustomize.WithContentHash(false),
		kustomize.WithSourceAnnotations(false),
	}

	// An empty namespace is the no-op: no plugin is registered, so
	// the zero options render exactly like the offline lint path.
	if namespace != "" {
		nativeOptions = append(nativeOptions, kustomize.WithPlugin(namespacePlugin(namespace)))
	}

	native, err := kustomize.New([]kustomize.Source{entry}, nativeOptions...)
	if err != nil {
		return nil, fmt.Errorf("kustomize render for module %s: %w", definition.Name, err)
	}

	return native, nil
}

// namespacePlugin returns the builtin Kustomize namespace transformer
// configured exactly as the operator's framework render action
// configures it (framework/render/kustomize/plugins.go:namespaceApplierPlugin):
// metadata/namespace and both RBAC binding subject kinds are created
// when absent, the webhook service references and the CRD conversion
// webhook service reference are rewritten only when already present,
// and every ServiceAccount subject receives the namespace. This ports
// the unexported constructor's configuration, not a new transformer
// implementation; cluster-scoped kinds without a namespace field spec
// are excluded by the builtins themselves.
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
