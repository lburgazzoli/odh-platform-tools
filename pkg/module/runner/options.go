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

package runner

import (
	"errors"
	"fmt"
	"time"
)

// DefaultPlatformType is the platform identity applied when the
// invocation sets none, matching the operator's Open Data Hub
// fallback.
const DefaultPlatformType = "OpenDataHub"

// Options are the invocation settings of one module run. They carry
// no credentials and no duplicate kubeconfig flags: the standard
// getter bound at composition supplies namespace, context, and every
// authentication input.
type Options struct {
	// DefinitionFile is the module definition file to run. It is
	// required and must name an explicit file; there is no default
	// discovery or fallback.
	DefinitionFile string

	// RelatedImagesFile is the explicitly selected dotenv-compatible
	// image source file. Empty means environment-only resolution.
	RelatedImagesFile string

	// ApplicationsNamespace is where the module controller package is
	// rendered and its platform config ConfigMap is created. Empty
	// defaults to the effective installation namespace resolved from
	// the standard getter.
	ApplicationsNamespace string

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
	// must be positive; the run command defaults its flag to five
	// minutes, and the orchestration derives the invocation context
	// deadline from it.
	Timeout time.Duration
}

// validate reports whether the options are usable. Every failure is
// an options error matching ErrInvalidOptions, so callers classify it
// as a usage failure before any input or output happens.
func (o Options) validate() error {
	if o.DefinitionFile == "" {
		return fmt.Errorf("%w: DefinitionFile is required", ErrInvalidOptions)
	}

	if o.PlatformVersion == "" {
		return fmt.Errorf("%w: PlatformVersion is required", ErrInvalidOptions)
	}

	if o.Timeout <= 0 {
		return fmt.Errorf("%w: Timeout must be positive", ErrInvalidOptions)
	}

	return nil
}

// ErrInvalidOptions identifies invalid per-call run options, separate
// from definition, source, render, and deployment error categories.
var ErrInvalidOptions = errors.New("invalid run options")
