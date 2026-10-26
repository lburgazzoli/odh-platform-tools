// Package adapt adapts rendered module resources before deployment.
//
// It provides two engine post-renderers carrying the
// engine types.PostRenderer signature, which the runner registers on
// the shared rendering engine after the library apply-order
// post-renderer: EnvInjector injects the resolved controller image and
// the module environment into the module's controller Deployment, and
// PlatformConfigInjector creates or merges the per-module platform
// config ConfigMap. Both read only the runner-owned inputs — the
// module definition, the resolved image snapshot, the target
// namespaces, and the platform version — and do not modify them.
package adapt
