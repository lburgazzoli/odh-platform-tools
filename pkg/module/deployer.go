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

	deployer "github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/deployer/cluster"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/loader"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// NewDeployer returns the Cobra-free staged module deploy action wired
// with the concrete reusable services: the loader, the centralized
// rendering service, the standard cluster connection, and the
// framework payload apply; the definition owner preparation is
// the deployer's own step over that connection. Callers pass a plain
// deployer.Options struct, the standard getter, and stage contexts;
// the action itself performs no flag parsing, printing, or
// environment resolution.
func NewDeployer(options deployer.Options, getter genericclioptions.RESTClientGetter) *deployer.Deployer {
	return deployer.New(
		options,
		getter,
		loader.LoadFromPath,
		renderer.Render,
		cluster.Connect,
		deployer.DeployPayload,
	)
}
