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

// DefaultPlatformType is the platform identity applied when the
// invocation sets none, matching the operator's Open Data Hub
// fallback.
const DefaultPlatformType = "OpenDataHub"

// DefaultFieldManager is the server-side-apply field manager the
// preparation writes — the embedded definition CRD and the definition
// itself — and the ordinary payload apply use when the invocation
// sets none. It shares the platform identity the framework itself
// uses for bundled payload CRDs.
const DefaultFieldManager = "platform.opendatahub.io"

// DefaultWaitTimeout is the shared rollout-wait budget applied when
// the invocation requests the wait without an explicit budget.
const DefaultWaitTimeout = 30 * time.Second

// DefaultWaitPollInterval is the pause between rollout observation
// rounds applied when the invocation sets no interval.
const DefaultWaitPollInterval = time.Second

// Options are the invocation settings of one module deploy, and the
// target of the constructor's functional options: a whole Options can
// be passed to New as a bundled preset, and WithXxx options override
// individual fields in application order. They carry no credentials:
// the Kubernetes client arrives as an already-built dependency, and
// the composition layer owns its construction from the standard
// cli-runtime configuration.
type Options struct {
	// DefinitionFile is the module definition file to run. It is
	// required and must name an explicit file; there is no default
	// discovery or fallback.
	DefinitionFile string

	// RelatedImagesFile is the explicitly selected dotenv-compatible
	// image source file. Empty means environment-only resolution.
	RelatedImagesFile string

	// Namespace is where the module controller package is
	// rendered and its platform config ConfigMap is created. It is
	// required: the deployer resolves no namespace on its own.
	Namespace string

	// MonitoringNamespace is the monitoring namespace injected as
	// MONITORING_NAMESPACE. Empty omits the env var.
	MonitoringNamespace string

	// PlatformType is the platform identity injected as
	// ODH_MODULE_OPERATOR_PLATFORM_TYPE. Empty defaults to
	// DefaultPlatformType.
	PlatformType string

	// PlatformVersion is the platform version. It is required and
	// parsed once, with semver.ParseTolerant semantics, into the
	// framework release the injector and deploy stages share.
	PlatformVersion string

	// Timeout is the overall invocation budget. It is required and
	// must be positive; the deploy command defaults its flag to five
	// minutes, and the orchestration derives the invocation context
	// deadline from it.
	Timeout time.Duration

	// Client is the Kubernetes client the deploy writes through. It
	// is required — the deployer never constructs one — and is used
	// exactly as supplied. WithClient is the functional-option form.
	Client crclient.Client

	// Wait requests the opt-in controller rollout wait after a
	// successful deploy. It is false by default: apply completion
	// alone is command success, and the default invocation performs no
	// workload or Deployment-count check and never claims readiness.
	Wait bool

	// WaitTimeout is the shared budget of the requested wait across
	// every selected controller Deployment, started after the apply.
	// Zero means DefaultWaitTimeout; negative is invalid. The budget
	// is a child of the overall invocation deadline and can never
	// extend the remaining overall timeout.
	WaitTimeout time.Duration

	// WaitPollInterval is the pause between rollout observation
	// rounds. Zero means DefaultWaitPollInterval; negative is
	// invalid. The budget, not the interval, is the primary
	// user-facing control.
	WaitPollInterval time.Duration

	// FieldManager is the server-side-apply field manager the
	// preparation writes and the ordinary payload apply claim their
	// fields under. Empty means DefaultFieldManager. A nonempty value
	// must be a valid Kubernetes label value of at most 63
	// characters — stricter than a raw SSA manager string, because
	// the framework also uses the resolved name as a fallback
	// part-of label value. It identifies field ownership only, never
	// a controller owner reference.
	FieldManager string
}

// ApplyTo implements DeployerOption, so a whole Options can be passed
// to New as a bundled preset: it replaces the resolved configuration
// wholesale, and later options override individual fields.
func (o Options) ApplyTo(target *Options) {
	*target = o
}

// DeployerOption customizes one deployer construction. Options apply
// in order over the zero configuration; a nil option is ignored.
type DeployerOption = option.Option[Options] //nolint:revive // The task-mandated constructor signature names the option type after the deployer.

// WithClient sets the Kubernetes client the deploy writes through,
// replacing any earlier client setting. The client is used exactly as
// supplied.
func WithClient(client crclient.Client) DeployerOption {
	return option.FunctionalOption[Options](func(target *Options) {
		target.Client = client
	})
}

// WithWaitPollInterval sets the pause between rollout observation
// rounds, replacing any earlier interval setting.
func WithWaitPollInterval(interval time.Duration) DeployerOption {
	return option.FunctionalOption[Options](func(target *Options) {
		target.WaitPollInterval = interval
	})
}

// Validate reports whether the options are usable by a deployer whose
// client is already injected. Every failure is an options error
// matching moduleerrors.ErrInvalidDeployOptions, so callers classify
// it as a usage failure before any input or output happens. The
// client is checked last, after every setting the invocation's caller
// owns, because the composition layer validates its settings before
// the connection that supplies the client exists — through
// ValidateSettings — and the deployer's first stage runs this full
// validation once construction has injected the client.
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

// ValidateSettings reports whether every invocation setting except the
// client is usable. It is the composition layer's pre-connection
// preflight: the command-owned flags are rejected as options errors —
// usage failures — before the client, the invocation timer, or any
// input exists. The client requirement itself can only be checked
// after the connection that supplies the client, so it belongs to
// Validate alone.
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
		// The empty value is the default request, not an error; any
		// nonempty value must be a valid nonempty Kubernetes label
		// value of at most 63 characters. The constraint is stated
		// statically: the value itself and the validator's own
		// output never reach the message.
		if len(o.FieldManager) > content.LabelValueMaxLength || len(content.IsLabelValue(o.FieldManager)) != 0 {
			return moduleerrors.New(
				moduleerrors.ErrInvalidDeployOptions,
				"invalid deploy options: FieldManager must be a nonempty Kubernetes label value of at most 63 characters",
			)
		}
	}

	return nil
}
