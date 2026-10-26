// Package kustomize renders Kustomize-based module definitions
// through the direct manifest-kit Kustomize renderer driven by the
// shared engine.
package kustomize

import (
	"context"
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/kustomize/api/builtins" //nolint:staticcheck // The builtin namespace transformer is the configured one the framework uses.
	"sigs.k8s.io/kustomize/api/filters/namespace"
	"sigs.k8s.io/kustomize/api/resmap"
	kustomizetypes "sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/resid"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"
	kustomize "github.com/k8s-manifest-kit/renderer-kustomize/pkg"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// New builds the native Kustomize renderer for the module described
// by definition, whose file was loaded from definitionFile: the
// declared path maps directly to the source path with no fallback to
// a default/ overlay, and the entry kustomization is always the one
// rendered — the renderer's own loader selects the kustomization file
// name. Load restrictions stay at the renderer's RootOnly default,
// and no renderer-added content hash, source annotations, or
// managed-by label is applied, preserving the old framework engine's
// effective metadata.
//
// A nonempty namespace registers the renderer's real builtin
// namespace transformer plugin — configured exactly as the operator's
// Kustomize render action configures it — applying the namespace to
// namespaced objects, RoleBinding and ClusterRoleBinding subjects,
// and cross-object references; cluster-scoped objects keep their
// scope. An empty namespace registers no plugin at all, so the
// offline lint path renders exactly like a plain build.
func New(
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	namespace string,
) (enginetypes.Renderer, error) {
	return newNative(definition, definitionFile, namespace, nil)
}

// newNative is the native construction seam behind New: a non-nil
// fsys replaces the on-disk filesystem the native renderer would
// otherwise use. Production rendering always passes nil; the shared
// guard tests supply an in-memory filesystem so their fixtures never
// touch the operating-system disk.
func newNative(
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	namespace string,
	fsys filesys.FileSystem,
) (enginetypes.Renderer, error) {
	var path string
	if manifests := definition.Spec.Manifests.Kustomize; manifests != nil {
		path = manifests.Path
	}

	source := kustomize.Source{
		Path: renderer.ResolveEntry(filepath.Dir(definitionFile), path),
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

	if fsys != nil {
		nativeOptions = append(nativeOptions, kustomize.WithFileSystem(fsys))
	}

	native, err := kustomize.New([]kustomize.Source{source}, nativeOptions...)
	if err != nil {
		return nil, fmt.Errorf("kustomize render for module %s: %w", definition.Name, err)
	}

	return native, nil
}

// Render renders the module described by definition, whose file was
// loaded from definitionFile, through the direct Kustomize renderer
// with no runtime options, and returns the rendered objects.
//
// The renderer renders the configured entry directory directly with
// no cache and no client attached. The declared path maps directly to
// the source path with no namespace transformation and no fallback to
// a default/ overlay: the entry kustomization is always the one
// rendered, and the renderer's own loader selects the kustomization
// file name — kustomization.yaml, kustomization.yml, or Kustomization,
// never kustomization.json. Load restrictions stay at the renderer's
// RootOnly default, and no renderer-added content hash, source
// annotations, or managed-by label is applied, preserving the old
// framework engine's effective metadata. The shared engine's
// apply-order post-renderer supplies the deterministic pre-injection
// normalization.
//
// The returned list contains every object the engine rendered;
// callers filtering it must not assume they hold the raw kustomize
// build output. The shared payload guard runs on the output:
// instances of the definition's own module type, any
// PlatformModuleDefinition, and the persisted definition CRD fail
// the render, so no caller can receive them.
func Render(ctx context.Context, definition v1alpha1.PlatformModuleDefinition, definitionFile string) ([]unstructured.Unstructured, error) {
	return render(ctx, definition, definitionFile, renderer.Options{}, nil)
}

// RenderWithOptions renders the module with runtime options: the
// options' namespace is applied by the renderer's real builtin
// namespace transformer plugin — configured exactly as the operator's
// Kustomize render action configures it — to namespaced objects,
// RoleBinding and ClusterRoleBinding subjects, and cross-object
// references. Cluster-scoped objects keep their scope, and an empty
// namespace registers no plugin at all, so the zero options render
// exactly like the offline lint path.
func RenderWithOptions(
	ctx context.Context,
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	options renderer.Options,
) ([]unstructured.Unstructured, error) {
	return render(ctx, definition, definitionFile, options, nil)
}

// render is the implementation the public render entrypoints share.
// The filesystem seam is package-private: a nil fsys keeps the native
// renderer's on-disk default — the only behavior production callers
// get — while the guard tests supply an in-memory filesystem through
// the same native WithFileSystem option, exercising the real backend,
// engine, and guard with no disk writes. No global filesystem state or
// public option is introduced.
func render(
	ctx context.Context,
	definition v1alpha1.PlatformModuleDefinition,
	definitionFile string,
	options renderer.Options,
	fsys filesys.FileSystem,
) ([]unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Production rendering goes through the public constructor; only
	// the test seam with its own filesystem builds the native
	// renderer directly.
	var native enginetypes.Renderer
	var err error

	if fsys != nil {
		native, err = newNative(definition, definitionFile, options.Namespace, fsys)
	} else {
		native, err = New(definition, definitionFile, options.Namespace)
	}
	if err != nil {
		return nil, err
	}

	shared, err := renderer.NewEngine(native, options)
	if err != nil {
		return nil, fmt.Errorf("kustomize render for module %s: %w", definition.Name, err)
	}

	resources, err := shared.Render(ctx)
	if err != nil {
		return nil, fmt.Errorf("kustomize render for module %s: %w", definition.Name, err)
	}

	// The shared payload guard fails the render itself on reserved
	// objects; they are never silently dropped.
	if err := renderer.CheckReserved(definition, resources); err != nil {
		return nil, fmt.Errorf("kustomize render for module %s: %w", definition.Name, err)
	}

	// A render that finished after the context expired never becomes
	// successful output.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return resources, nil
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
