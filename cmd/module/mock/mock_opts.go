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

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/validate/content"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/cluster"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	modmock "github.com/lburgazzoli/odh-platform-tools/pkg/module/mock"
)

type flags struct {
	apiVersion        string
	kind              string
	platformConfigMap string
}

func (f flags) options(getter genericclioptions.RESTClientGetter) (modmock.Options, error) {
	gv, err := schema.ParseGroupVersion(f.apiVersion)
	if err != nil || !validCustomGroupVersion(gv) {
		return modmock.Options{}, invalidFlag("--api-version must be a custom resource group/version")
	}
	if len(validation.IsDNS1035Label(strings.ToLower(f.kind))) != 0 {
		return modmock.Options{}, invalidFlag("--kind must be a valid custom resource kind")
	}
	if len(content.IsDNS1123Subdomain(f.platformConfigMap)) != 0 {
		return modmock.Options{}, invalidFlag("--platform-configmap must be a valid ConfigMap name")
	}

	gvk := gv.WithKind(f.kind)
	if clientgoscheme.Scheme.Recognizes(gvk) || (gvk.Group == apiextensionsv1.GroupName && gvk.Kind == "CustomResourceDefinition") {
		return modmock.Options{}, invalidFlag("--api-version and --kind must select a custom resource")
	}

	namespace, err := cluster.ResolveNamespace(getter)
	if err != nil {
		return modmock.Options{}, err
	}
	if len(content.IsDNS1123Label(namespace)) != 0 {
		return modmock.Options{}, invalidFlag("--namespace must be a valid namespace name")
	}

	return modmock.Options{GVK: gvk, Namespace: namespace, PlatformConfigMap: f.platformConfigMap}, nil
}

func validCustomGroupVersion(gv schema.GroupVersion) bool {
	return strings.Contains(gv.Group, ".") &&
		len(content.IsDNS1123Subdomain(gv.Group)) == 0 &&
		len(validation.IsDNS1035Label(gv.Version)) == 0
}

func invalidFlag(message string) error {
	return moduleerrors.New(moduleerrors.ErrInvalidMockOptions, message)
}
