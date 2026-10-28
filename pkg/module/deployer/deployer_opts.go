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

package deployer

import (
	"time"

	"k8s.io/apimachinery/pkg/api/validate/content"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
	"github.com/lburgazzoli/odh-platform-tools/pkg/util/option"
)

// DefaultPlatformType is the platform identity injected when none is supplied.
const DefaultPlatformType = "OpenDataHub"

// DefaultFieldManager owns definition and ordinary payload fields by default.
const DefaultFieldManager = "platform.opendatahub.io"

// DefaultWaitTimeout is the default shared rollout budget.
const DefaultWaitTimeout = 30 * time.Second

// DefaultWaitPollInterval is the default rollout observation interval.
const DefaultWaitPollInterval = time.Second

// Options configures one deployment. It can be passed to New as a preset.
type Options struct {
	// DefinitionFile is the required, explicit module definition path.
	DefinitionFile string

	// RelatedImagesFile selects a dotenv file; empty uses only the environment.
	RelatedImagesFile string

	// Namespace is the required rendering and platform-config namespace.
	Namespace string

	// MonitoringNamespace sets MONITORING_NAMESPACE; empty omits it.
	MonitoringNamespace string

	// PlatformType defaults to DefaultPlatformType.
	PlatformType string

	// PlatformVersion is required and parsed with semver.ParseTolerant.
	PlatformVersion string

	// Timeout is the required positive invocation budget.
	Timeout time.Duration

	// Client is required and used unchanged.
	Client crclient.Client

	// Wait enables controller rollout observation after applying resources.
	Wait bool

	// WaitTimeout defaults to DefaultWaitTimeout when zero. It cannot extend
	// the overall invocation deadline.
	WaitTimeout time.Duration

	// WaitPollInterval defaults to DefaultWaitPollInterval when zero.
	WaitPollInterval time.Duration

	// FieldManager defaults to DefaultFieldManager. It must be a Kubernetes label
	// value (at most 63 characters) because the framework also uses it as a label.
	FieldManager string
}

// ApplyTo replaces the configuration with this preset.
func (o Options) ApplyTo(target *Options) {
	*target = o
}

// DeployerOption customizes construction; options apply in order.
type DeployerOption = option.Option[Options] //nolint:revive // The task-mandated constructor signature names the option type after the deployer.

// WithClient supplies the Kubernetes client.
func WithClient(client crclient.Client) DeployerOption {
	return option.FunctionalOption[Options](func(target *Options) {
		target.Client = client
	})
}

// WithWaitPollInterval sets the rollout observation interval.
func WithWaitPollInterval(interval time.Duration) DeployerOption {
	return option.FunctionalOption[Options](func(target *Options) {
		target.WaitPollInterval = interval
	})
}

// Validate checks settings and the required client, returning ErrInvalidDeployOptions.
func (o Options) Validate() error {
	if err := o.ValidateSettings(); err != nil {
		return err
	}

	if o.Client == nil {
		return moduleerrors.New(
			moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: Client is required",
		)
	}

	return nil
}

// ValidateSettings checks settings before a client is constructed.
func (o Options) ValidateSettings() error {
	if o.DefinitionFile == "" {
		return moduleerrors.New(
			moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: DefinitionFile is required",
		)
	}

	if o.PlatformVersion == "" {
		return moduleerrors.New(
			moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: PlatformVersion is required",
		)
	}

	if o.Namespace == "" {
		return moduleerrors.New(
			moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: Namespace is required",
		)
	}

	if o.Timeout <= 0 {
		return moduleerrors.New(
			moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: Timeout must be positive",
		)
	}

	if o.Wait && o.WaitTimeout < 0 {
		return moduleerrors.New(
			moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: WaitTimeout must not be negative",
		)
	}

	if o.WaitPollInterval < 0 {
		return moduleerrors.New(
			moduleerrors.ErrInvalidDeployOptions,
			"invalid deploy options: WaitPollInterval must not be negative",
		)
	}

	if o.FieldManager != "" {
		if len(o.FieldManager) > content.LabelValueMaxLength || len(content.IsLabelValue(o.FieldManager)) != 0 {
			return moduleerrors.New(
				moduleerrors.ErrInvalidDeployOptions,
				"invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters",
			)
		}
	}

	return nil
}
