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

package module

import (
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/helm"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer/kustomize"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/cluster"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/runner/deploy"
)

// NewRunner returns the Cobra-free staged module run action wired
// with the concrete reusable services: the strict loader, both
// manifest backend renderers, the standard cluster connection, and
// the framework payload apply; the definition owner preparation is
// the runner's own step over that connection. Callers pass a plain
// runner.Options struct, the standard getter, and stage contexts;
// the action itself performs no flag parsing, printing, or
// environment resolution.
func NewRunner(options runner.Options, getter genericclioptions.RESTClientGetter) *runner.Runner {
	return runner.New(
		options,
		getter,
		loader.LoadFromPath,
		helm.RenderWithOptions,
		kustomize.RenderWithOptions,
		cluster.Connect,
		deploy.Apply,
	)
}
