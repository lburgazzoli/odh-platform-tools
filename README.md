# odh-platform-tools

Tools for OpenDataHub platform modules. `module generate` infers a
`PlatformModuleDefinition` from local Helm or Kustomize manifests.
`module lint` validates a rendered module package offline. `module deploy`
installs the controller package in one pass and can wait for its Deployment
rollout. `module mock` runs a status-only controller for testing the module
contract.

## Quickstart

Lint the Helm package from the repository root.

```sh
go run ./cmd module lint --config examples/helm/chart/module.yaml
```

Generate a definition for the same package, then lint it.

```sh
go run ./cmd module generate examples/helm/chart --output examples/helm/generated.yaml
go run ./cmd module lint --config examples/helm/generated.yaml
```

The Helm and Kustomize examples both run the mock controller from the
tools image. Build and load the image, deploy the packages, and apply
their module instances separately. The walkthrough also exercises the
condition and platform version controls. [Run the examples](docs/mock.md)

## Build

```sh
make build      # build all packages
make verify     # fmt-check, vet, lint, unit tests
```

## Documentation

- [Module definitions](docs/modules.md) — authoring a
  `PlatformModuleDefinition`
- [CLI reference](docs/cli.md) — commands, flags, reports, exit codes
- [Architecture](docs/architecture.md) — packages and pipelines
- [Development](docs/development.md) — layout, conventions, commands
- [Testing](docs/testing.md) — suites, style, e2e engines
