// Package adapt applies the operator's post-render transformations to
// rendered module resources: module environment and image injection
// into the controller Deployment, and the per-module platform config
// ConfigMap.
//
// The transformations are operator-identical ports of the modules
// controller pipeline actions (injectModuleEnv,
// injectPlatformConfig) at commit 5a1d17f57b32, reduced to a single
// module with runtime inputs supplied at action construction. Both
// ports are framework actions — functions over the shared framework
// types.ReconciliationRequest that mutate rr.Resources in place — so
// a runner composes them with the framework render and deploy actions
// into one pipeline, exactly as the operator's controller composes
// its chain. Operator changes port with a minimal diff, and an
// exported upstream function can replace its port here without
// touching the composition.
//
// Two approved behavioral boundaries exist. First, the image source:
// where the operator reads RELATED_IMAGE_* values from the process
// environment, the env action resolves them from the caller-supplied
// snapshot produced by the images service, and the configured
// controller image may be a literal pipeline-built image as well as a
// whole {{NAME}} reference, so the main and init containers only ever
// receive the same nonempty image. Second, the operator's
// upgrade-gate extraction and checking and its XKS cert-manager
// bootstrap are platform concerns with no one-shot-install analogue
// and are deliberately absent; the platform version comes from
// rr.Release.Version, read where upstream reads it.
package adapt
