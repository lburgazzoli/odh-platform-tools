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

package cluster

import (
	"time"

	kindcluster "sigs.k8s.io/kind/pkg/cluster"
)

// Defaults of the managed Kind engine. The node image pin is the
// recorded kindest/node image of the Kubernetes line this repository
// builds against (v1.37.0, digest from the kind release notes); the
// wait and supervision budgets bound operations whose provider API
// takes no context.
const (
	DefaultNamePrefix     = "odh-platform-tools-e2e-"
	DefaultNodeImage      = "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"
	DefaultWaitForReady   = 3 * time.Minute
	DefaultCreateBudget   = 10 * time.Minute
	DefaultDeleteBudget   = 5 * time.Minute
	DefaultLogsBudget     = 2 * time.Minute
	DefaultSuperviseGrace = 30 * time.Second
)

// InvalidOptionsError reports engine options that cannot produce a
// safe engine.
type InvalidOptionsError struct {
	Reason string
}

// Error implements the error interface.
func (e *InvalidOptionsError) Error() string {
	return "invalid cluster engine options: " + e.Reason
}

// Option customizes one engine. Options apply in order over the
// recorded defaults; a nil option is ignored.
type Option interface {
	apply(target *options)
}

type options struct {
	Type            string
	NamePrefix      string
	NodeImage       string
	LogsDir         string
	LogFn           func(format string, args ...any)
	ProviderOptions []kindcluster.ProviderOption
	CreateOptions   []kindcluster.CreateOption
	WaitForReady    time.Duration
	CreateBudget    time.Duration
	DeleteBudget    time.Duration
	LogsBudget      time.Duration
	SuperviseGrace  time.Duration

	// Kubeconfig and Context select the external engine's confirmed
	// configuration. Both are required there; neither is ever read
	// from an ambient current context.
	Kubeconfig string
	Context    string

	// provider overrides the Kind provider seam; production uses the
	// real kind provider.
	provider provider
}

// newOptions applies the options over the recorded defaults.
func newOptions(opts ...Option) options {
	defaults := options{
		NamePrefix:     DefaultNamePrefix,
		NodeImage:      DefaultNodeImage,
		WaitForReady:   DefaultWaitForReady,
		CreateBudget:   DefaultCreateBudget,
		DeleteBudget:   DefaultDeleteBudget,
		LogsBudget:     DefaultLogsBudget,
		SuperviseGrace: DefaultSuperviseGrace,
		LogFn: func(_ string, _ ...any) {
			// Silent by default: engines log through the caller's
			// logger when the harness supplies one.
		},
	}

	for _, opt := range opts {
		if opt != nil {
			opt.apply(&defaults)
		}
	}

	return defaults
}

type optionFunc func(target *options)

// applyOption implements Option.
func (fn optionFunc) apply(target *options) {
	if fn == nil || target == nil {
		return
	}

	fn(target)
}

// WithType selects the engine: TypeKind (the default) or
// TypeExternal.
func WithType(engineType string) Option {
	return optionFunc(func(target *options) {
		if engineType != "" {
			target.Type = engineType
		}
	})
}

// WithNamePrefix sets the prefix of the managed Kind cluster name.
func WithNamePrefix(prefix string) Option {
	return optionFunc(func(target *options) {
		if prefix != "" {
			target.NamePrefix = prefix
		}
	})
}

// WithNodeImage overrides the recorded default node image pin.
func WithNodeImage(image string) Option {
	return optionFunc(func(target *options) {
		if image != "" {
			target.NodeImage = image
		}
	})
}

// WithLogsDir overrides the directory failure logs are collected
// into. The default is a private directory under the engine's own
// temporary root.
func WithLogsDir(dir string) Option {
	return optionFunc(func(target *options) {
		if dir != "" {
			target.LogsDir = dir
		}
	})
}

// WithLogFn sets the engine's progress logger.
func WithLogFn(logFn func(format string, args ...any)) Option {
	return optionFunc(func(target *options) {
		if logFn != nil {
			target.LogFn = logFn
		}
	})
}

// WithProviderOption appends one kind provider option.
func WithProviderOption(option kindcluster.ProviderOption) Option {
	return optionFunc(func(target *options) {
		if option != nil {
			target.ProviderOptions = append(target.ProviderOptions, option)
		}
	})
}

// WithCreateOption appends one kind create option after the
// engine's own image, wait, and private-kubeconfig options.
func WithCreateOption(option kindcluster.CreateOption) Option {
	return optionFunc(func(target *options) {
		if option != nil {
			target.CreateOptions = append(target.CreateOptions, option)
		}
	})
}

// WithWaitForReady bounds the Kind create's own readiness wait.
func WithWaitForReady(wait time.Duration) Option {
	return optionFunc(func(target *options) {
		if wait > 0 {
			target.WaitForReady = wait
		}
	})
}

// WithCreateBudget bounds the supervised cluster creation.
func WithCreateBudget(budget time.Duration) Option {
	return optionFunc(func(target *options) {
		if budget > 0 {
			target.CreateBudget = budget
		}
	})
}

// WithDeleteBudget bounds the supervised cluster deletion.
func WithDeleteBudget(budget time.Duration) Option {
	return optionFunc(func(target *options) {
		if budget > 0 {
			target.DeleteBudget = budget
		}
	})
}

// WithLogsBudget bounds the supervised log collection.
func WithLogsBudget(budget time.Duration) Option {
	return optionFunc(func(target *options) {
		if budget > 0 {
			target.LogsBudget = budget
		}
	})
}

// WithSuperviseGrace bounds the join of a terminated operation
// before its supervisor reports a leak.
func WithSuperviseGrace(grace time.Duration) Option {
	return optionFunc(func(target *options) {
		if grace > 0 {
			target.SuperviseGrace = grace
		}
	})
}

// WithKubeconfig sets the external engine's explicit kubeconfig
// file path.
func WithKubeconfig(path string) Option {
	return optionFunc(func(target *options) {
		if path != "" {
			target.Kubeconfig = path
		}
	})
}

// WithContext sets the external engine's explicit kubeconfig
// context. There is no current-context fallback.
func WithContext(name string) Option {
	return optionFunc(func(target *options) {
		if name != "" {
			target.Context = name
		}
	})
}

// validate reports options no engine can run safely with.
func (o options) validate() error {
	if o.NamePrefix == "" {
		return &InvalidOptionsError{Reason: "name prefix is empty"}
	}

	if o.NodeImage == "" {
		return &InvalidOptionsError{Reason: "node image is empty"}
	}

	if o.LogFn == nil {
		return &InvalidOptionsError{Reason: "log function is nil"}
	}

	return nil
}
