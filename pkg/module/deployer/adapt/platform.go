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

const (
	platformConfigPrefix = "odh-"

	platformConfigSuffix = "-config"

	platformVersionKey = "platformVersion"

	certManagerIssuerRefNameKey     = "CERT_MANAGER_ISSUER_REF_NAME"
	certManagerIssuerRefKindKey     = "CERT_MANAGER_ISSUER_REF_KIND"
	certManagerCASecretNameKey      = "CERT_MANAGER_CA_SECRET_NAME"      //nolint:gosec // ConfigMap key name, not a credential.
	certManagerCASecretNamespaceKey = "CERT_MANAGER_CA_SECRET_NAMESPACE" //nolint:gosec // ConfigMap key name, not a credential.
	certManagerIstioCACertPathKey   = "CERT_MANAGER_ISTIO_CA_CERT_PATH"
)

// Remove stale keys reserved for the operator's xKS bootstrap.
var platformManagedCertManagerKeys = []string{
	certManagerIssuerRefNameKey,
	certManagerIssuerRefKindKey,
	certManagerCASecretNameKey,
	certManagerCASecretNamespaceKey,
	certManagerIstioCACertPathKey,
}

// PlatformConfigInjector creates or merges odh-<module>-config in Namespace.
// The last exact GVK/namespace/name match wins. Existing data is preserved
// except for platform-managed keys; typed conversion may drop unknown fields.
type PlatformConfigInjector struct {
	// Definition supplies the module name.
	Definition *toolApi.PlatformModuleDefinition

	// Namespace is the ConfigMap's namespace.
	Namespace string

	// PlatformVersion is the canonical parsed version.
	PlatformVersion string
}

var _ enginetypes.PostRenderer = (*PlatformConfigInjector)(nil).Run

// Run updates the last matching ConfigMap or appends one when absent.
func (inj *PlatformConfigInjector) Run(
	_ context.Context,
	resources []unstructured.Unstructured,
) ([]unstructured.Unstructured, error) {
	name := inj.Definition.Name

	cm := &corev1.ConfigMap{}
	cm.SetGroupVersionKind(k8s.ConfigMap)
	cm.Name = platformConfigPrefix + name + platformConfigSuffix
	cm.Namespace = inj.Namespace

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
