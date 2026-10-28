// Package adapt injects platform configuration and bridges framework ownership.
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

	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s/deployment"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/images"
)

const (
	applicationsNamespaceEnv = "APPLICATIONS_NAMESPACE"
	monitoringNamespaceEnv   = "MONITORING_NAMESPACE"
	platformTypeEnv          = "ODH_MODULE_OPERATOR_PLATFORM_TYPE"
)

// EnvInjector adapts matching controller Deployments with resolved images and
// environment variables. Inputs are read-only; typed conversion may drop
// unknown Deployment fields.
type EnvInjector struct {
	// Definition is the loaded, defaulted definition.
	Definition *toolApi.PlatformModuleDefinition

	// Images contains resolved image sources.
	Images map[string]string

	// Namespace sets APPLICATIONS_NAMESPACE when nonempty.
	Namespace string

	// MonitoringNamespace sets MONITORING_NAMESPACE when nonempty.
	MonitoringNamespace string

	// PlatformType sets ODH_MODULE_OPERATOR_PLATFORM_TYPE when nonempty.
	PlatformType string
}

var _ enginetypes.PostRenderer = (*EnvInjector)(nil).Run

// Run adapts matching Deployments in place. A missing target or container,
// or an unresolved required image, returns an error and nil output.
func (inj *EnvInjector) Run(
	ctx context.Context,
	resources []unstructured.Unstructured,
) ([]unstructured.Unstructured, error) {
	log := logf.FromContext(ctx)

	matched := false
	for i := range resources {
		if !inj.Definition.IsControllerDeployment(resources[i].GroupVersionKind(), resources[i].GetName()) {
			continue
		}
		matched = true

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
	if !matched {
		return nil, fmt.Errorf("no rendered apps/v1 Deployment matched controller %q", inj.Definition.ControllerDeploymentName())
	}

	return resources, nil
}

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

// Later values win: related images, explicit env, then platform env.
// Explicit env keys are sorted for deterministic output.
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

	if inj.Namespace != "" {
		envVars = append(envVars, corev1.EnvVar{Name: applicationsNamespaceEnv, Value: inj.Namespace})
	}

	if inj.MonitoringNamespace != "" {
		envVars = append(envVars, corev1.EnvVar{Name: monitoringNamespaceEnv, Value: inj.MonitoringNamespace})
	}

	if inj.PlatformType != "" {
		envVars = append(envVars, corev1.EnvVar{Name: platformTypeEnv, Value: inj.PlatformType})
	}

	return envVars
}
