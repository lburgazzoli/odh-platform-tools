/*
Copyright 2026 Luca Burgazzoli

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mock

import (
	"strings"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/validate/content"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

// Options identifies the custom resource kind and the ConfigMap the controller watches.
type Options struct {
	APIVersion        string
	Kind              string
	PlatformConfigMap string
	Namespace         string
}

// Validate checks local settings without opening a cluster connection.
func (o Options) Validate() error {
	_, err := o.groupVersionKind()
	return err
}

func (o Options) groupVersionKind() (schema.GroupVersionKind, error) {
	groupVersion, err := schema.ParseGroupVersion(o.APIVersion)
	switch {
	case err != nil, !strings.Contains(groupVersion.Group, "."), len(content.IsDNS1123Subdomain(groupVersion.Group)) != 0, len(validation.IsDNS1035Label(groupVersion.Version)) != 0:
		return schema.GroupVersionKind{}, moduleerrors.New(moduleerrors.ErrInvalidMockOptions, "--api-version must be a custom resource group/version")
	case len(validation.IsDNS1035Label(strings.ToLower(o.Kind))) != 0:
		return schema.GroupVersionKind{}, moduleerrors.New(moduleerrors.ErrInvalidMockOptions, "--kind must be a valid custom resource kind")
	case len(content.IsDNS1123Subdomain(o.PlatformConfigMap)) != 0:
		return schema.GroupVersionKind{}, moduleerrors.New(moduleerrors.ErrInvalidMockOptions, "--platform-configmap must be a valid ConfigMap name")
	case o.Namespace != "" && len(content.IsDNS1123Label(o.Namespace)) != 0:
		return schema.GroupVersionKind{}, moduleerrors.New(moduleerrors.ErrInvalidMockOptions, "--namespace must be a valid namespace name")
	}

	gvk := groupVersion.WithKind(o.Kind)
	if clientgoscheme.Scheme.Recognizes(gvk) || gvk.GroupKind() == (schema.GroupKind{Group: apiextensionsv1.GroupName, Kind: "CustomResourceDefinition"}) {
		return schema.GroupVersionKind{}, moduleerrors.New(moduleerrors.ErrInvalidMockOptions, "--api-version and --kind must select a custom resource")
	}

	return gvk, nil
}
