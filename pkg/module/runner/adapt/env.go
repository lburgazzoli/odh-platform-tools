// Package adapt adapts rendered module resources before deployment.
//
// It provides two engine post-renderers carrying the
// engine types.PostRenderer signature, which the runner registers on
// the shared rendering engine after the library apply-order
// post-renderer: EnvInjector injects the resolved controller image and
// the module environment into the module's controller Deployment, and
// PlatformConfigInjector creates or merges the per-module platform
// config ConfigMap. Both read only the runner-owned inputs — the
// module definition, the resolved image snapshot, the target
// namespaces, and the platform version — and do not modify them.
package adapt

import (
	"context"
	"fmt"
	"maps"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"
	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	k8s "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s/deployment"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/images"
)

// Platform environment names injected into the module controller
// container, mirroring the operator's constants exactly.
const (
	applicationsNamespaceEnv = "APPLICATIONS_NAMESPACE"
	monitoringNamespaceEnv   = "MONITORING_NAMESPACE"
	platformTypeEnv          = "ODH_MODULE_OPERATOR_PLATFORM_TYPE"
)

// EnvInjector is the engine post-renderer that mutates the module's
// controller Deployment to inject the resolved controller image and
// the RELATED_IMAGE_*, APPLICATIONS_NAMESPACE, MONITORING_NAMESPACE,
// and platform identity env vars into the target container.
//
// Only the exact Deployment group/version/kind whose name matches the
// configured controller (module-name fallback) is adapted, through a
// typed round-trip: the target is converted to an appsv1.Deployment,
// mutated, and converted back. A target with malformed typed fields
// fails the pass instead of being repaired, and unknown fields on an
// adapted target may be dropped; unrelated resources are never
// converted.
//
// The injector holds the loaded definition and the resolved image
// snapshot (the images service output) by pointer and reads their
// contents at injection time without mutating either — and the
// configured controller image may be a literal pipeline-built image as
// well as a whole {{NAME}} reference.
type EnvInjector struct {
	// Definition is the loaded module definition. Its Spec is read
	// directly; the runner owns the definition's lifetime.
	Definition *toolApi.PlatformModuleDefinition

	// Images is the resolved image snapshot, the images service output.
	Images map[string]string

	// ApplicationsNamespace is the installation namespace; when
	// nonempty it becomes the APPLICATIONS_NAMESPACE env var.
	ApplicationsNamespace string

	// MonitoringNamespace is the monitoring namespace; when nonempty
	// it becomes the MONITORING_NAMESPACE env var.
	MonitoringNamespace string

	// PlatformType is the platform identity; when nonempty it becomes
	// the ODH_MODULE_OPERATOR_PLATFORM_TYPE env var.
	PlatformType string
}

// The injector's Run method value is a real engine post-renderer.
var _ enginetypes.PostRenderer = (*EnvInjector)(nil).Run

// Run implements the engine post-renderer contract: targeted
// Deployment resources are adapted in place and the same slice is
// returned. On any error no successful output is returned.
func (inj *EnvInjector) Run(
	ctx context.Context,
	resources []unstructured.Unstructured,
) ([]unstructured.Unstructured, error) {
	log := logf.FromContext(ctx)

	target := inj.Definition.Spec.Controller.Deployment.Name
	if target == "" {
		// The operator falls back to the Helm release name, which for
		// this tool is always the module's metadata.name.
		target = inj.Definition.Name
	}

	for i := range resources {
		if resources[i].GroupVersionKind() != k8s.Deployment || resources[i].GetName() != target {
			continue
		}

		adapted, err := inj.adaptDeployment(ctx, &resources[i])
		if err != nil {
			log.Error(err, "failed to inject env vars into Deployment",
				"deployment", resources[i].GetName(),
				"deploymentNamespace", resources[i].GetNamespace(),
			)

			return nil, err
		}

		if adapted != nil {
			resources[i] = *adapted
		}
	}

	return resources, nil
}

// adaptDeployment converts the targeted Deployment to its typed form,
// delegates the container and init container mutations to their
// adapters, and returns the converted-back resource. The containers the
// definition names — the main container and a configured init
// container — must exist on the target, and their images must resolve:
// a missing container or an unresolved image fails the pass.
// A successfully customized target is always reserialized, even when
// no effective field value changed.
func (inj *EnvInjector) adaptDeployment(
	ctx context.Context,
	obj *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	typed := &appsv1.Deployment{}
	if err := fwresources.ObjectFromUnstructured(clientgoscheme.Scheme, obj, typed); err != nil {
		return nil, fmt.Errorf("deployment %s: %w", obj.GetName(), err)
	}

	if len(typed.Spec.Template.Spec.Containers) == 0 {
		return nil, fmt.Errorf("deployment %s has no containers", obj.GetName())
	}

	if err := inj.adaptContainer(ctx, typed); err != nil {
		return nil, err
	}

	if err := inj.adaptInitContainer(ctx, typed); err != nil {
		return nil, err
	}

	out, err := fwresources.ObjectToUnstructured(clientgoscheme.Scheme, typed)
	if err != nil {
		return nil, fmt.Errorf("deployment %s: %w", obj.GetName(), err)
	}

	return out, nil
}

// adaptContainer applies the main container customization: the named
// container must exist on the target, its image must resolve — a
// resolved image replaces the container's image — and the injected env
// vars are merged into its env list. The name comes defaulted from the
// loader; this adapter never defaults it.
func (inj *EnvInjector) adaptContainer(
	ctx context.Context,
	typed *appsv1.Deployment,
) error {
	container := inj.Definition.Spec.Controller.Deployment.Container

	if _, found := k8s.FindContainer(typed.Spec.Template.Spec.Containers, container.Name); !found {
		return fmt.Errorf("container %q not found in Deployment %s", container.Name, typed.Name)
	}

	image := images.ResolveImage(container.Image, inj.Images)
	if image == "" {
		return fmt.Errorf("container %q image unresolved in Deployment %s", container.Name, typed.Name)
	}

	if err := deployment.Customize(
		typed,
		deployment.WithContainerImage(container.Name, image),
		deployment.WithContainerEnv(container.Name, inj.injectedEnvVars(ctx)),
	); err != nil {
		return fmt.Errorf("deployment %s: %w", typed.Name, err)
	}

	return nil
}

// adaptInitContainer applies the init container customization: the
// configured init container must be named, must exist on the target,
// and its image — the one it declares, or the main container's when it
// declares none — must resolve; the injected env vars are merged into
// its env list the same way as the main container's. No init container
// configured means no mutation.
func (inj *EnvInjector) adaptInitContainer(
	ctx context.Context,
	typed *appsv1.Deployment,
) error {
	init := inj.Definition.Spec.Controller.Deployment.InitContainer
	if init == nil {
		return nil
	}
	if init.Name == "" {
		return fmt.Errorf("unnamed init container in Deployment %s", typed.Name)
	}

	if _, found := k8s.FindContainer(typed.Spec.Template.Spec.InitContainers, init.Name); !found {
		return fmt.Errorf("init container %q not found in Deployment %s", init.Name, typed.Name)
	}

	image := init.Image
	if image == "" {
		image = inj.Definition.Spec.Controller.Deployment.Container.Image
	}

	image = images.ResolveImage(image, inj.Images)
	if image == "" {
		return fmt.Errorf("init container %q image unresolved in Deployment %s", init.Name, typed.Name)
	}

	if err := deployment.Customize(
		typed,
		deployment.WithInitContainerImage(init.Name, image),
		deployment.WithInitContainerEnv(init.Name, inj.injectedEnvVars(ctx)),
	); err != nil {
		return fmt.Errorf("deployment %s: %w", typed.Name, err)
	}

	return nil
}

// injectedEnvVars resolves the env vars to inject into the target
// container, in precedence order: related images in descriptor order,
// explicit extra env in sorted key order, then the nonempty platform
// env. Later entries win over earlier ones once applied. Unresolved
// related images are skipped, and a nil descriptor env value projects
// to the empty string.
func (inj *EnvInjector) injectedEnvVars(ctx context.Context) []corev1.EnvVar {
	log := logf.FromContext(ctx)

	var envVars []corev1.EnvVar

	for _, relImg := range inj.Definition.Spec.RelatedImages {
		val := inj.Images[relImg]
		if val == "" {
			log.V(1).Info("RELATED_IMAGE env var not set, skipping",
				"envVar", relImg)
			continue
		}

		envVars = append(envVars, corev1.EnvVar{Name: relImg, Value: val})
	}

	if container := inj.Definition.Spec.Controller.Deployment.Container; container != nil {
		// The extra env list becomes a map where later duplicates win
		// and a nil value projects to the empty string.
		extraEnv := make(map[string]string, len(container.Env))
		for _, env := range container.Env {
			value := ""
			if env.Value != nil {
				value = *env.Value
			}

			extraEnv[env.Name] = value
		}

		for _, name := range slices.Sorted(maps.Keys(extraEnv)) {
			envVars = append(envVars, corev1.EnvVar{Name: name, Value: extraEnv[name]})
		}
	}

	if inj.ApplicationsNamespace != "" {
		envVars = append(envVars, corev1.EnvVar{Name: applicationsNamespaceEnv, Value: inj.ApplicationsNamespace})
	}

	if inj.MonitoringNamespace != "" {
		envVars = append(envVars, corev1.EnvVar{Name: monitoringNamespaceEnv, Value: inj.MonitoringNamespace})
	}

	if inj.PlatformType != "" {
		envVars = append(envVars, corev1.EnvVar{Name: platformTypeEnv, Value: inj.PlatformType})
	}

	return envVars
}
