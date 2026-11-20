---
name: odh-platform-tools
description: Run a Helm chart or Kustomize tree as an OpenDataHub platform module with the odh-platform-tools CLI — generate a PlatformModuleDefinition, lint the controller package offline, deploy it to a cluster, apply the module instance separately, validate the running module against the platform contract, run the status-only mock controller, and adapt the GitHub Actions module CI workflow with disposable Kind clusters. Use when onboarding, authoring, or troubleshooting a platform module, or wiring module generate, lint, deploy, or validate checks into CI. Not for developing the odh-platform-tools repository itself, writing module operators, or general Kubernetes, Helm, Kustomize, and GitHub Actions questions unrelated to the platform module contract.
---

# OpenDataHub platform module onboarding

## Mental model

A module is described by one `PlatformModuleDefinition` — a YAML file plus a
local manifest source (Helm chart or Kustomize tree). The five `module`
commands are separate steps, not one orchestrated pipeline:

- `generate` infers a definition from the local source, offline. The output
  file is created once and never overwritten — every run needs a fresh path.
- `lint` renders the definition's source offline and checks the package. No
  cluster access.
- `deploy` installs the controller package (CRD, definition, resources) on a
  cluster. It never creates the module instance.
- The module instance — the module custom resource — is applied separately,
  after deploy.
- `validate` checks the running module against the platform contract,
  read-only, with automatic waiting.
- `mock` runs a status-only test controller for a compatible module resource.
  It is a test stand-in, never the real module.

## Start here

Read the shared onboarding guide first — it defines the pinned invocation,
the preflight interview, the ordered path for Helm and Kustomize sources,
cluster targeting, and the CI workflow layout that every recipe assumes:

- [Module onboarding](resources/cmd/module.md)

## Recipes

- [Generate a definition from a local source](resources/cmd/module/generate.md)
- [Lint a definition offline](resources/cmd/module/lint.md)
- [Deploy a controller package](resources/cmd/module/deploy.md)
- [Validate a running module](resources/cmd/module/validate.md)
- [Run the mock module controller](resources/cmd/module/mock.md)

## Workflow template

- [module-tck.yaml](resources/templates/module-tck.yaml) — the one
  GitHub Actions template to adapt: PR runs generate and lint only; manual
  runs additionally use a disposable Kind cluster. See
  [Module onboarding](resources/cmd/module.md) for the adaptation walkthrough.

## Hard rules

- Pin the tools: `ODH_TOOLS_REF` must be an explicitly chosen release tag or
  commit accepted by Go — `main` only while the tools iterate without
  releases, never an invented version.
- Generate fresh in every run, including CI. Never treat a committed
  definition as the CI input.
- Never create a cluster, deploy, or use credentials without explicit user
  approval. `deploy` and `mock` write to a cluster; `validate` is read-only.
- Passing lint is not a trust decision: review source, images, and
  prerequisite manifests before any credential-bearing live run.
