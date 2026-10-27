package adapt

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	enginetypes "github.com/k8s-manifest-kit/engine/pkg/types"
	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s"
	"github.com/lburgazzoli/odh-platform-tools/pkg/k8s/configmap"
	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
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

// PlatformConfigInjector is the engine post-renderer that creates or
// merges the per-module platform config ConfigMap: the
// odh-<module>-config ConfigMap is created in the applications
// namespace, or the platform-managed keys are merged into the module's
// own ConfigMap of that name co-located in the applications namespace.
// Existing module-owned data keys are preserved.
//
// An existing ConfigMap matches on the full object identity —
// group/version/kind, namespace, and name, with the last exact match
// winning — and is merged through a typed round-trip: a
// malformed data block fails the render instead of being repaired,
// and unknown fields may be dropped. The deployer supplies the canonical
// parsed platform version string directly, so no release plumbing is
// carried.
type PlatformConfigInjector struct {
	// Definition is the loaded module definition. Its name forms the
	// ConfigMap name; the deployer owns the definition's lifetime.
	Definition *toolApi.PlatformModuleDefinition

	// ApplicationsNamespace is where the platform config ConfigMap is
	// created and where a module-rendered one is matched for merging.
	ApplicationsNamespace string

	// PlatformVersion is the canonical platform version string the
	// deployer parsed and supplies to both the injector and deploy.
	PlatformVersion string
}

// The injector's Run method value is a real engine post-renderer.
var _ enginetypes.PostRenderer = (*PlatformConfigInjector)(nil).Run

// Run implements the engine post-renderer contract: the platform
// config ConfigMap is built fresh and a rendered ConfigMap of the same
// identity — when one exists — is decoded over it, so the found one's
// data wins; the platform keys are merged once and the result replaces
// the match in place, or is appended when none matched. On error no
// successful output is returned.
func (inj *PlatformConfigInjector) Run(
	_ context.Context,
	resources []unstructured.Unstructured,
) ([]unstructured.Unstructured, error) {
	name := inj.Definition.Name

	cm := &corev1.ConfigMap{}
	cm.SetGroupVersionKind(k8s.ConfigMap)
	cm.Name = platformConfigPrefix + name + platformConfigSuffix
	cm.Namespace = inj.ApplicationsNamespace

	// One reverse search: the last resource of the created ConfigMap's
	// identity — k8s.Is compares group/version/kind, namespace, and
	// name — wins. Same-name lookalikes of another group/version/kind
	// or namespace are not candidates. The match is decoded over the
	// created ConfigMap through the typed round-trip: the found one's
	// identity and data replace the created one's, a malformed data
	// block fails the render instead of being repaired, and unknown
	// fields may be dropped.
	match := -1

	for i := len(resources) - 1; i >= 0; i-- {
		if k8s.Is(&resources[i], cm) {
			match = i
			break
		}
	}

	if match >= 0 {
		if err := fwresources.ObjectFromUnstructured(clientgoscheme.Scheme, &resources[match], cm); err != nil {
			return nil, fmt.Errorf("converting platform config ConfigMap for %s: %w", name, err)
		}
	}

	if err := configmap.Customize(
		cm,
		configmap.Remove(platformManagedCertManagerKeys...),
		configmap.WithData(platformVersionKey, inj.PlatformVersion),
	); err != nil {
		return nil, fmt.Errorf("customizing platform config ConfigMap for %s: %w", name, err)
	}

	u, err := fwresources.ObjectToUnstructured(clientgoscheme.Scheme, cm)

	switch {
	case err != nil:
		return nil, fmt.Errorf("converting platform config ConfigMap for %s: %w", name, err)
	case match == -1:
		resources = append(resources, *u)
	default:
		resources[match] = *u
	}

	return resources, nil
}
