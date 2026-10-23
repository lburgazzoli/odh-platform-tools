package adapt

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"

	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/images"
)

// Platform environment names injected into the module controller
// container, mirroring the operator's constants exactly.
const (
	applicationsNamespaceEnv = "APPLICATIONS_NAMESPACE"
	monitoringNamespaceEnv   = "MONITORING_NAMESPACE"
	platformTypeEnv          = "ODH_MODULE_OPERATOR_PLATFORM_TYPE"
)

// EnvInjector is the engine post-renderer porting the operator's
// injectModuleEnv (internal/controller/modules/modules_controller_actions_inject_env.go):
// it mutates Deployment resources to inject the resolved controller
// image and the RELATED_IMAGE_*, APPLICATIONS_NAMESPACE,
// MONITORING_NAMESPACE, and platform identity env vars into the target
// container of the module controller Deployment.
//
// The operator aggregates every enabled module into a request-carried
// ModuleEnvInjection and reads image values from the process
// environment at injection time. This port is single-module — the
// injector holds the loaded definition and the resolved image snapshot
// (the images service output) by pointer and reads their contents at
// injection time without mutating either — and the configured
// controller image may be a literal pipeline-built image as well as a
// whole {{NAME}} reference.
type EnvInjector struct {
	// Definition is the loaded module definition. Its Spec is read
	// directly; the runner owns the definition's lifetime.
	Definition *v1alpha1.PlatformModuleDefinition

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

// Run implements the engine post-renderer contract: Deployment
// resources are mutated in place and the same slice is returned. On any
// error no successful output is returned.
func (inj *EnvInjector) Run(
	ctx context.Context,
	resources []unstructured.Unstructured,
) ([]unstructured.Unstructured, error) {
	log := logf.FromContext(ctx)

	for i := range resources {
		if !isDeployment(&resources[i]) {
			continue
		}

		if err := inj.injectEnvVarsIntoDeployment(log, &resources[i]); err != nil {
			log.Error(err, "failed to inject env vars into Deployment",
				"deployment", resources[i].GetName(),
				"deploymentNamespace", resources[i].GetNamespace(),
			)

			return nil, err
		}
	}

	return resources, nil
}

// injectEnvVarsIntoDeployment ports the operator's
// injectEnvVarsIntoDeployment for the single module, mutating the
// controller Deployment in place.
//
// Upstream control flow is preserved deliberately, including its
// permissive branches: the containers of every Deployment are read
// before module matching, so a malformed or empty containers list
// errors even on a Deployment this module does not target; a missing
// target container skips injection without error; and a malformed
// existing env list is treated as empty and replaced by the injected
// entries once anything is injected.
//
// The fallback deployment and container names, the resolved image, and
// the last-wins extra-env map are transient calculations over the
// definition made here, not a mirrored long-lived projection.
func (inj *EnvInjector) injectEnvVarsIntoDeployment(log logr.Logger, obj *unstructured.Unstructured) error {
	deployment := inj.Definition.Spec.Controller.Deployment

	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil {
		return err
	}
	if !found || len(containers) == 0 {
		return fmt.Errorf("deployment %s has no containers", obj.GetName())
	}

	var injected int
	deployName := obj.GetName()

	// Upstream reads the containers of every Deployment before module
	// matching, exactly as preserved above.
	targetDeployment := deployment.Name
	if targetDeployment == "" {
		// The operator falls back to the Helm release name, which for
		// this tool is always the module's metadata.name.
		targetDeployment = inj.Definition.Name
	}
	if targetDeployment != deployName {
		return nil
	}

	targetName := v1alpha1.DefaultMainContainerName
	imageRef := ""
	image := ""
	var extraEnv map[string]string

	if container := deployment.Container; container != nil {
		if container.Name != "" {
			targetName = container.Name
		}
		imageRef = container.Image
		image = images.ResolveImage(container.Image, inj.Images)

		// The extra env list becomes a map where later duplicates win
		// and a nil value projects to the empty string.
		extraEnv = make(map[string]string, len(container.Env))
		for _, env := range container.Env {
			value := ""
			if env.Value != nil {
				value = *env.Value
			}

			extraEnv[env.Name] = value
		}
	}

	idx := findNamedContainer(containers, targetName)
	if idx < 0 {
		log.Error(nil, "target container not found in Deployment, skipping env injection",
			"deployment", deployName, "deploymentNamespace", obj.GetNamespace(), "container", targetName)
		return nil
	}

	container, ok := containers[idx].(map[string]any)
	if !ok {
		return nil
	}

	if image != "" {
		container["image"] = image
		injected++
		log.V(3).Info("overriding controller image",
			"deployment", deployName, "container", targetName)

		initInjected, err := inj.injectInitContainerImage(log, obj, image, deployName)
		if err != nil {
			return err
		}
		injected += initInjected
	} else if imageRef != "" {
		log.V(1).Info("controller image unresolved, keeping chart default",
			"deployment", deployName)
	}

	existingEnv, _ := container["env"].([]any)

	// Injection precedence mirrors the operator: related images, then
	// explicit extra env, then the nonempty platform env. New entries
	// append in the definition's list order for related images and in
	// sorted key order for the extra-env map, keeping CLI output
	// stable without changing the operator's semantic precedence.
	for _, relImg := range inj.Definition.Spec.RelatedImages {
		val := inj.Images[relImg]
		if val == "" {
			log.V(1).Info("RELATED_IMAGE env var not set, skipping",
				"envVar", relImg, "deployment", deployName)
			continue
		}

		if setOrOverrideEnv(&existingEnv, relImg, val) {
			injected++
		}
	}

	for _, name := range slices.Sorted(maps.Keys(extraEnv)) {
		if setOrOverrideEnv(&existingEnv, name, extraEnv[name]) {
			injected++
		}
	}

	if inj.ApplicationsNamespace != "" {
		if setOrOverrideEnv(&existingEnv, applicationsNamespaceEnv, inj.ApplicationsNamespace) {
			injected++
		}
	}

	if inj.MonitoringNamespace != "" {
		if setOrOverrideEnv(&existingEnv, monitoringNamespaceEnv, inj.MonitoringNamespace) {
			injected++
		}
	}

	if inj.PlatformType != "" {
		if setOrOverrideEnv(&existingEnv, platformTypeEnv, inj.PlatformType) {
			injected++
		}
	}

	container["env"] = existingEnv
	containers[idx] = container

	if injected == 0 {
		return nil
	}

	if err := unstructured.SetNestedSlice(obj.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		return err
	}

	log.V(3).Info("injected env vars into module Deployment",
		"name", obj.GetName(),
		"count", injected,
	)

	return nil
}

// injectInitContainerImage ports the operator's
// injectInitContainerImage: an unconfigured init container name and an
// absent initContainers list are skips, while a configured name
// missing from a nonempty list is an error — reached only when the
// controller image resolved, since the caller injects the same
// nonempty image into main and init containers or neither.
func (inj *EnvInjector) injectInitContainerImage(
	log logr.Logger,
	obj *unstructured.Unstructured,
	img string,
	deployName string,
) (int, error) {
	init := inj.Definition.Spec.Controller.Deployment.InitContainer
	if init == nil {
		return 0, nil
	}
	initContainerName := init.Name

	initContainers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "initContainers")
	if err != nil {
		return 0, err
	}
	if !found || len(initContainers) == 0 {
		log.V(1).Info("no initContainers in Deployment, skipping init container image injection",
			"deployment", deployName)
		return 0, nil
	}

	idx := findNamedContainer(initContainers, initContainerName)
	if idx < 0 {
		return 0, fmt.Errorf("init container %q not found in Deployment %s", initContainerName, deployName)
	}

	ic, ok := initContainers[idx].(map[string]any)
	if !ok {
		return 0, nil
	}

	ic["image"] = img
	initContainers[idx] = ic

	if err := unstructured.SetNestedSlice(obj.Object, initContainers, "spec", "template", "spec", "initContainers"); err != nil {
		return 0, err
	}

	log.V(3).Info("overriding init container image",
		"deployment", deployName, "initContainer", initContainerName)

	return 1, nil
}

// isDeployment ports the operator's isDeployment: exact Deployment
// group/version/kind matching.
func isDeployment(obj *unstructured.Unstructured) bool {
	return obj.GroupVersionKind() == k8s.Deployment
}

// setOrOverrideEnv ports the operator's setOrOverrideEnv: it adds a
// new env var or overrides an existing one, removing any valueFrom,
// and reports whether a change was made. A matching entry whose value
// is already exact and carries no valueFrom is left untouched.
// Non-map entries and non-string names never match.
func setOrOverrideEnv(envSlice *[]any, name string, value string) bool {
	for i, e := range *envSlice {
		if em, ok := e.(map[string]any); ok {
			if n, ok := em["name"].(string); ok && n == name {
				_, hasValueFrom := em["valueFrom"]
				if em["value"] == value && !hasValueFrom {
					return false
				}
				em["value"] = value
				delete(em, "valueFrom")
				(*envSlice)[i] = em
				return true
			}
		}
	}

	*envSlice = append(*envSlice, map[string]any{
		"name":  name,
		"value": value,
	})
	return true
}

// findNamedContainer ports the operator's findNamedContainer: the
// index of the container with the given name, or -1.
func findNamedContainer(containers []any, targetName string) int {
	for i, c := range containers {
		if cm, ok := c.(map[string]any); ok {
			if name, ok := cm["name"].(string); ok && name == targetName {
				return i
			}
		}
	}

	return -1
}
