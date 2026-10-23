package adapt

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	fwactions "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions"
	fwtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"

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

// InjectModuleEnv returns the framework pipeline action porting the
// operator's injectModuleEnv (internal/controller/modules/modules_controller_actions_inject_env.go):
// it mutates Deployment resources in rr.Resources to inject the
// resolved controller image and the RELATED_IMAGE_*,
// APPLICATIONS_NAMESPACE, MONITORING_NAMESPACE, and platform identity
// env vars into the target container of the module controller
// Deployment.
//
// The operator aggregates every enabled module into a request-carried
// ModuleEnvInjection and reads image values from the process
// environment at injection time. This port is single-module — the
// module descriptor and the resolved image snapshot (the images
// service output) are supplied at construction — and the configured
// controller image may be a literal pipeline-built image as well as a
// whole {{NAME}} reference.
func InjectModuleEnv(
	definition v1alpha1.PlatformModuleDefinition,
	resolvedImages map[string]string,
	applicationsNamespace string,
	monitoringNamespace string,
	platformType string,
) fwactions.Fn {
	injection := newEnvInjection(definition, resolvedImages, applicationsNamespace, monitoringNamespace, platformType)

	return func(ctx context.Context, rr *fwtypes.ReconciliationRequest) error {
		log := logf.FromContext(ctx)

		for i := range rr.Resources {
			if !isDeployment(&rr.Resources[i]) {
				continue
			}

			if err := injection.injectEnvVarsIntoDeployment(log, &rr.Resources[i]); err != nil {
				log.Error(err, "failed to inject env vars into Deployment",
					"deployment", rr.Resources[i].GetName(),
					"deploymentNamespace", rr.Resources[i].GetNamespace(),
				)

				return err
			}
		}

		return nil
	}
}

// envInjection is the construction-time projection of one module's
// definition and runtime image snapshot onto the operator's
// ModuleEnvInjection inputs: the deployment name falls back to the
// module name, the container name to the operator manager default,
// and the extra env list becomes a map where later duplicates win and
// a nil value projects to the empty string. The controller image is
// resolved once — literal or whole {{NAME}} token. It is unexported:
// the package's visible surface is the action constructors alone.
type envInjection struct {
	deploymentName        string
	containerName         string
	initContainerName     string
	image                 string
	imageRef              string
	relatedImages         []string
	extraEnv              map[string]string
	images                map[string]string
	applicationsNamespace string
	monitoringNamespace   string
	platformType          string
}

// newEnvInjection projects the module descriptor and runtime inputs.
func newEnvInjection(
	definition v1alpha1.PlatformModuleDefinition,
	resolvedImages map[string]string,
	applicationsNamespace string,
	monitoringNamespace string,
	platformType string,
) *envInjection {
	injection := &envInjection{
		images:                resolvedImages,
		applicationsNamespace: applicationsNamespace,
		monitoringNamespace:   monitoringNamespace,
		platformType:          platformType,
		relatedImages:         definition.Spec.RelatedImages,
	}

	deployment := definition.Spec.Controller.Deployment

	injection.deploymentName = deployment.Name
	if injection.deploymentName == "" {
		// The operator falls back to the Helm release name, which for
		// this tool is always the module's metadata.name.
		injection.deploymentName = definition.Name
	}

	if container := deployment.Container; container != nil {
		injection.containerName = container.Name
		injection.imageRef = container.Image
		injection.image = images.ResolveImage(container.Image, resolvedImages)

		injection.extraEnv = make(map[string]string, len(container.Env))
		for _, env := range container.Env {
			value := ""
			if env.Value != nil {
				value = *env.Value
			}

			injection.extraEnv[env.Name] = value
		}
	}

	if init := deployment.InitContainer; init != nil {
		injection.initContainerName = init.Name
	}

	return injection
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
func (inj *envInjection) injectEnvVarsIntoDeployment(log logr.Logger, obj *unstructured.Unstructured) error {
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
	if inj.deploymentName != deployName {
		return nil
	}

	targetName := inj.containerName
	if targetName == "" {
		targetName = v1alpha1.DefaultMainContainerName
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

	if inj.image != "" {
		container["image"] = inj.image
		injected++
		log.V(3).Info("overriding controller image",
			"deployment", deployName, "container", targetName, "image", inj.imageRef)

		initInjected, err := inj.injectInitContainerImage(log, obj, inj.image, deployName)
		if err != nil {
			return err
		}
		injected += initInjected
	} else if inj.imageRef != "" {
		log.V(1).Info("controller image unresolved, keeping chart default",
			"image", inj.imageRef, "deployment", deployName)
	}

	existingEnv, _ := container["env"].([]any)

	// Injection precedence mirrors the operator: related images, then
	// explicit extra env, then the nonempty platform env. New entries
	// append in the definition's list order for related images and in
	// sorted key order for the extra-env map, keeping CLI output
	// stable without changing the operator's semantic precedence.
	for _, relImg := range inj.relatedImages {
		val := inj.images[relImg]
		if val == "" {
			log.V(1).Info("RELATED_IMAGE env var not set, skipping",
				"envVar", relImg, "deployment", deployName)
			continue
		}

		if setOrOverrideEnv(&existingEnv, relImg, val) {
			injected++
		}
	}

	for _, name := range slices.Sorted(maps.Keys(inj.extraEnv)) {
		if setOrOverrideEnv(&existingEnv, name, inj.extraEnv[name]) {
			injected++
		}
	}

	if inj.applicationsNamespace != "" {
		if setOrOverrideEnv(&existingEnv, applicationsNamespaceEnv, inj.applicationsNamespace) {
			injected++
		}
	}

	if inj.monitoringNamespace != "" {
		if setOrOverrideEnv(&existingEnv, monitoringNamespaceEnv, inj.monitoringNamespace) {
			injected++
		}
	}

	if inj.platformType != "" {
		if setOrOverrideEnv(&existingEnv, platformTypeEnv, inj.platformType) {
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
func (inj *envInjection) injectInitContainerImage(
	log logr.Logger,
	obj *unstructured.Unstructured,
	img string,
	deployName string,
) (int, error) {
	if inj.initContainerName == "" {
		return 0, nil
	}

	initContainers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "initContainers")
	if err != nil {
		return 0, err
	}
	if !found || len(initContainers) == 0 {
		log.V(1).Info("no initContainers in Deployment, skipping init container image injection",
			"deployment", deployName)
		return 0, nil
	}

	idx := findNamedContainer(initContainers, inj.initContainerName)
	if idx < 0 {
		return 0, fmt.Errorf("init container %q not found in Deployment %s", inj.initContainerName, deployName)
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
		"deployment", deployName, "initContainer", inj.initContainerName)

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
