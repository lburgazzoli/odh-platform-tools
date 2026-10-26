package adapt

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"
	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	gvk "github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
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
		if resources[i].GroupVersionKind() != gvk.Deployment || resources[i].GetName() != target {
			continue
		}

		adapted, err := inj.adaptDeployment(log, &resources[i])
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
// applies the image and env mutations, and returns the converted-back
// resource. It returns a nil resource when nothing was injected or the
// target container is absent, leaving the caller's original object
// untouched — no-adaptation paths are never reserialized.
func (inj *EnvInjector) adaptDeployment(
	log logr.Logger,
	obj *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	deployment := &appsv1.Deployment{}
	if err := fwresources.ObjectFromUnstructured(clientgoscheme.Scheme, obj, deployment); err != nil {
		return nil, fmt.Errorf("deployment %s: %w", obj.GetName(), err)
	}

	podSpec := &deployment.Spec.Template.Spec
	if len(podSpec.Containers) == 0 {
		return nil, fmt.Errorf("deployment %s has no containers", obj.GetName())
	}

	descriptor := inj.Definition.Spec.Controller.Deployment

	targetName := toolApi.DefaultMainContainerName
	image := ""

	if container := descriptor.Container; container != nil {
		if container.Name != "" {
			targetName = container.Name
		}
		image = images.ResolveImage(container.Image, inj.Images)
	}

	idx := slices.IndexFunc(podSpec.Containers, func(c corev1.Container) bool {
		return c.Name == targetName
	})
	if idx < 0 {
		log.Error(nil, "target container not found in Deployment, skipping env injection",
			"deployment", obj.GetName(), "deploymentNamespace", obj.GetNamespace(), "container", targetName)
		return nil, nil
	}

	container := &podSpec.Containers[idx]

	var injected int

	if image != "" {
		container.Image = image
		injected++
		log.V(3).Info("overriding controller image",
			"deployment", obj.GetName(), "container", targetName)

		initInjected, err := inj.injectInitContainerImage(log, deployment, image)
		if err != nil {
			return nil, err
		}
		injected += initInjected
	} else if descriptor.Container != nil && descriptor.Container.Image != "" {
		log.V(1).Info("controller image unresolved, keeping chart default",
			"deployment", obj.GetName())
	}

	injected += injectEnvVars(container, inj.injectedEnvVars(log))

	if injected == 0 {
		return nil, nil
	}

	out, err := fwresources.ObjectToUnstructured(clientgoscheme.Scheme, deployment)
	if err != nil {
		return nil, fmt.Errorf("deployment %s: %w", obj.GetName(), err)
	}

	log.V(3).Info("injected env vars into module Deployment",
		"name", obj.GetName(),
		"count", injected,
	)

	return out, nil
}

// injectedEnvVars resolves the env vars to inject into the target
// container, in precedence order: related images in descriptor order,
// explicit extra env in sorted key order, then the nonempty platform
// env. Later entries win over earlier ones once applied. Unresolved
// related images are skipped, and a nil descriptor env value projects
// to the empty string.
func (inj *EnvInjector) injectedEnvVars(log logr.Logger) []corev1.EnvVar {
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

// injectEnvVars applies the env vars to the container's env list and
// reports how many entries changed.
func injectEnvVars(container *corev1.Container, envVars []corev1.EnvVar) int {
	var injected int

	for _, envVar := range envVars {
		if setEnv(&container.Env, envVar.Name, envVar.Value) {
			injected++
		}
	}

	return injected
}

// injectInitContainerImage injects the resolved image into the
// configured init container of the typed Deployment: an unconfigured
// name and an absent or empty initContainers list are skips, while a
// configured name missing from a nonempty list is an error — reached
// only when the controller image resolved, since the same nonempty
// image goes to the main and init containers or neither.
func (inj *EnvInjector) injectInitContainerImage(
	log logr.Logger,
	deployment *appsv1.Deployment,
	image string,
) (int, error) {
	init := inj.Definition.Spec.Controller.Deployment.InitContainer
	if init == nil {
		return 0, nil
	}

	initContainers := deployment.Spec.Template.Spec.InitContainers
	if len(initContainers) == 0 {
		log.V(1).Info("no initContainers in Deployment, skipping init container image injection",
			"deployment", deployment.Name)
		return 0, nil
	}

	idx := slices.IndexFunc(initContainers, func(c corev1.Container) bool {
		return c.Name == init.Name
	})
	if idx < 0 {
		return 0, fmt.Errorf("init container %q not found in Deployment %s", init.Name, deployment.Name)
	}

	initContainers[idx].Image = image

	log.V(3).Info("overriding init container image",
		"deployment", deployment.Name, "initContainer", init.Name)

	return 1, nil
}

// setEnv adds or overrides a typed env var on the container's list,
// clearing any valueFrom, and reports whether a change was made. The
// first entry with the name is updated; an entry already carrying the
// exact value and no valueFrom is left untouched.
func setEnv(env *[]corev1.EnvVar, name string, value string) bool {
	for i := range *env {
		if (*env)[i].Name == name {
			if (*env)[i].Value == value && (*env)[i].ValueFrom == nil {
				return false
			}

			(*env)[i].Value = value
			(*env)[i].ValueFrom = nil

			return true
		}
	}

	*env = append(*env, corev1.EnvVar{Name: name, Value: value})

	return true
}
