package adapt

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	fwactions "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions"
	fwtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// Platform config ConfigMap naming and data keys, mirroring the
// operator's constants exactly.
const (
	// platformConfigPrefix is prepended to the module name to form
	// the ConfigMap name: odh-<modulename>-config.
	platformConfigPrefix = "odh-"

	// platformConfigSuffix is appended to the module name.
	platformConfigSuffix = "-config"

	// platformVersionKey is the data key containing the platform
	// version. This is platform-managed and reconciled back if
	// modified externally.
	platformVersionKey = "platformVersion"

	certManagerIssuerRefNameKey     = "CERT_MANAGER_ISSUER_REF_NAME"
	certManagerIssuerRefKindKey     = "CERT_MANAGER_ISSUER_REF_KIND"
	certManagerCASecretNameKey      = "CERT_MANAGER_CA_SECRET_NAME"      //nolint:gosec // ConfigMap key name, not a credential.
	certManagerCASecretNamespaceKey = "CERT_MANAGER_CA_SECRET_NAMESPACE" //nolint:gosec // ConfigMap key name, not a credential.
	certManagerIstioCACertPathKey   = "CERT_MANAGER_ISTIO_CA_CERT_PATH"
)

// platformManagedCertManagerKeys are removed from an existing module
// ConfigMap before the platform keys are merged. The operator manages
// them for XKS bootstrap; this tool never sets them but removes stale
// ones exactly as upstream does.
var platformManagedCertManagerKeys = []string{
	certManagerIssuerRefNameKey,
	certManagerIssuerRefKindKey,
	certManagerCASecretNameKey,
	certManagerCASecretNamespaceKey,
	certManagerIstioCACertPathKey,
}

// InjectPlatformConfig returns the framework pipeline action porting
// the operator's injectPlatformConfig (internal/controller/modules/modules_controller_actions_platform_config.go)
// for a single always-enabled module: the odh-<module>-config
// ConfigMap is created in the applications namespace, or the
// platform-managed keys are merged into an existing ConfigMap of the
// same name rendered by the module. Existing module-owned data keys
// are preserved. The platform version is read from rr.Release.Version,
// exactly where upstream reads it.
//
// Upstream additionally seeds cert-manager bootstrap keys on XKS
// platforms; that bootstrap is a platform concern with no
// one-shot-install analogue, so this port never sets them and only
// removes stale ones.
func InjectPlatformConfig(
	definition v1alpha1.PlatformModuleDefinition,
	applicationsNamespace string,
) fwactions.Fn {
	return func(ctx context.Context, rr *fwtypes.ReconciliationRequest) error {
		log := logf.FromContext(ctx)

		platformVersion := rr.Release.Version.String()

		existingCMs := indexConfigMapsByName(rr.Resources)

		name := definition.Name
		cmName := platformConfigName(name)

		ns := applicationsNamespace

		if idx, ok := existingCMs[cmName]; ok {
			log.V(1).Info("merging platform config into existing ConfigMap",
				"module", name, "configmap", cmName)
			mergePlatformKeys(&rr.Resources[idx], platformVersion)

			return nil
		}

		log.V(1).Info("creating platform config ConfigMap",
			"module", name, "configmap", cmName)

		cm := buildPlatformConfigMap(cmName, ns, platformVersion)

		u, err := fwresources.ToUnstructured(cm)
		if err != nil {
			return fmt.Errorf("converting platform config ConfigMap for %s: %w", name, err)
		}

		rr.Resources = append(rr.Resources, *u)

		return nil
	}
}

// platformConfigName returns the well-known ConfigMap name for a
// module, porting the operator's PlatformConfigName.
func platformConfigName(moduleName string) string {
	return platformConfigPrefix + moduleName + platformConfigSuffix
}

// buildPlatformConfigMap ports the operator's
// buildPlatformConfigMap without the XKS extra params.
func buildPlatformConfigMap(name string, namespace string, platformVersion string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string]string{
			platformVersionKey: platformVersion,
		},
	}
}

// mergePlatformKeys ports the operator's mergePlatformKeys: existing
// module-owned keys are preserved, stale platform-managed keys are
// removed, and the platform version is enforced. A missing or
// malformed (non-string) data block is replaced, exactly as upstream's
// ignored error projects nil into a fresh map.
func mergePlatformKeys(u *unstructured.Unstructured, platformVersion string) {
	data, _, _ := unstructured.NestedStringMap(u.Object, "data")
	if data == nil {
		data = make(map[string]string)
	}

	for _, k := range platformManagedCertManagerKeys {
		delete(data, k)
	}

	data[platformVersionKey] = platformVersion

	_ = unstructured.SetNestedStringMap(u.Object, data, "data")
}

// indexConfigMapsByName ports the operator's
// indexConfigMapsByName: ConfigMaps are matched by kind and name
// only — no group, version, or namespace — and a later resource with
// the same name wins.
func indexConfigMapsByName(resources []unstructured.Unstructured) map[string]int {
	idx := make(map[string]int)
	for i := range resources {
		if resources[i].GetKind() == "ConfigMap" {
			idx[resources[i].GetName()] = i
		}
	}
	return idx
}
