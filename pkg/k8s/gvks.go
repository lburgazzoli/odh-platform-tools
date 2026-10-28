// Package k8s provides shared Kubernetes resource helpers.
package k8s

import (
	"k8s.io/apimachinery/pkg/runtime/schema"

	framework "github.com/opendatahub-io/odh-platform-utilities/framework/cluster/gvk"
)

// Resource GVKs used by module manifests.
var (
	Deployment                       = framework.Deployment
	Service                          = framework.Service
	ServiceAccount                   = framework.ServiceAccount
	ConfigMap                        = framework.ConfigMap
	Role                             = framework.Role
	RoleBinding                      = framework.RoleBinding
	ClusterRole                      = framework.ClusterRole
	ClusterRoleBinding               = framework.ClusterRoleBinding
	CustomResourceDefinition         = framework.CustomResourceDefinition
	MutatingWebhookConfiguration     = framework.MutatingWebhookConfiguration
	ValidatingWebhookConfiguration   = framework.ValidatingWebhookConfiguration
	ValidatingAdmissionPolicy        = framework.ValidatingAdmissionPolicy
	ValidatingAdmissionPolicyBinding = framework.ValidatingAdmissionPolicyBinding
	CertManagerIssuer                = framework.CertManagerIssuer
	CertManagerCertificate           = framework.CertManagerCertificate
	NetworkPolicy                    = framework.NetworkPolicy
	SecurityContextConstraints       = schema.GroupVersionKind{
		Group:   "security.openshift.io",
		Version: "v1",
		Kind:    "SecurityContextConstraints",
	}
)
